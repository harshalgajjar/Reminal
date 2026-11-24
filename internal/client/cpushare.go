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
	"syscall"
	"time"
)

// One CPU sampler for the whole machine.
//
// Where reading the CPU costs something (macOS: there is no cheap call for
// it without cgo, so a program has to be run), every reminal process on the
// machine reads one shared value, and at most one of them runs the sampler.
// Each session is its own process, and each used to take its own reading
// whenever a viewer or a notice asked for the host's state: on a machine
// with sixty sessions that was sixty `top` at once, several cores spent on
// measuring the CPU.
//
// The files, in reminal's folder:
//
//	cpu       the latest reading and when it was taken ("42 1795500000000000000")
//	cpu.want  touched by readers: its time is when a reading was last wanted
//	cpu.lock  held (flock) by the one process that is sampling
//
// A reader takes the value from cpu when it is fresh. When it is not, it
// tries for the lock, and the one that gets it samples and writes cpu once a
// second for everyone. The sampler stops when no reader has wanted a reading
// for a while, so a machine nobody is looking at runs nothing and writes
// nothing. It does not depend on the daemon: a session alone on a machine
// samples for itself. A holder that dies lets go of the lock (the kernel's
// doing), the value goes stale, and the next reader takes over.
type cpuShare struct {
	dir string
	// sample runs the sampler until emit answers false (nobody wants
	// readings any more) or wanted does (asked when there is no reading to
	// emit), then returns. It returns early if it cannot sample at all.
	sample func(emit func(pct float64) (more bool), wanted func() bool)

	fresh     time.Duration // a reading this old or newer is the value
	wantEvery time.Duration // a reader touches cpu.want at most this often
	wantFor   time.Duration // the sampler stops when cpu.want is older
	tryEvery  time.Duration // a reader tries for the lock at most this often

	mu       sync.Mutex
	sampling bool
	wantAt   time.Time
	triedAt  time.Time
}

func (s *cpuShare) path(name string) string { return filepath.Join(s.dir, name) }

// read is the machine's CPU reading, when there is a fresh one. With none,
// this process offers to be the sampler and says "unknown" for now: the
// caller shows its last value until a reading is there.
func (s *cpuShare) read() (float64, bool) {
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
	if pct, at, ok := s.latest(); ok && now.Sub(at) < s.fresh {
		return pct, true
	}
	s.mu.Lock()
	try := !s.sampling && now.Sub(s.triedAt) >= s.tryEvery
	if try {
		s.triedAt = now
	}
	s.mu.Unlock()
	if try {
		go s.lead()
	}
	return 0, false
}

func (s *cpuShare) touchWant(now time.Time) {
	p := s.path("cpu.want")
	if os.Chtimes(p, now, now) == nil {
		return
	}
	_ = os.MkdirAll(s.dir, 0o700)
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
		_ = os.Chtimes(p, now, now)
	}
}

func (s *cpuShare) wanted() bool {
	st, err := os.Stat(s.path("cpu.want"))
	return err == nil && time.Since(st.ModTime()) < s.wantFor
}

// latest is the reading in cpu and when it was taken.
func (s *cpuShare) latest() (pct float64, at time.Time, ok bool) {
	raw, err := os.ReadFile(s.path("cpu"))
	if err != nil {
		return 0, time.Time{}, false
	}
	f := strings.Fields(string(raw))
	if len(f) != 2 {
		return 0, time.Time{}, false
	}
	pct, err = strconv.ParseFloat(f[0], 64)
	ns, err2 := strconv.ParseInt(f[1], 10, 64)
	if err != nil || err2 != nil || pct < 0 || pct > 100 {
		return 0, time.Time{}, false
	}
	return pct, time.Unix(0, ns), true
}

// lead samples for the machine if no other process is: it holds cpu.lock
// while it does, and writes each reading to cpu (a new file renamed over the
// old one, so a reader never sees half of one).
func (s *cpuShare) lead() {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return
	}
	lock, err := os.OpenFile(s.path("cpu.lock"), os.O_CREATE|os.O_RDWR, 0o600)
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
	tmp := s.path(fmt.Sprintf("cpu.%d.tmp", os.Getpid()))
	defer os.Remove(tmp)
	s.sample(func(pct float64) bool {
		line := strconv.FormatFloat(pct, 'f', 1, 64) + " " + strconv.FormatInt(time.Now().UnixNano(), 10) + "\n"
		if os.WriteFile(tmp, []byte(line), 0o600) == nil {
			_ = os.Rename(tmp, s.path("cpu"))
		}
		return s.wanted()
	}, s.wanted)
}
