// Command mock-cloud is a throwaway ingest server for local testing. It
// accepts the agent's WebSocket, completes enrollment, prints heartbeats
// and channel inventory, and writes received JPEG frames to ./frames/ so
// you can eyeball that the whole pipe works before any real cloud exists.
//
//	go run ./cmd/mock-cloud            # listens on :8080
//	# then point the agent's cloud_url at ws://127.0.0.1:8080/agent
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"

	"sakshi.example/edge-agent/internal/cloud"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

func main() {
	_ = os.MkdirAll("frames", 0o755)
	http.HandleFunc("/agent", handleAgent)
	log.Println("mock cloud listening on :8080 (ws://127.0.0.1:8080/agent)")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleAgent(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}
	defer conn.Close()
	log.Printf("agent connected from %s", r.RemoteAddr)

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			log.Println("agent disconnected:", err)
			return
		}
		if mt == websocket.BinaryMessage {
			handleFrame(data)
			continue
		}

		var msg cloud.Control
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case cloud.MsgEnroll:
			// Accept any token; assign a stable-ish store id.
			storeID := msg.StoreID
			if storeID == "" {
				storeID = "store-" + fmt.Sprint(time.Now().Unix()%100000)
			}
			reply, _ := json.Marshal(cloud.Control{Type: cloud.MsgEnrolled, StoreID: storeID})
			conn.WriteMessage(websocket.TextMessage, reply)
			log.Printf("enrolled %s (token=%q, agent v%s)", storeID, msg.EnrollmentToken, msg.AgentVersion)
		case cloud.MsgChannels:
			log.Printf("inventory: %d channels", len(msg.Channels))
			for _, c := range msg.Channels {
				log.Printf("  %s %-10s vendor=%s online=%v", c.ID, c.Name, c.Vendor, c.Online)
			}
		case cloud.MsgHeartbeat:
			if hb := msg.Heartbeat; hb != nil {
				log.Printf("heartbeat: up=%ds chans=%d frames=%d fps=%.2f",
					hb.UptimeSec, hb.ActiveChans, hb.FramesPushed, hb.FpsActual)
			}
		}
	}
}

func handleFrame(data []byte) {
	if len(data) < 4 {
		return
	}
	hlen := binary.BigEndian.Uint32(data[:4])
	if int(hlen)+4 > len(data) {
		return
	}
	var hdr cloud.FrameHeader
	if err := json.Unmarshal(data[4:4+hlen], &hdr); err != nil {
		return
	}
	jpeg := data[4+hlen:]
	name := filepath.Join("frames", fmt.Sprintf("%s_%s_%d.jpg", hdr.StoreID, hdr.ChannelID, hdr.Seq))
	if err := os.WriteFile(name, jpeg, 0o644); err == nil {
		log.Printf("frame %s ch=%s seq=%d (%d bytes)", hdr.StoreID, hdr.ChannelID, hdr.Seq, len(jpeg))
	}
}
