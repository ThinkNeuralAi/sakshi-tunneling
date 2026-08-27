// Package nvr turns an NVR login into a list of camera channels and
// their local RTSP URLs. This is the piece that makes the install UX
// clean: the shop assistant types one IP + one login, and the agent
// enumerates every camera behind the recorder.
//
// Two adapters are implemented as first-class citizens because they
// (and their OEM cousins) dominate India/Gulf retail:
//
//   - Hikvision  -> ISAPI (also covers most CP Plus "Orange"/HikOEM)
//   - Dahua      -> HTTP CGI  (also covers CP Plus "Dahua" OEM, many others)
//
// A third path, ONVIF, lives in the discovery package and is used as a
// vendor-agnostic fallback.
package nvr

import (
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/icholy/digest"

	"sakshi.example/edge-agent/internal/cloud"
	"sakshi.example/edge-agent/internal/config"
)

// Channel is a discovered camera plus its local stream URLs.
type Channel struct {
	ID      string
	Name    string
	Vendor  string
	MainURL string
	SubURL  string
}

// ToInfo converts to the wire type (without leaking creds beyond the LAN
// boundary; the URLs are reported for ops visibility only and the cloud
// never dials them).
func (c Channel) ToInfo(online bool) cloud.ChannelInfo {
	return cloud.ChannelInfo{
		ID: c.ID, Name: c.Name, Vendor: c.Vendor,
		MainURL: c.MainURL, SubURL: c.SubURL, Online: online,
	}
}

// Adapter is the contract every vendor implementation satisfies.
type Adapter interface {
	Vendor() string
	Enumerate(ctx context.Context) ([]Channel, error)
}

// New returns the right adapter for the configured vendor, fingerprinting
// the device when vendor == "auto".
func New(cfg config.NVRConfig) (Adapter, error) {
	v := strings.ToLower(cfg.Vendor)
	if v == "" || v == "auto" {
		if len(cfg.Channels) > 0 || cfg.ChannelCount > 0 {
			return nil, fmt.Errorf("set nvr.vendor to dahua, hikvision, or custom when using channels (HTTP fingerprint is skipped)")
		}
		detected := fingerprint(cfg)
		v = detected
	}
	switch v {
	case "hikvision":
		return &hikvision{cfg: cfg, http: digestClient(cfg)}, nil
	case "dahua":
		return &dahua{cfg: cfg, http: digestClient(cfg)}, nil
	case "custom":
		if strings.TrimSpace(cfg.RTSPURL) == "" {
			return nil, fmt.Errorf("nvr.rtsp_url is required when vendor is custom")
		}
		if len(cfg.Channels) == 0 && cfg.ChannelCount == 0 {
			return nil, fmt.Errorf("nvr.channels or nvr.channel_count is required when vendor is custom")
		}
		return &customNVR{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unsupported vendor %q (try hikvision, dahua, or custom)", v)
	}
}

func digestClient(cfg config.NVRConfig) *http.Client {
	return &http.Client{
		Timeout: 8 * time.Second,
		Transport: &digest.Transport{
			Username: cfg.Username,
			Password: cfg.Password,
		},
	}
}

// fingerprint makes a cheap unauthenticated request and sniffs the
// response to guess the vendor. Falls back to hikvision, the most common.
func fingerprint(cfg config.NVRConfig) string {
	base := fmt.Sprintf("http://%s:%d", cfg.Host, cfg.HTTPPort)
	c := &http.Client{Timeout: 4 * time.Second}

	// Hikvision devices answer ISAPI even pre-auth with a 401 + WWW-Auth
	// realm that mentions the product; Dahua exposes /cgi-bin.
	if resp, err := c.Get(base + "/ISAPI/System/deviceInfo"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK {
			if strings.Contains(strings.ToLower(resp.Header.Get("WWW-Authenticate")), "hikvision") ||
				resp.StatusCode == http.StatusOK {
				return "hikvision"
			}
		}
	}
	if resp, err := c.Get(base + "/cgi-bin/magicBox.cgi?action=getDeviceType"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK {
			return "dahua"
		}
	}
	return "hikvision"
}

// ---------------------------------------------------------------------
// Hikvision (ISAPI)
// ---------------------------------------------------------------------

type hikvision struct {
	cfg  config.NVRConfig
	http *http.Client
}

func (h *hikvision) Vendor() string { return "hikvision" }

// isapiChannelList models the subset of the ISAPI streaming channels
// response we care about.
type isapiChannelList struct {
	XMLName  xml.Name `xml:"StreamingChannelList"`
	Channels []struct {
		ID      string `xml:"id"`
		Name    string `xml:"channelName"`
		Enabled bool   `xml:"enabled"`
	} `xml:"StreamingChannel"`
}

func (h *hikvision) Enumerate(ctx context.Context) ([]Channel, error) {
	if nums := configuredChannels(h.cfg); len(nums) > 0 {
		return buildChannels("hikvision", nums, h.rtspURL), nil
	}

	base := fmt.Sprintf("http://%s:%d", h.cfg.Host, h.cfg.HTTPPort)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/ISAPI/Streaming/channels", nil)

	resp, err := h.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("isapi streaming channels: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("isapi returned %d (check credentials / enable ISAPI)", resp.StatusCode)
	}

	var list isapiChannelList
	if err := xml.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode isapi channel list: %w", err)
	}

	// ISAPI ids are like 101 (cam1 main), 102 (cam1 sub), 201, 202...
	// We fold the two streams of one camera into a single Channel.
	seen := map[int]*Channel{}
	order := []int{}
	for _, ch := range list.Channels {
		if len(ch.ID) < 3 {
			continue
		}
		camNo := parseCam(ch.ID) // 101 -> 1, 1701 -> 17
		if _, ok := seen[camNo]; !ok {
			seen[camNo] = &Channel{
				ID:     fmt.Sprintf("ch%d", camNo),
				Name:   strings.TrimSpace(ch.Name),
				Vendor: "hikvision",
			}
			order = append(order, camNo)
		}
		c := seen[camNo]
		url := h.rtspURL(camNo, isSub(ch.ID))
		if isSub(ch.ID) {
			c.SubURL = url
		} else {
			c.MainURL = url
		}
	}

	out := make([]Channel, 0, len(order))
	for _, n := range order {
		c := seen[n]
		if c.SubURL == "" { // some devices only report main in the list
			c.SubURL = h.rtspURL(n, true)
		}
		out = append(out, *c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no channels found on hikvision device")
	}
	return out, nil
}

// rtspURL builds a Hikvision RTSP URL. Stream 1 = main, 2 = sub.
// Channel encoding: cam N main = N*100+1, sub = N*100+2.
func (h *hikvision) rtspURL(cam int, sub bool) string {
	return buildRTSP(h.cfg, "hikvision", cam, sub)
}

// ---------------------------------------------------------------------
// Dahua (HTTP CGI)
// ---------------------------------------------------------------------

type dahua struct {
	cfg  config.NVRConfig
	http *http.Client
}

func (d *dahua) Vendor() string { return "dahua" }

// Enumerate asks the recorder how many video input channels it has, then
// builds URLs. Dahua's getConfig returns "table.ChannelTitle[0].Name=..."
// style key/value lines.
func (d *dahua) Enumerate(ctx context.Context) ([]Channel, error) {
	if nums := configuredChannels(d.cfg); len(nums) > 0 {
		log.Printf("dahua: skipping HTTP, using configured channels %v on rtsp :%d", nums, d.cfg.RTSPPort)
		return buildChannels("dahua", nums, d.rtspURL), nil
	}

	base := fmt.Sprintf("http://%s:%d", d.cfg.Host, d.cfg.HTTPPort)

	max := d.channelCount(ctx, base)
	if max == 0 {
		return nil, fmt.Errorf("could not determine dahua channel count (check credentials)")
	}
	names := d.channelTitles(ctx, base, max)

	out := make([]Channel, 0, max)
	for i := 0; i < max; i++ {
		out = append(out, Channel{
			ID:      fmt.Sprintf("ch%d", i+1),
			Name:    names[i],
			Vendor:  "dahua",
			MainURL: d.rtspURL(i+1, false),
			SubURL:  d.rtspURL(i+1, true),
		})
	}
	return out, nil
}

// channelCount reads the max channel count via magicBox.
func (d *dahua) channelCount(ctx context.Context, base string) int {
	body := d.get(ctx, base+"/cgi-bin/magicBox.cgi?action=getProductDefinition")
	// Look for MaxRemoteInputChannels or VideoInChannels in the reply.
	for _, key := range []string{"MaxRemoteInputChannels", "VideoInChannels", "MaxExtraStream"} {
		if v := scanIntAfter(body, key+"="); v > 0 {
			return v
		}
	}
	// Fall back to a sane default for a small-store NVR.
	if body == "" {
		return 0
	}
	return 8
}

func (d *dahua) channelTitles(ctx context.Context, base string, max int) []string {
	names := make([]string, max)
	body := d.get(ctx, base+"/cgi-bin/configManager.cgi?action=getConfig&name=ChannelTitle")
	for i := 0; i < max; i++ {
		key := fmt.Sprintf("ChannelTitle[%d].Name=", i)
		names[i] = scanStringAfter(body, key)
	}
	return names
}

// rtspURL builds a Dahua RTSP URL. subtype 0 = main, 1 = sub.
func (d *dahua) rtspURL(cam int, sub bool) string {
	return buildRTSP(d.cfg, "dahua", cam, sub)
}

// ---------------------------------------------------------------------
// Custom (config-driven URL template, no HTTP enumeration)
// ---------------------------------------------------------------------

type customNVR struct {
	cfg config.NVRConfig
}

func (c *customNVR) Vendor() string { return "custom" }

func (c *customNVR) Enumerate(_ context.Context) ([]Channel, error) {
	nums := configuredChannels(c.cfg)
	if len(nums) == 0 {
		return nil, fmt.Errorf("no channels configured")
	}
	log.Printf("custom: using rtsp_url template for channels %v on :%d", nums, c.cfg.RTSPPort)
	return buildChannels("custom", nums, func(cam int, sub bool) string {
		return buildRTSP(c.cfg, "custom", cam, sub)
	}), nil
}

func (d *dahua) get(ctx context.Context, url string) string {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := d.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	buf := make([]byte, 64*1024)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}
