package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Active is the on-disk record of a currently running agent. Each agent
// writes to ~/.reminal/active-<id>.json with mode 0600 so a casual `cat`
// can't leak the PIN, and removes its file on graceful shutdown.
// `reminal info` reads it so a user who cleared their terminal can
// recover the join details.
//
// Multiple agents can run on the same host concurrently — each gets its
// own active-<id>.json so `reminal list` can enumerate them. Kind tells
// shell agents and port-forwarders apart.
type Active struct {
	ID        string    `json:"id"`
	PIN       string    `json:"pin"`
	OpenURL   string    `json:"open_url"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	// Kind distinguishes shell sessions ("" or "shell") from port
	// forwards ("port"). Empty means shell for back-compat with
	// records written before port-forwarding existed.
	Kind string `json:"kind,omitempty"`
	// Port is the local TCP port being forwarded. Set only when
	// Kind == "port"; zero otherwise.
	Port int `json:"port,omitempty"`
	// Headless is true when the agent was spawned with --headless (no
	// host terminal attached). Surfaced by `reminal list` so the user
	// can tell foreground vs. background sessions apart. Port forwards
	// are always headless — Headless is unset for them since Kind=="port"
	// already implies it.
	Headless bool `json:"headless,omitempty"`
	// Viewers is the live count of currently-attached viewers (updated by
	// the agent on every connect/disconnect event from the relay). Read
	// by `reminal info` and the "attach to existing?" prompt.
	Viewers int `json:"viewers,omitempty"`
}

// KindShell + KindPort are the canonical values of Active.Kind. New
// records always write one of these. Legacy records (pre-port-forward)
// with empty Kind are treated as KindShell.
const (
	KindShell = "shell"
	KindPort  = "port"
)

// IsPort reports whether this record is a port forward.
func (a Active) IsPort() bool { return a.Kind == KindPort }

// ReadActiveByPort returns the running port-forward bound to the given
// local port, or os.ErrNotExist if none. Scans ~/.reminal/ — cheap, the
// list is always small.
func ReadActiveByPort(port int) (*Active, error) {
	all, err := ReadAllActive()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].IsPort() && all[i].Port == port {
			return &all[i], nil
		}
	}
	return nil, os.ErrNotExist
}

// activeDir returns ~/.reminal/. Created on demand by WriteActive.
func activeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reminal"), nil
}

// activePath returns the per-session active record path. Used by
// WriteActive/ClearActive/lookup-by-id. ID must be non-empty.
func activePath(id string) (string, error) {
	if id == "" {
		return "", errors.New("active record requires a session id")
	}
	dir, err := activeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "active-"+id+".json"), nil
}

// WriteActive records the running session so `reminal info` / `reminal list`
// can find it. One file per session — multiple agents can coexist.
func WriteActive(a Active) error {
	p, err := activePath(a.ID)
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

// ClearActive deletes this session's record. Idempotent — a missing file
// is not an error since the agent may be cleaning up after never having
// written (e.g., aborted startup).
func ClearActive(id string) error {
	p, err := activePath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ReadActiveByID returns one specific session's record if its PID is still
// alive. A stale record (process gone) is removed and (nil, os.ErrNotExist)
// is returned — `reminal list` / `reminal attach` should never surface a
// dead session.
func ReadActiveByID(id string) (*Active, error) {
	p, err := activePath(id)
	if err != nil {
		return nil, err
	}
	a, err := readActiveFile(p)
	if err != nil {
		return nil, err
	}
	if !pidAlive(a.PID) {
		_ = os.Remove(p)
		return nil, os.ErrNotExist
	}
	return a, nil
}

// ReadAllActive scans ~/.reminal/ for active-*.json files, drops any
// whose PID is no longer alive (and removes their stale files), and
// returns the rest sorted by started_at ascending (oldest first).
func ReadAllActive() ([]Active, error) {
	dir, err := activeDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Active
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasPrefix(name, "active-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		full := filepath.Join(dir, name)
		a, err := readActiveFile(full)
		if err != nil {
			continue // skip corrupt entries silently — best-effort enumeration
		}
		if !pidAlive(a.PID) {
			_ = os.Remove(full)
			continue
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

// ReadActive returns the single active session if exactly one is running.
// Errors with os.ErrNotExist if none are running. Returns the first (oldest)
// when multiple exist — callers that care about disambiguation should use
// ReadAllActive or ReadActiveByID directly.
//
// Kept for back-compat with single-session callers (info, doctor) where
// "the running session" is unambiguous in the common case.
func ReadActive() (*Active, error) {
	all, err := ReadAllActive()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, os.ErrNotExist
	}
	a := all[0]
	return &a, nil
}

func readActiveFile(path string) (*Active, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a Active
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
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
