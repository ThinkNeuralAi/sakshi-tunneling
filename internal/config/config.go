// Package config loads and persists the edge agent's configuration.
//
// On a real deployment the config file lives next to the binary
// (e.g. C:\ProgramData\SakshiEdge\config.yaml on Windows). The only
// thing a non-technical installer must supply is the NVR IP, the NVR
// login, and a one-time enrollment token that binds this agent to a
// store in the cloud. Everything else has sane defaults.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the on-disk configuration for one store agent.
type Config struct {
	// StoreID is assigned by the cloud at enrollment. Empty until enrolled.
	StoreID string `yaml:"store_id"`

	// EnrollmentToken is a one-time secret the installer pastes in. The
	// agent trades it for a per-store credential on first contact.
	EnrollmentToken string `yaml:"enrollment_token"`

	// CloudURL is the wss:// endpoint of your ingest service.
	CloudURL string `yaml:"cloud_url"`

	// NVR describes how to reach the recorder on the local LAN.
	NVR NVRConfig `yaml:"nvr"`

	// Sampling controls how many frames per second we lift per channel.
	// Sub-second values are allowed (0.5 = one frame every 2s).
	SampleFPS float64 `yaml:"sample_fps"`

	// HeartbeatInterval is how often we report health to the cloud.
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`

	// PreferSubStream pulls the low-bitrate sub-stream instead of the
	// main stream. Almost always what you want for inference.
	PreferSubStream bool `yaml:"prefer_sub_stream"`

	// DiscoveryEnabled runs ONVIF WS-Discovery on the LAN at startup in
	// addition to enumerating channels off the NVR login.
	DiscoveryEnabled bool `yaml:"discovery_enabled"`
}

// NVRConfig holds the recorder connection details.
type NVRConfig struct {
	// Vendor is one of: hikvision, dahua, custom, auto.
	// "auto" fingerprints the device before enumerating.
	Vendor   string `yaml:"vendor"`
	Host     string `yaml:"host"`
	HTTPPort int    `yaml:"http_port"`
	RTSPPort int    `yaml:"rtsp_port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`

	// Channels is an explicit camera list (e.g. [26] or [1, 2, 26]).
	// When set, the agent skips HTTP/ISAPI enumeration and builds RTSP
	// URLs from the vendor template. Use this when port 80 is closed
	// but RTSP :554 is reachable.
	Channels []int `yaml:"channels,omitempty"`

	// ChannelCount, if set and Channels is empty, builds cameras 1..N
	// the same way (no HTTP login required).
	ChannelCount int `yaml:"channel_count,omitempty"`

	// RTSPURL is an optional per-branch stream URL pattern. Different
	// OEM recorders (and even two Dahua NVRs) expose different paths.
	//
	// Path-only (host/user/pass/port still come from the fields above):
	//   /cam/realmonitor?channel={channel}&subtype={subtype}
	// Full URL:
	//   rtsp://{user}:{pass}@{host}:{port}/Streaming/Channels/{channel_id}
	//
	// Placeholders: {user} {pass} {host} {port} {channel} {subtype}
	// {stream} {channel_id} {userinfo}
	// If empty, the vendor default path is used.
	RTSPURL string `yaml:"rtsp_url,omitempty"`
}

// Default returns a config with production-sane defaults filled in.
func Default() Config {
	return Config{
		CloudURL:          "wss://ingest.sakshi.example/agent",
		SampleFPS:         1,
		HeartbeatInterval: 30 * time.Second,
		PreferSubStream:   true,
		DiscoveryEnabled:  true,
		NVR: NVRConfig{
			Vendor:   "auto",
			HTTPPort: 80,
			RTSPPort: 554,
		},
	}
}

// Load reads config from path, applying defaults for anything unset.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	if cfg.NVR.Host == "" {
		return cfg, fmt.Errorf("nvr.host is required")
	}
	return cfg, nil
}

// Save writes config back to disk (used after enrollment to persist
// the store_id the cloud handed us).
func Save(path string, cfg Config) error {
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}
