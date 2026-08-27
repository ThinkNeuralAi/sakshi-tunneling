# Sakshi Edge Agent

A lightweight, outbound-only agent that runs at a store, logs into the
CCTV/NVR with the credentials the installer types in, pulls camera
sub-streams **locally on the LAN**, and relays **sampled JPEG frames** to
your cloud over a single WebSocket.

No static IP. No port forwarding. No inbound firewall change. The agent
dials out, so the store's NAT permits the return traffic. That is the same
trick Tailscale, Cloudflare Tunnel, and every camera-to-cloud product use,
and it is what makes this work behind India's carrier-grade NAT where
inbound port forwarding is impossible.

Unlike a transparent tunnel (WireGuard/Tailscale), the camera feeds never
leave the LAN and your cloud is never given a route into the store network.
Only decoded, down-sampled frames travel up. Smaller bandwidth bill,
smaller security blast radius.

## The four components

```
 discovery ──► nvr ──► puller ──► cloud
   ONVIF       login    ffmpeg     outbound
   LAN probe   ►channels ►JPEG     WebSocket
                          sample    frames + heartbeat
```

| Package | Job |
|---|---|
| `internal/discovery` | ONVIF WS-Discovery probe (UDP multicast) to find cameras on the LAN |
| `internal/nvr` | Log into Hikvision (ISAPI) or Dahua (CGI), enumerate channels, build local RTSP URLs. `auto` fingerprints the device |
| `internal/puller` | ffmpeg pulls one sub-stream over RTSP/TCP and emits sampled JPEGs; supervised with backoff |
| `internal/cloud` | One outbound WebSocket: enroll, heartbeat, binary frame push, inbound control commands |
| `internal/agent` | Wires it together, one supervised puller per channel, reconnect loop |
| `internal/config` | YAML config; persists the store id assigned at enrollment |

`cmd/agent` is the Windows-service entrypoint. `cmd/mock-cloud` is a
throwaway ingest server for local testing.

## Build

```bash
go build -o agent ./cmd/agent            # this platform
GOOS=windows GOARCH=amd64 go build -o agent.exe ./cmd/agent   # Windows
```

Single static binary. The only runtime dependency is **ffmpeg** on PATH.

## Run it locally (proven end-to-end)

```bash
# 1. throwaway cloud that writes received frames to ./frames/
go run ./cmd/mock-cloud

# 2. a fake camera (needs mediamtx + ffmpeg)
mediamtx &
ffmpeg -re -stream_loop -1 -f lavfi -i testsrc=size=640x480:rate=15 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -g 15 \
  -rtsp_transport tcp -f rtsp rtsp://127.0.0.1:8554/cam1 &

# 3. point config at ws://127.0.0.1:8080/agent and the RTSP source, then:
go run ./cmd/agent run --config ./config.yaml
```

Frames land in `./frames/` as JPEGs. This exact flow was validated:
enrollment, channel inventory, and 2 fps frame push all confirmed, with
valid 640×480 JPEGs written cloud-side.

## Install as a Windows service

```
agent.exe install
agent.exe start
agent.exe stop
agent.exe uninstall
```

Put `config.yaml` next to `agent.exe`. Ship it as a signed MSI for silent
install (`msiexec /i agent.msi /quiet`).

## Configuration

See `config.example.yaml`. The installer fills in four things: NVR host,
username, password, and a one-time `enrollment_token`. Everything else has
production defaults. `prefer_sub_stream: true` keeps bandwidth low.

## Deliberately left as next steps

This is a working skeleton, not a finished product. Before production:

- **mutual-TLS agent auth** — today enrollment trusts the token; add a
  per-store client cert so a leaked token can't impersonate a store.
- **encrypt NVR credentials at rest** — use Windows DPAPI, not plaintext YAML.
- **local buffering** — ring-buffer frames through network drops and
  replay on reconnect, so a flaky uplink loses nothing.
- **CGNAT relay fallback** — a TURN/relay for the minority of stores where
  even outbound WebRTC/P2P is throttled; plain WSS already covers most.
- **edge inference** — run a light detector here and push only crops/events
  to cut cloud GPU spend at scale.
- **self-update** — signed binary fetch-and-swap from the cloud.
- **more vendors** — Uniview, Matrix, Sparsh URL templates (the adapter
  interface is ready for them).
- **h.265 / audio** — currently video-only, JPEG sampling.
```
