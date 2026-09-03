// Package restream turns inbound JPEGs into a local RTSP URL that an
// existing CV pipeline can open with VideoCapture / DeepStream.
//
// Each camera gets one ffmpeg process that repeats the latest still at a
// constant fps (so a 1 fps agent still looks like a live H.264 stream)
// and publishes it to MediaMTX:
//
//	rtsp://127.0.0.1:8554/{store_id}/{channel_id}
package restream

import (
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

var safe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// Hub is the process-wide registry of per-camera publishers.
type Hub struct {
	mtxBase  string
	fps      int
	restream bool

	mu    sync.Mutex
	chans map[string]*Publisher
}

// Info is a snapshot for GET /streams.
type Info struct {
	StoreID   string    `json:"store_id"`
	ChannelID string    `json:"channel_id"`
	RTSPURL   string    `json:"rtsp_url"`
	Seq       int64     `json:"seq"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewHub stores inbound JPEGs. If restream is true, each camera is also
// republished to MediaMTX via ffmpeg. Turn restream off when MediaMTX is
// not running — the ffmpeg crash-loop can stall the WebSocket and freeze
// /latest/ on the first frame.
func NewHub(mtxBase string, fps int, restream bool) *Hub {
	if fps <= 0 {
		fps = 15
	}
	return &Hub{
		mtxBase:  mtxBase,
		fps:      fps,
		restream: restream,
		chans:    map[string]*Publisher{},
	}
}

// Push updates the latest JPEG for a camera and starts ffmpeg on first frame.
func (h *Hub) Push(storeID, channelID string, seq int64, jpeg []byte) {
	if len(jpeg) == 0 {
		return
	}
	p := h.get(storeID, channelID)
	p.set(seq, jpeg)
}

// List returns every live publisher (for the CV operator to copy URLs).
func (h *Hub) List() []Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Info, 0, len(h.chans))
	for _, p := range h.chans {
		out = append(out, p.info(h.mtxBase))
	}
	return out
}

// LatestJPEG returns the most recent still for a camera, if any.
func (h *Hub) LatestJPEG(storeID, channelID string) ([]byte, bool) {
	key := pathOf(storeID, channelID)
	h.mu.Lock()
	p, ok := h.chans[key]
	h.mu.Unlock()
	if !ok {
		return nil, false
	}
	jpeg := p.snapshot()
	return jpeg, len(jpeg) > 0
}

func (h *Hub) get(storeID, channelID string) *Publisher {
	key := pathOf(storeID, channelID)
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.chans[key]; ok {
		return p
	}
	p := newPublisher(storeID, channelID, h.mtxBase, h.fps)
	h.chans[key] = p
	if h.restream {
		go p.supervise()
	}
	return p
}

// Publisher is one camera → one ffmpeg → one MediaMTX path.
type Publisher struct {
	storeID   string
	channelID string
	path      string
	rtspURL   string
	fps       int

	mu        sync.Mutex
	jpeg      []byte
	seq       int64
	updatedAt time.Time
	ready     chan struct{}
	once      sync.Once
}

func newPublisher(storeID, channelID, mtxBase string, fps int) *Publisher {
	path := pathOf(storeID, channelID)
	p := &Publisher{
		storeID:   storeID,
		channelID: channelID,
		path:      path,
		rtspURL:   fmt.Sprintf("%s/%s", mtxBase, path),
		fps:       fps,
		ready:     make(chan struct{}),
	}
	return p
}

func (p *Publisher) set(seq int64, jpeg []byte) {
	p.mu.Lock()
	p.jpeg = append(p.jpeg[:0], jpeg...)
	p.seq = seq
	p.updatedAt = time.Now()
	p.mu.Unlock()
	p.once.Do(func() { close(p.ready) })
}

func (p *Publisher) snapshot() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.jpeg) == 0 {
		return nil
	}
	out := make([]byte, len(p.jpeg))
	copy(out, p.jpeg)
	return out
}

func (p *Publisher) info(mtxBase string) Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Info{
		StoreID:   p.storeID,
		ChannelID: p.channelID,
		RTSPURL:   fmt.Sprintf("%s/%s", mtxBase, p.path),
		Seq:       p.seq,
		UpdatedAt: p.updatedAt,
	}
}

func (p *Publisher) supervise() {
	<-p.ready
	backoff := time.Second
	for {
		err := p.runOnce()
		log.Printf("restream %s stopped: %v; retry in %s", p.path, err, backoff)
		time.Sleep(backoff)
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (p *Publisher) runOnce() error {
	args := []string{
		"-loglevel", "error",
		"-fflags", "+genpts",
		"-f", "mjpeg",
		"-framerate", fmt.Sprintf("%d", p.fps),
		"-i", "pipe:0",
		"-an",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-pix_fmt", "yuv420p",
		"-r", fmt.Sprintf("%d", p.fps),
		"-g", fmt.Sprintf("%d", p.fps),
		"-f", "rtsp",
		"-rtsp_transport", "tcp",
		p.rtspURL,
	}
	cmd := exec.Command("ffmpeg", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w (is ffmpeg installed?)", err)
	}
	log.Printf("publishing %s (repeat last JPEG at %d fps)", p.rtspURL, p.fps)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	tick := time.NewTicker(time.Second / time.Duration(p.fps))
	defer tick.Stop()

	for {
		select {
		case err := <-done:
			_ = stdin.Close()
			if err == nil {
				return io.EOF
			}
			return err
		case <-tick.C:
			frame := p.snapshot()
			if frame == nil {
				continue
			}
			if _, err := stdin.Write(frame); err != nil {
				_ = cmd.Process.Kill()
				<-done
				return err
			}
		}
	}
}

func pathOf(storeID, channelID string) string {
	return sanitize(storeID) + "/" + sanitize(channelID)
}

func sanitize(s string) string {
	out := safe.ReplaceAllString(s, "_")
	if out == "" {
		return "unknown"
	}
	return out
}
