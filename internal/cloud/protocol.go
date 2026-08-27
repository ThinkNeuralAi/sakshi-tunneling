package cloud

import "time"

// The agent <-> cloud protocol runs over a single outbound WebSocket.
// Two message kinds share the socket:
//
//   - JSON text messages for control (enroll, heartbeat, commands)
//   - Binary messages for frames: a 4-byte big-endian header length,
//     then a JSON FrameHeader, then the raw JPEG bytes.
//
// Keeping frames binary avoids base64 bloat (~33% overhead) on the
// hot path, while control stays human-readable for easy debugging.

// MsgType tags a control message.
type MsgType string

const (
	MsgEnroll     MsgType = "enroll"      // agent -> cloud
	MsgEnrolled   MsgType = "enrolled"    // cloud -> agent
	MsgHeartbeat  MsgType = "heartbeat"   // agent -> cloud
	MsgChannels   MsgType = "channels"    // agent -> cloud (inventory)
	MsgCommand    MsgType = "command"     // cloud -> agent
	MsgError      MsgType = "error"       // either direction
)

// Control is the envelope for every JSON text message.
type Control struct {
	Type MsgType `json:"type"`

	// Enroll / Enrolled
	EnrollmentToken string `json:"enrollment_token,omitempty"`
	StoreID         string `json:"store_id,omitempty"`
	AgentVersion    string `json:"agent_version,omitempty"`

	// Heartbeat
	Heartbeat *Heartbeat `json:"heartbeat,omitempty"`

	// Channels inventory
	Channels []ChannelInfo `json:"channels,omitempty"`

	// Command (cloud -> agent): e.g. change fps, start/stop a channel
	Command *Command `json:"command,omitempty"`

	// Error
	Message string `json:"message,omitempty"`
}

// Heartbeat is the periodic health report from the store.
type Heartbeat struct {
	SentAt       time.Time `json:"sent_at"`
	UptimeSec    int64     `json:"uptime_sec"`
	ActiveChans  int       `json:"active_channels"`
	FramesPushed int64     `json:"frames_pushed"`
	FpsActual    float64   `json:"fps_actual"`
	AgentVersion string    `json:"agent_version"`
}

// ChannelInfo describes one camera channel discovered at the store.
type ChannelInfo struct {
	ID        string `json:"id"`         // stable per-store channel id, e.g. "ch1"
	Name      string `json:"name"`       // human label from the NVR if any
	Vendor    string `json:"vendor"`
	MainURL   string `json:"main_url"`   // never leaves the store; shown for ops only
	SubURL    string `json:"sub_url"`
	Online    bool   `json:"online"`
}

// Command is a control instruction from the cloud.
type Command struct {
	Action    string  `json:"action"`     // "set_fps" | "enable" | "disable"
	ChannelID string  `json:"channel_id"` // empty = all channels
	FPS       float64 `json:"fps,omitempty"`
}

// FrameHeader precedes the JPEG bytes in a binary frame message.
type FrameHeader struct {
	StoreID   string    `json:"store_id"`
	ChannelID string    `json:"channel_id"`
	CapturedAt time.Time `json:"captured_at"`
	Seq       int64     `json:"seq"`
	Width     int       `json:"width,omitempty"`
	Height    int       `json:"height,omitempty"`
}
