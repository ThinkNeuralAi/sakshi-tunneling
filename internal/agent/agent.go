// Package agent wires the four components into one supervised loop:
//
//	discovery  -> find cameras on the LAN (ONVIF) and via the NVR login
//	pull       -> ffmpeg pulls each sub-stream locally, sampled to N fps
//	frame-push -> sampled JPEGs go out over the WebSocket
//	heartbeat  -> periodic health report keeps the store visible in cloud
//
// Everything is outbound and supervised: a dead camera restarts with
// backoff, a dropped cloud link reconnects, and the process is safe to run
// headless as a Windows service.
package agent

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sakshi.example/edge-agent/internal/cloud"
	"sakshi.example/edge-agent/internal/config"
	"sakshi.example/edge-agent/internal/discovery"
	"sakshi.example/edge-agent/internal/nvr"
	"sakshi.example/edge-agent/internal/puller"
)

const Version = "0.1.0"

// Agent holds runtime state for one store.
type Agent struct {
	cfg        config.Config
	cfgPath    string
	client     *cloud.Client
	startedAt  time.Time
	framesSent atomic.Int64

	mu       sync.Mutex
	fps      float64
	channels []nvr.Channel
}

// New builds an agent from config.
func New(cfg config.Config, cfgPath string) *Agent {
	return &Agent{
		cfg:       cfg,
		cfgPath:   cfgPath,
		fps:       cfg.SampleFPS,
		startedAt: time.Now(),
	}
}

// Run is the top-level supervised loop. It returns only when ctx is
// cancelled (service stop); transient failures are retried internally.
func (a *Agent) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := a.session(ctx); err != nil && ctx.Err() == nil {
			log.Printf("session ended: %v; reconnecting in 5s", err)
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
			}
		}
	}
	return ctx.Err()
}

// session runs one full cloud connection lifecycle.
func (a *Agent) session(ctx context.Context) error {
	a.client = cloud.NewClient(a.cfg.CloudURL, Version)
	a.client.CommandHandler = a.onCommand

	storeID, err := a.client.Connect(ctx, a.cfg.EnrollmentToken, a.cfg.StoreID)
	if err != nil {
		return err
	}
	defer a.client.Close()

	// Persist the store id the cloud assigned, so re-enrollment is a no-op
	// next boot.
	if storeID != a.cfg.StoreID {
		a.cfg.StoreID = storeID
		if err := config.Save(a.cfgPath, a.cfg); err != nil {
			log.Printf("warning: could not persist store id: %v", err)
		}
	}
	log.Printf("enrolled as store %s", storeID)

	// Discover the camera inventory (best-effort ONVIF + authoritative NVR).
	channels, err := a.discover(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.channels = channels
	a.mu.Unlock()

	infos := make([]cloud.ChannelInfo, 0, len(channels))
	for _, c := range channels {
		infos = append(infos, c.ToInfo(true))
	}
	if err := a.client.SendChannels(infos); err != nil {
		return err
	}
	log.Printf("reported %d channels", len(channels))

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	// One supervised puller per channel.
	for _, ch := range channels {
		url := ch.SubURL
		if !a.cfg.PreferSubStream || url == "" {
			url = ch.MainURL
		}
		wg.Add(1)
		go func(channelID, rtsp string) {
			defer wg.Done()
			a.superviseChannel(sessCtx, channelID, rtsp)
		}(ch.ID, url)
	}

	// Heartbeat loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.heartbeatLoop(sessCtx)
	}()

	// Inbound control loop. Its return (conn drop) ends the session.
	err = a.client.ReadLoop(sessCtx)
	cancel()
	wg.Wait()
	return err
}

// discover merges ONVIF LAN discovery with authoritative NVR enumeration.
func (a *Agent) discover(ctx context.Context) ([]nvr.Channel, error) {
	if a.cfg.DiscoveryEnabled {
		if devs, err := discovery.Probe(ctx, 3*time.Second); err == nil {
			log.Printf("ONVIF discovery saw %d device(s) on the LAN", len(devs))
		}
	}
	adapter, err := nvr.New(a.cfg.NVR)
	if err != nil {
		return nil, err
	}
	log.Printf("enumerating channels via %s adapter", adapter.Vendor())
	return adapter.Enumerate(ctx)
}

// superviseChannel keeps one channel's puller alive with backoff.
func (a *Agent) superviseChannel(ctx context.Context, channelID, rtsp string) {
	backoff := time.Second
	for ctx.Err() == nil {
		a.mu.Lock()
		fps := a.fps
		a.mu.Unlock()

		log.Printf("channel %s pulling %s", channelID, redactRTSP(rtsp))
		err := puller.Run(ctx, puller.Options{
			ChannelID: channelID,
			RTSPURL:   rtsp,
			FPS:       fps,
			TCP:       true,
		}, a.onFrame)

		if ctx.Err() != nil {
			return
		}
		log.Printf("channel %s puller stopped: %v; retry in %s", channelID, err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// onFrame ships one sampled JPEG to the cloud.
func (a *Agent) onFrame(f puller.Frame) {
	if err := a.client.PushFrame(f.ChannelID, f.JPEG, f.CapturedAt, f.Seq); err != nil {
		log.Printf("push frame (ch %s) failed: %v", f.ChannelID, err)
		return
	}
	a.framesSent.Add(1)
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(a.cfg.HeartbeatInterval)
	defer t.Stop()
	var last int64
	lastAt := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			sent := a.framesSent.Load()
			fps := float64(sent-last) / now.Sub(lastAt).Seconds()
			last, lastAt = sent, now

			a.mu.Lock()
			active := len(a.channels)
			a.mu.Unlock()

			_ = a.client.SendHeartbeat(cloud.Heartbeat{
				SentAt:       now.UTC(),
				UptimeSec:    int64(time.Since(a.startedAt).Seconds()),
				ActiveChans:  active,
				FramesPushed: sent,
				FpsActual:    fps,
			})
		}
	}
}

// onCommand applies a control instruction pushed from the cloud.
func (a *Agent) onCommand(cmd cloud.Command) {
	switch cmd.Action {
	case "set_fps":
		a.mu.Lock()
		a.fps = cmd.FPS
		a.mu.Unlock()
		log.Printf("cloud set sample fps to %g (applies on next channel restart)", cmd.FPS)
	default:
		log.Printf("unhandled command %q", cmd.Action)
	}
}

func redactRTSP(u string) string {
	at := strings.Index(u, "@")
	scheme := strings.Index(u, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return u
	}
	return u[:scheme+3] + "***@" + u[at+1:]
}
