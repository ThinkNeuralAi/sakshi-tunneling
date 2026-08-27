// Package puller connects to a camera's RTSP stream on the local LAN and
// emits sampled JPEG frames. It shells out to ffmpeg rather than decoding
// H.264/H.265 in-process: ffmpeg handles the messy reality of vendor
// codec quirks, and running it as a subprocess keeps a bad stream from
// taking down the agent.
//
// Bandwidth note: we pull the SUB-stream (low bitrate) and further sample
// to a few fps, so the data that eventually leaves the store is a tiny
// fraction of the raw feed. Nothing here goes to the cloud; this stays on
// the LAN. The cloud client decides what to forward.
package puller

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Frame is one sampled still.
type Frame struct {
	ChannelID  string
	JPEG       []byte
	CapturedAt time.Time
	Seq        int64
}

// Options configure a single channel puller.
type Options struct {
	ChannelID string
	RTSPURL   string
	FPS       float64
	// TCP forces RTSP-over-TCP. Strongly recommended over the internet
	// and even on busy LANs; UDP RTSP loses packets and confuses NAT.
	TCP bool
}

// Run pulls frames until ctx is cancelled, delivering each to onFrame.
// It blocks; run it in a goroutine per channel. On ffmpeg exit it returns
// the error so the supervisor can back off and restart.
func Run(ctx context.Context, opt Options, onFrame func(Frame)) error {
	if opt.FPS <= 0 {
		opt.FPS = 1
	}
	transport := "udp"
	if opt.TCP {
		transport = "tcp"
	}

	// Do not use -vf fps=N: when RTSP stalls, that filter duplicates the
	// last picture and we push identical JPEGs (seq climbs, image does not).
	// Decode every frame, sample in process by wall clock, skip duplicates.
	args := []string{
		"-loglevel", "error",
		"-rtsp_transport", transport,
		"-fflags", "nobuffer+discardcorrupt",
		"-flags", "low_delay",
		"-i", opt.RTSPURL,
		"-an",
		"-vsync", "0",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-q:v", "5",
		"pipe:1",
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	interval := time.Duration(float64(time.Second) / opt.FPS)
	var seq int64
	var lastAt time.Time
	var lastJPEG []byte
	err = splitJPEG(bufio.NewReaderSize(stdout, 1<<20), func(jpeg []byte) {
		now := time.Now()
		if !lastAt.IsZero() && now.Sub(lastAt) < interval {
			return
		}
		if bytes.Equal(jpeg, lastJPEG) {
			return
		}
		lastAt = now
		lastJPEG = append(lastJPEG[:0], jpeg...)
		seq++
		onFrame(Frame{
			ChannelID:  opt.ChannelID,
			JPEG:       jpeg,
			CapturedAt: now.UTC(),
			Seq:        seq,
		})
	})

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err() // clean shutdown
	}
	if err != nil {
		return fmt.Errorf("read frames: %w", err)
	}
	return waitErr
}

// JPEG frames start with FFD8 and end with FFD9. splitJPEG scans the
// concatenated stream and hands each complete image to cb.
func splitJPEG(r *bufio.Reader, cb func([]byte)) error {
	const (
		soi1, soi2 = 0xFF, 0xD8
		eoi1, eoi2 = 0xFF, 0xD9
	)
	var buf []byte
	inImage := false
	var prev byte

	for {
		b, err := r.ReadByte()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if !inImage {
			if prev == soi1 && b == soi2 {
				inImage = true
				buf = append(buf[:0], soi1, soi2)
			}
			prev = b
			continue
		}
		buf = append(buf, b)
		if prev == eoi1 && b == eoi2 {
			img := make([]byte, len(buf))
			copy(img, buf)
			cb(img)
			inImage = false
			prev = 0
			continue
		}
		prev = b
	}
}
