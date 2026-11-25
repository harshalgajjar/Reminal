// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// One sampler for the whole machine, for a reading that costs something.
//
// Where reading a thing about the machine means running a program (macOS:
// the CPU and the battery, with no cheap call for either without cgo), every
// reminal process on the machine reads one shared value, and at most one of
// them runs the sampler. Each session is its own process, and each used to
// take its own reading whenever a viewer or a notice asked for the host's
// state: on a machine with sixty sessions that was sixty `top` at once,
// several cores spent on measuring the CPU.
//
// The files of a reading called name, in reminal's folder:
//
//	name       the latest value and when it was taken ("42.0 1795500000000000000")
//	name.want  touched by readers: its time is when a reading was last wanted
//	name.lock  held (flock) by the one process that is sampling
//
// A reader takes the value from the file when it is fresh. When it is not,
// it tries for the lock, and the one that gets it samples and writes the
// file for everyone. The sampler stops when no reader has wanted a reading
// for a while, so a machine nobody is looking at runs nothing and writes
// nothing. It does not depend on the daemon: a session alone on a machine
// samples for itself. A holder that dies lets go of the lock (the kernel's
// doing), the value goes stale, and the next reader takes over.
//
// A value is one word (no spaces, no line breaks): the time follows it in
// the file. Processes of different reminal versions share these files (a
// session runs the reminal it was started with), so a reading's name and the
// form of its value stay as they were first written.
type machineShare struct {
	dir  string
	name string
	// sample runs the sampler until emit answers false (nobody wants
	// readings any more) or wanted does (asked when there is no reading to
	// emit), then returns. It returns early if it cannot sample at all.
	sample func(emit func(val string) (more bool), wanted func() bool)

	fresh     time.Duration // a value this old or newer is the reading
	wantEvery time.Duration // a reader touches name.want at most this often
	wantFor   time.Duration // the sampler stops when name.want is older
	tryEvery  time.Duration // a reader tries for the lock at most this often

	mu       sync.Mutex
	sampling bool
	wantAt   time.Time
	triedAt  time.Time
	// leading counts this share's offers to sample that have not ended yet,
	// the one sampling among them: none of them touches the directory once
	// it is zero.
	leading atomic.Int32
}

func (s *machineShare) path(suffix string) string { return filepath.Join(s.dir, s.name+suffix) }

// read is the machine's reading, when there is a fresh one. With none, this
// process offers to be the sampler and says "unknown" for now.
func (s *machineShare) read() (string, bool) {
	now := time.Now()
	s.mu.Lock()
	want := now.Sub(s.wantAt) >= s.wantEvery
	if want {
		s.wantAt = now
	}
	s.mu.Unlock()
	if want {
		s.touchWant(now)
	}
	if val, at, ok := s.latest(); ok && now.Sub(at) < s.fresh {
		return val, true
	}
	s.mu.Lock()
	try := !s.sampling && now.Sub(s.triedAt) >= s.tryEvery
	if try {
		s.triedAt = now
	}
	s.mu.Unlock()
	if try {
		s.leading.Add(1)
		go s.lead()
	}
	return "", false
}

// readWithin is read, waiting up to d for a reading when there is none yet:
// for a sampler whose first reading comes at once.
func (s *machineShare) readWithin(d time.Duration) (string, bool) {
	for end := time.Now().Add(d); ; time.Sleep(20 * time.Millisecond) {
		if val, ok := s.read(); ok || !time.Now().Before(end) {
			return val, ok
		}
	}
}

func (s *machineShare) touchWant(now time.Time) {
	p := s.path(".want")
	if os.Chtimes(p, now, now) == nil {
		return
	}
	_ = os.MkdirAll(s.dir, 0o700)
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
		_ = os.Chtimes(p, now, now)
	}
}

func (s *machineShare) wanted() bool {
	st, err := os.Stat(s.path(".want"))
	return err == nil && time.Since(st.ModTime()) < s.wantFor
}

// latest is the value in the file and when it was taken.
func (s *machineShare) latest() (val string, at time.Time, ok bool) {
	raw, err := os.ReadFile(s.path(""))
	if err != nil {
		return "", time.Time{}, false
	}
	f := strings.Fields(string(raw))
	if len(f) != 2 {
		return "", time.Time{}, false
	}
	ns, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	return f[0], time.Unix(0, ns), true
}

// lead samples for the machine if no other process is: it holds name.lock
// while it does, and writes each value to the file (a new file renamed over
// the old one, so a reader never sees half of one).
func (s *machineShare) lead() {
	defer s.leading.Add(-1)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return
	}
	lock, err := os.OpenFile(s.path(".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return // another process is sampling
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	s.mu.Lock()
	s.sampling = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.sampling = false
		s.mu.Unlock()
	}()
	tmp := s.path(fmt.Sprintf(".%d.tmp", os.Getpid()))
	defer os.Remove(tmp)
	s.sample(func(val string) bool {
		if val != "" && !strings.ContainsAny(val, " \t\r\n") {
			line := val + " " + strconv.FormatInt(time.Now().UnixNano(), 10) + "\n"
			if os.WriteFile(tmp, []byte(line), 0o600) == nil {
				_ = os.Rename(tmp, s.path(""))
			}
		}
		return s.wanted()
	}, s.wanted)
}
