package nvr

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"sakshi.example/edge-agent/internal/config"
)

func userinfo(cfg config.NVRConfig) string {
	return url.UserPassword(cfg.Username, cfg.Password).String()
}

func configuredChannels(cfg config.NVRConfig) []int {
	if len(cfg.Channels) > 0 {
		out := make([]int, 0, len(cfg.Channels))
		for _, n := range cfg.Channels {
			if n > 0 {
				out = append(out, n)
			}
		}
		return out
	}
	if cfg.ChannelCount > 0 {
		out := make([]int, 0, cfg.ChannelCount)
		for i := 1; i <= cfg.ChannelCount; i++ {
			out = append(out, i)
		}
		return out
	}
	return nil
}

// buildRTSP expands nvr.rtsp_url when set; otherwise uses the vendor default.
//
// Template may be a path ("/cam/realmonitor?channel={channel}&subtype={subtype}")
// or a full URL ("rtsp://{user}:{pass}@{host}:{port}/Streaming/Channels/{channel_id}").
func buildRTSP(cfg config.NVRConfig, vendor string, cam int, sub bool) string {
	tmpl := strings.TrimSpace(cfg.RTSPURL)
	if tmpl == "" {
		port := cfg.RTSPPort
		if port == 0 {
			port = 554
		}
		return fmt.Sprintf("rtsp://%s@%s:%d%s", userinfo(cfg), cfg.Host, port, defaultRTSPPath(vendor, cam, sub))
	}
	return expandRTSPTemplate(cfg, tmpl, cam, sub)
}

func defaultRTSPPath(vendor string, cam int, sub bool) string {
	switch strings.ToLower(vendor) {
	case "hikvision":
		stream := 1
		if sub {
			stream = 2
		}
		return fmt.Sprintf("/Streaming/Channels/%d0%d", cam, stream)
	default:
		subtype := 0
		if sub {
			subtype = 1
		}
		return fmt.Sprintf("/cam/realmonitor?channel=%d&subtype=%d", cam, subtype)
	}
}

func expandRTSPTemplate(cfg config.NVRConfig, tmpl string, cam int, sub bool) string {
	subtype, stream := 0, 1
	if sub {
		subtype, stream = 1, 2
	}
	port := cfg.RTSPPort
	if port == 0 {
		port = 554
	}
	user, pass := encodedUserPass(cfg)

	out := strings.NewReplacer(
		"{userinfo}", userinfo(cfg),
		"{username}", user,
		"{password}", pass,
		"{user}", user,
		"{pass}", pass,
		"{host}", cfg.Host,
		"{port}", strconv.Itoa(port),
		"{channel_id}", strconv.Itoa(cam*100+stream),
		"{channel}", strconv.Itoa(cam),
		"{subtype}", strconv.Itoa(subtype),
		"{stream}", strconv.Itoa(stream),
	).Replace(tmpl)

	if !strings.Contains(out, "://") {
		if !strings.HasPrefix(out, "/") {
			out = "/" + out
		}
		out = fmt.Sprintf("rtsp://%s@%s:%d%s", userinfo(cfg), cfg.Host, port, out)
	}
	return out
}

func encodedUserPass(cfg config.NVRConfig) (user, pass string) {
	s := userinfo(cfg)
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}

func buildChannels(vendor string, nums []int, rtspURL func(cam int, sub bool) string) []Channel {
	out := make([]Channel, 0, len(nums))
	for _, n := range nums {
		out = append(out, Channel{
			ID:      fmt.Sprintf("ch%d", n),
			Name:    fmt.Sprintf("Camera %d", n),
			Vendor:  vendor,
			MainURL: rtspURL(n, false),
			SubURL:  rtspURL(n, true),
		})
	}
	return out
}

// parseCam turns an ISAPI channel id ("101", "1701") into a camera number.
// The last two digits are the stream index, everything before is the cam.
func parseCam(id string) int {
	if len(id) < 3 {
		return 0
	}
	n, _ := strconv.Atoi(id[:len(id)-2])
	return n
}

// isSub reports whether an ISAPI channel id is a sub-stream (ends in 2).
func isSub(id string) bool {
	return strings.HasSuffix(id, "2")
}

// scanIntAfter finds `key` in body and parses the integer immediately
// following it up to the next non-digit. Returns 0 if not found.
func scanIntAfter(body, key string) int {
	i := strings.Index(body, key)
	if i < 0 {
		return 0
	}
	rest := body[i+len(key):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}

// scanStringAfter finds `key` in body and returns the rest of that line.
func scanStringAfter(body, key string) string {
	i := strings.Index(body, key)
	if i < 0 {
		return ""
	}
	rest := body[i+len(key):]
	if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.TrimSpace(rest)
}
