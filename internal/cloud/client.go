package cloud

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client is the agent's single outbound connection to the cloud. It dials
// OUT (wss://), which is the whole NAT-traversal trick: the store's
// firewall permits return traffic on a connection the agent initiated, so
// there's no static IP, no port forward, no inbound rule. The camera feeds
// never leave the LAN; only sampled frames and telemetry travel this pipe.
type Client struct {
	url          string
	agentVersion string

	mu   sync.Mutex // serialises writes; gorilla conns aren't write-safe
	conn *websocket.Conn

	// CommandHandler is invoked for each command the cloud pushes down.
	CommandHandler func(Command)

	storeID string
}

// NewClient constructs a client (does not connect yet).
func NewClient(url, agentVersion string) *Client {
	return &Client{url: url, agentVersion: agentVersion}
}

// Connect dials the cloud and performs enrollment, trading the one-time
// token for a store id (or confirming an existing one).
func (c *Client) Connect(ctx context.Context, enrollmentToken, storeID string) (string, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return "", fmt.Errorf("dial cloud: %w", err)
	}
	c.conn = conn

	if err := c.writeControl(Control{
		Type:            MsgEnroll,
		EnrollmentToken: enrollmentToken,
		StoreID:         storeID,
		AgentVersion:    c.agentVersion,
	}); err != nil {
		conn.Close()
		return "", err
	}

	// Expect an enrolled reply.
	_, data, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("await enrollment: %w", err)
	}
	var reply Control
	if err := json.Unmarshal(data, &reply); err != nil {
		conn.Close()
		return "", fmt.Errorf("decode enrollment reply: %w", err)
	}
	if reply.Type == MsgError {
		conn.Close()
		return "", fmt.Errorf("cloud rejected enrollment: %s", reply.Message)
	}
	if reply.Type != MsgEnrolled || reply.StoreID == "" {
		conn.Close()
		return "", fmt.Errorf("unexpected enrollment reply %q", reply.Type)
	}
	c.storeID = reply.StoreID
	return reply.StoreID, nil
}

// ReadLoop dispatches inbound control messages until the connection drops.
// Run it in its own goroutine. Frame pushes and heartbeats are writes and
// don't go through here.
func (c *Client) ReadLoop(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return err
		}
		var msg Control
		if err := json.Unmarshal(data, &msg); err != nil {
			continue // ignore malformed control
		}
		if msg.Type == MsgCommand && msg.Command != nil && c.CommandHandler != nil {
			c.CommandHandler(*msg.Command)
		}
	}
}

// SendHeartbeat reports health to the cloud.
func (c *Client) SendHeartbeat(hb Heartbeat) error {
	hb.AgentVersion = c.agentVersion
	return c.writeControl(Control{Type: MsgHeartbeat, Heartbeat: &hb})
}

// SendChannels reports the discovered inventory.
func (c *Client) SendChannels(chans []ChannelInfo) error {
	return c.writeControl(Control{Type: MsgChannels, Channels: chans})
}

// PushFrame sends one sampled JPEG as a binary message:
//
//	[4 bytes big-endian header length][JSON FrameHeader][JPEG bytes]
func (c *Client) PushFrame(channelID string, jpeg []byte, capturedAt time.Time, seq int64) error {
	hdr := FrameHeader{
		StoreID:    c.storeID,
		ChannelID:  channelID,
		CapturedAt: capturedAt,
		Seq:        seq,
	}
	hdrBytes, err := json.Marshal(hdr)
	if err != nil {
		return err
	}
	msg := make([]byte, 4+len(hdrBytes)+len(jpeg))
	binary.BigEndian.PutUint32(msg[:4], uint32(len(hdrBytes)))
	copy(msg[4:], hdrBytes)
	copy(msg[4+len(hdrBytes):], jpeg)

	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteMessage(websocket.BinaryMessage, msg)
}

func (c *Client) writeControl(m Control) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// Close tears down the connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
