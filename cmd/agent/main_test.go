package main

import "testing"

func TestParseArgsConfigAfterRun(t *testing.T) {
	cfg, cmd := parseArgs([]string{"run", "--config", `C:\store\config.yaml`})
	if cmd != "run" {
		t.Fatalf("cmd=%q", cmd)
	}
	if cfg != `C:\store\config.yaml` {
		t.Fatalf("cfg=%q", cfg)
	}
}

func TestParseArgsConfigBeforeRun(t *testing.T) {
	cfg, cmd := parseArgs([]string{"--config", `C:\store\config.yaml`, "run"})
	if cmd != "run" {
		t.Fatalf("cmd=%q", cmd)
	}
	if cfg != `C:\store\config.yaml` {
		t.Fatalf("cfg=%q", cfg)
	}
}

func TestParseArgsConfigOnly(t *testing.T) {
	cfg, cmd := parseArgs([]string{"--config=C:\\store\\config.yaml"})
	if cmd != "" {
		t.Fatalf("cmd=%q", cmd)
	}
	if cfg != `C:\store\config.yaml` {
		t.Fatalf("cfg=%q", cfg)
	}
}
