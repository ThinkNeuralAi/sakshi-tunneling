//go:build windows

// Command start launches the edge agent as a detached background process.
// Closing this window does not stop the agent. Use stop.exe to kill it.
//
//	start.exe
//	start.exe --config C:\path\to\config.yaml
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"sakshi.example/edge-agent/internal/daemon"
)

func main() {
	cfgFlag := flag.String("config", "", "path to config.yaml (default: next to the exe)")
	flag.Parse()

	dir, err := exeDir()
	if err != nil {
		fatal(err)
	}

	if pid, err := daemon.Read(dir); err == nil && processAlive(pid) {
		fmt.Printf("agent already running (pid %d)\n", pid)
		return
	}

	agent, err := findAgent(dir)
	if err != nil {
		fatal(err)
	}
	cfg := *cfgFlag
	if cfg == "" {
		cfg = filepath.Join(dir, "config.yaml")
	}

	logPath := filepath.Join(dir, "agent.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fatal(err)
	}
	defer logFile.Close()

	cmd := exec.Command(agent, "run", "--config", cfg)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: uint32(syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess | createNoWindow),
	}
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	if err := daemon.Write(dir, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		fatal(err)
	}
	fmt.Printf("agent started in background (pid %d)\n", cmd.Process.Pid)
	fmt.Printf("logs: %s\n", logPath)
	fmt.Println("close this window; the agent keeps running. Use stop.exe to stop it.")
}

const (
	detachedProcess = 0x00000008
	createNoWindow  = 0x08000000
)

func findAgent(dir string) (string, error) {
	for _, name := range []string{"agent-windows-amd64.exe", "agent.exe"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("agent-windows-amd64.exe not found in %s", dir)
}

func exeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func processAlive(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
