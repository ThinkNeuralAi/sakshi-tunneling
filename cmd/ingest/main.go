// Command ingest is the cloud half of the Sakshi edge agent.
//
// It accepts the agent's outbound WebSocket, enrolls the store, and
// republishes each camera as a local RTSP URL via ffmpeg + MediaMTX so
// an existing CV app can keep using VideoCapture / DeepStream:
//
//	wss://ingest.example.com/agent   ← agent pushes JPEGs here
//	rtsp://127.0.0.1:8554/{store}/{ch}  ← CV reads this
//
//	./ingest
//	INGEST_ADDR=:8080 MEDIAMTX_RTSP=rtsp://127.0.0.1:8554 ./ingest
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"sakshi.example/edge-agent/internal/cloud"
	"sakshi.example/edge-agent/internal/restream"
)

const (
	pingPeriod = 20 * time.Second
	pongWait   = 60 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

func main() {
	addr := env("INGEST_ADDR", ":8080")
	mtx := strings.TrimRight(env("MEDIAMTX_RTSP", "rtsp://127.0.0.1:8554"), "/")
	public := strings.TrimRight(env("PUBLIC_RTSP_BASE", mtx), "/")
	fps := 15
	if v := env("RESTREAM_FPS", "15"); v != "" {
		fmt.Sscanf(v, "%d", &fps)
	}
	tokens := splitTokens(env("ENROLLMENT_TOKENS", ""))
	enableRestream := true
	switch strings.ToLower(strings.TrimSpace(env("ENABLE_RESTREAM", "1"))) {
	case "0", "false", "no", "off":
		enableRestream = false
	}

	hub := restream.NewHub(mtx, fps, enableRestream)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/streams", func(w http.ResponseWriter, _ *http.Request) {
		type row struct {
			restream.Info
			PlayURL string `json:"play_url"`
		}
		list := hub.List()
		out := make([]row, 0, len(list))
		for _, s := range list {
			play := s.RTSPURL
			if public != mtx {
				play = strings.Replace(s.RTSPURL, mtx, public, 1)
			}
			out = append(out, row{Info: s, PlayURL: play})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"streams": out})
	})
	mux.HandleFunc("/latest/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/latest/")
		rest = strings.TrimSuffix(rest, ".jpg")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		jpeg, ok := hub.LatestJPEG(parts[0], parts[1])
		if !ok {
			http.Error(w, "no frame yet", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(jpeg)
	})
	mux.HandleFunc("/agent", func(w http.ResponseWriter, r *http.Request) {
		handleAgent(w, r, hub, tokens)
	})

	log.Printf("ingest listening on %s", addr)
	log.Printf("  WS   %s/agent", addr)
	if enableRestream {
		log.Printf("  RTSP restream ON → %s/{store}/{channel}", mtx)
	} else {
		log.Printf("  RTSP restream OFF (ENABLE_RESTREAM=0); use /latest/{store}/{ch}.jpg")
	}
	if len(tokens) == 0 {
		log.Printf("WARNING: ENROLLMENT_TOKENS is empty; any agent token is accepted")
	}
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handleAgent(w http.ResponseWriter, r *http.Request, hub *restream.Hub, tokens map[string]struct{}) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}
	defer conn.Close()

	conn.SetReadLimit(8 << 20) // 8 MiB; one JPEG + header
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	var writeMu sync.Mutex
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(pingPeriod)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-t.C:
				writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				writeMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	storeID := ""
	log.Printf("agent connected from %s", r.RemoteAddr)

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("agent %s disconnected: %v", storeID, err)
			return
		}

		if mt == websocket.BinaryMessage {
			store, ch, seq, jpeg, err := parseFrame(data)
			if err != nil {
				continue
			}
			if storeID != "" {
				store = storeID
			}
			jpegCopy := make([]byte, len(jpeg))
			copy(jpegCopy, jpeg)
			hub.Push(store, ch, seq, jpegCopy)
			continue
		}

		var msg cloud.Control
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case cloud.MsgEnroll:
			id, err := enroll(msg, tokens)
			if err != nil {
				writeMu.Lock()
				_ = writeJSON(conn, cloud.Control{Type: cloud.MsgError, Message: err.Error()})
				writeMu.Unlock()
				return
			}
			storeID = id
			writeMu.Lock()
			err = writeJSON(conn, cloud.Control{Type: cloud.MsgEnrolled, StoreID: storeID})
			writeMu.Unlock()
			if err != nil {
				return
			}
			log.Printf("enrolled %s (agent v%s)", storeID, msg.AgentVersion)

		case cloud.MsgChannels:
			log.Printf("%s inventory: %d channels", storeID, len(msg.Channels))
			for _, c := range msg.Channels {
				log.Printf("  %s %q vendor=%s", c.ID, c.Name, c.Vendor)
			}

		case cloud.MsgHeartbeat:
			if hb := msg.Heartbeat; hb != nil {
				log.Printf("%s heartbeat: up=%ds chans=%d frames=%d fps=%.2f",
					storeID, hb.UptimeSec, hb.ActiveChans, hb.FramesPushed, hb.FpsActual)
			}
		}
	}
}

func enroll(msg cloud.Control, tokens map[string]struct{}) (string, error) {
	if len(tokens) > 0 {
		if _, ok := tokens[msg.EnrollmentToken]; !ok {
			return "", fmt.Errorf("invalid enrollment token")
		}
	}
	if msg.StoreID != "" {
		return msg.StoreID, nil
	}
	return fmt.Sprintf("store-%d", time.Now().Unix()%100000), nil
}

func parseFrame(data []byte) (store, channel string, seq int64, jpeg []byte, err error) {
	if len(data) < 4 {
		return "", "", 0, nil, fmt.Errorf("short")
	}
	hlen := binary.BigEndian.Uint32(data[:4])
	if int(hlen)+4 > len(data) {
		return "", "", 0, nil, fmt.Errorf("bad header length")
	}
	var hdr cloud.FrameHeader
	if err := json.Unmarshal(data[4:4+hlen], &hdr); err != nil {
		return "", "", 0, nil, err
	}
	return hdr.StoreID, hdr.ChannelID, hdr.Seq, data[4+hlen:], nil
}

func writeJSON(conn *websocket.Conn, m cloud.Control) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteMessage(websocket.TextMessage, b)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitTokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			out[t] = struct{}{}
		}
	}
	return out
}
