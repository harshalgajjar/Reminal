package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Active is the on-disk record of a currently running agent. The file lives
// at ~/.reminal/active.json with mode 0600 so a casual `cat` can't leak the
// PIN, and is removed by the agent on graceful shutdown. `reminal info` reads
// it so a user who cleared their terminal can recover the join details.
type Active struct {
	ID        string    `json:"id"`
	PIN       string    `json:"pin"`
	OpenURL   string    `json:"open_url"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

func activePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reminal", "active.json"), nil
}

// WriteActive records the running session so `reminal info` can find it.
func WriteActive(a Active) error {
	p, err := activePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// ClearActive deletes the active-session file. Idempotent: missing file is
// not an error since the agent may be cleaning up after never having written.
func ClearActive() error {
	p, err := activePath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ReadActive returns the recorded session if its PID is still alive. A stale
// file (PID no longer present — e.g., a crashed agent that couldn't run its
// defers) is removed and (nil, os.ErrNotExist) is returned.
func ReadActive() (*Active, error) {
	p, err := activePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var a Active
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	if !pidAlive(a.PID) {
		_ = ClearActive()
		return nil, os.ErrNotExist
	}
	return &a, nil
}

// pidAlive returns true if a process with this PID exists and is reachable
// (signal 0 is "permission and existence check, no signal sent"). On Unix
// this is the standard way to check liveness without side effects.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
