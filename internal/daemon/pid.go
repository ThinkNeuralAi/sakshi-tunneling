// Package daemon keeps a pid file so start.exe / stop.exe can manage
// a detached agent process.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const pidFileName = "agent.pid"

// Path is <dir>/agent.pid. dir should be the folder that holds the exes.
func Path(dir string) string {
	return filepath.Join(dir, pidFileName)
}

func Write(dir string, pid int) error {
	return os.WriteFile(Path(dir), []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

func Read(dir string) (int, error) {
	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid pid file")
	}
	return n, nil
}

func Remove(dir string) {
	_ = os.Remove(Path(dir))
}
