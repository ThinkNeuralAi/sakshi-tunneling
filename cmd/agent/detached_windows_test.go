//go:build windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDetachedLaunchDoesNotPanic(t *testing.T) {
	exe := filepath.Join("..", "..", "bin", "agent-windows-amd64.exe")
	if _, err := os.Stat(exe); err != nil {
		t.Skip("bin/agent-windows-amd64.exe not built")
	}
	logPath := filepath.Join(t.TempDir(), "out.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(exe, "--config", filepath.Join(t.TempDir(), "missing.yaml"))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000008 | 0x08000000 | 0x00000200, // DETACHED | CREATE_NO_WINDOW | NEW_GROUP
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	logFile.Close()

	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("panic: The parameter is incorrect")) {
		t.Fatalf("detached process still panics:\n%s", body)
	}
	if !bytes.Contains(body, []byte("read config")) && !bytes.Contains(body, []byte("fatal")) {
		t.Fatalf("expected a config-load error, got:\n%s", body)
	}
}
