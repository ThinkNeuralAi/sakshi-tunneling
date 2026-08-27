// Command agent is the Sakshi edge agent. It runs as a Windows service
// (or a foreground process for dev), logs into the store NVR, pulls
// sub-streams locally, and pushes sampled frames to the cloud over one
// outbound WebSocket. No static IP, no port forwarding.
//
// Install as a Windows service:
//
//	agent.exe install     # register the service
//	agent.exe start       # start it
//	agent.exe stop
//	agent.exe uninstall
//
// Run in the foreground for development:
//
//	agent.exe run --config ./config.yaml
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/kardianos/service"

	"sakshi.example/edge-agent/internal/agent"
	"sakshi.example/edge-agent/internal/config"
	"sakshi.example/edge-agent/internal/daemon"
)

type program struct {
	cfgPath string
	cancel  context.CancelFunc
}

func (p *program) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.run(ctx)
	return nil
}

func (p *program) run(ctx context.Context) {
	if dir := filepath.Dir(p.cfgPath); dir != "" {
		_ = daemon.Write(dir, os.Getpid())
		defer daemon.Remove(dir)
	}
	cfg, err := config.Load(p.cfgPath)
	if err != nil {
		log.Printf("fatal: %v", err)
		return
	}
	a := agent.New(cfg, p.cfgPath)
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Printf("agent exited: %v", err)
	}
}

func (p *program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

func main() {
	cfgFlag := flag.String("config", defaultConfigPath(), "path to config.yaml")
	flag.Parse()

	prg := &program{cfgPath: *cfgFlag}

	svcConfig := &service.Config{
		Name:        "SakshiEdgeAgent",
		DisplayName: "Sakshi Edge Agent",
		Description: "Pulls store camera feeds locally and relays sampled frames to Sakshi cloud.",
		Arguments:   []string{"run", "--config", *cfgFlag},
	}

	s, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatal(err)
	}

	// Sub-command (install/start/stop/uninstall) vs. run.
	if arg := flag.Arg(0); arg != "" && arg != "run" {
		if err := service.Control(s, arg); err != nil {
			log.Fatalf("%s: %v", arg, err)
		}
		log.Printf("%s: ok", arg)
		return
	}

	if err := s.Run(); err != nil {
		log.Fatal(err)
	}
}

func defaultConfigPath() string {
	// Prefer a path next to the executable; fall back to CWD.
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "config.yaml")
	}
	return "config.yaml"
}
