// Command agent is the Sakshi edge agent. It logs into the store NVR,
// pulls sub-streams locally, and pushes sampled frames to the cloud over
// one outbound WebSocket. No static IP, no port forwarding.
//
// Foreground (dev):
//
//	agent.exe
//	agent.exe --config ./config.yaml
//
// Background on Windows: use start.exe / stop.exe (they live next to this
// binary). Those launch this process detached; this binary must not import
// kardianos/service — that package panics in a CREATE_NO_WINDOW process.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"sakshi.example/edge-agent/internal/agent"
	"sakshi.example/edge-agent/internal/config"
	"sakshi.example/edge-agent/internal/daemon"
)

func main() {
	cfgPath, cmd := parseArgs(os.Args[1:])
	switch cmd {
	case "", "run", "start":
		if err := runAgent(cfgPath); err != nil {
			log.Fatal(err)
		}
	case "install", "uninstall":
		log.Fatalf("%s: use start.exe / stop.exe next to this binary to run the agent in the background", cmd)
	case "stop":
		log.Fatalf("stop: use stop.exe next to this binary")
	default:
		log.Fatalf("unknown command %q (want run)", cmd)
	}
}

func runAgent(cfgPath string) error {
	if dir := filepath.Dir(cfgPath); dir != "" {
		_ = daemon.Write(dir, os.Getpid())
		defer daemon.Remove(dir)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("fatal: %w", err)
	}
	log.Printf("starting agent store_id=%s config=%s", cfg.StoreID, cfgPath)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	a := agent.New(cfg, cfgPath)
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// parseArgs accepts both `agent --config x run` and `agent run --config x`
// (start.exe historically passed the subcommand first, which made Go's
// flag parser ignore --config).
func parseArgs(args []string) (cfgPath, cmd string) {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgFlag := fs.String("config", defaultConfigPath(), "path to config.yaml")

	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positional = append(positional, a)
	}
	if err := fs.Parse(flags); err != nil {
		os.Exit(2)
	}
	if len(positional) > 0 {
		cmd = positional[0]
	}
	return *cfgFlag, cmd
}

func defaultConfigPath() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "config.yaml")
	}
	return "config.yaml"
}
