//go:build windows

// Command stop kills the background edge agent started by start.exe.
//
//	stop.exe
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"sakshi.example/edge-agent/internal/daemon"
)

func main() {
	dir, err := exeDir()
	if err != nil {
		fatal(err)
	}

	pid, err := daemon.Read(dir)
	if err != nil {
		// Fallback: kill by image name if the pid file is missing.
		if killByName() {
			fmt.Println("agent stopped")
			return
		}
		fmt.Println("agent is not running")
		return
	}

	if !processAlive(pid) {
		daemon.Remove(dir)
		if killByName() {
			fmt.Println("agent stopped")
			return
		}
		fmt.Println("agent is not running")
		return
	}

	if err := killPID(pid); err != nil {
		fatal(err)
	}
	daemon.Remove(dir)
	fmt.Printf("agent stopped (pid %d)\n", pid)
}

func killPID(pid int) error {
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "not found") {
		return fmt.Errorf("taskkill: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func killByName() bool {
	killed := false
	for _, name := range []string{"agent-windows-amd64.exe", "agent.exe"} {
		cmd := exec.Command("taskkill", "/IM", name, "/F")
		if err := cmd.Run(); err == nil {
			killed = true
		}
	}
	return killed
}

func processAlive(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

func exeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
