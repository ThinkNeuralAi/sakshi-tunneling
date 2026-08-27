package nvr

import (
	"strings"
	"testing"

	"sakshi.example/edge-agent/internal/config"
)

func testCFG() config.NVRConfig {
	return config.NVRConfig{
		Host:     "10.0.0.5",
		RTSPPort: 554,
		Username: "admin",
		Password: "Infra@098",
	}
}

func TestBuildRTSPVendorDefaults(t *testing.T) {
	cfg := testCFG()

	dahuaMain := buildRTSP(cfg, "dahua", 26, false)
	if !strings.Contains(dahuaMain, "/cam/realmonitor?channel=26&subtype=0") {
		t.Fatalf("dahua main: %s", dahuaMain)
	}
	dahuaSub := buildRTSP(cfg, "dahua", 26, true)
	if !strings.Contains(dahuaSub, "channel=26&subtype=1") {
		t.Fatalf("dahua sub: %s", dahuaSub)
	}

	hik := buildRTSP(cfg, "hikvision", 26, false)
	if !strings.Contains(hik, "/Streaming/Channels/2601") {
		t.Fatalf("hikvision main: %s", hik)
	}
}

func TestBuildRTSPPathTemplate(t *testing.T) {
	cfg := testCFG()
	cfg.RTSPURL = "/cam/realmonitor?channel={channel}&subtype={subtype}"

	got := buildRTSP(cfg, "dahua", 3, true)
	wantSuffix := "/cam/realmonitor?channel=3&subtype=1"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("got %s, want suffix %s", got, wantSuffix)
	}
	if !strings.HasPrefix(got, "rtsp://") {
		t.Fatalf("path template should be prefixed with rtsp://, got %s", got)
	}
	if !strings.Contains(got, "Infra%40") {
		t.Fatalf("password should be URL-encoded, got %s", got)
	}
}

func TestBuildRTSPFullURLTemplate(t *testing.T) {
	cfg := testCFG()
	cfg.RTSPURL = "rtsp://{user}:{pass}@{host}:{port}/unicast/c{channel}/s{stream}/live"

	got := buildRTSP(cfg, "custom", 4, false)
	want := "rtsp://admin:Infra%40098@10.0.0.5:554/unicast/c4/s1/live"
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}

	cfg.RTSPURL = "rtsp://{user}:{pass}@{host}:{port}/Streaming/Channels/{channel_id}"
	got = buildRTSP(cfg, "hikvision", 1, true)
	if !strings.HasSuffix(got, "/Streaming/Channels/102") {
		t.Fatalf("channel_id sub: %s", got)
	}
}
