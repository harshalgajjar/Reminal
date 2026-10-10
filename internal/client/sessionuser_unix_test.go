// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build !windows

package client

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// A machine that names a session user runs its sessions as that user — and
// only when reminal itself runs as root there. Anywhere else (a person's own
// computer: no such file, not root) a session starts exactly as before.
func TestASessionRunsAsTheMachinesSessionUserOnlyWhenRootAndNamed(t *testing.T) {
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no user nobody here:", err)
	}
	named := filepath.Join(t.TempDir(), "session-user")
	if err := os.WriteFile(named, []byte("nobody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "none")
	cases := []struct {
		name    string
		euid    int
		file    string
		switch_ bool
	}{
		{"not root, a user named", 1000, named, false},
		{"root, no user named", 0, missing, false},
		{"root, a user named", 0, named, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sessionUserPath, geteuid = c.file, func() int { return c.euid }
			t.Cleanup(func() { sessionUserPath, geteuid = sessionUserFile, os.Geteuid })
			cmd := exec.Command("true")
			cmd.Env = []string{"HOME=/root", "USER=root", "LOGNAME=root", "PATH=/usr/bin:/bin", "KEEP=1"}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := asSessionUser(cmd); err != nil {
				t.Fatal(err)
			}
			if !cmd.SysProcAttr.Setsid {
				t.Error("its detach (Setsid) was lost")
			}
			if !c.switch_ {
				if cmd.SysProcAttr.Credential != nil {
					t.Fatalf("switched user: %+v", cmd.SysProcAttr.Credential)
				}
				if !slices.Contains(cmd.Env, "HOME=/root") {
					t.Errorf("its environment changed: %v", cmd.Env)
				}
				return
			}
			cr := cmd.SysProcAttr.Credential
			if cr == nil || strconv.Itoa(int(cr.Uid)) != nobody.Uid || strconv.Itoa(int(cr.Gid)) != nobody.Gid {
				t.Fatalf("not the session user: %+v (want uid %s gid %s)", cr, nobody.Uid, nobody.Gid)
			}
			for _, want := range []string{"HOME=" + nobody.HomeDir, "USER=nobody", "LOGNAME=nobody", "PATH=/usr/bin:/bin", "KEEP=1"} {
				if !slices.Contains(cmd.Env, want) {
					t.Errorf("its environment lacks %s: %v", want, cmd.Env)
				}
			}
			if slices.Contains(cmd.Env, "HOME=/root") || slices.Contains(cmd.Env, "USER=root") {
				t.Errorf("root's own still in its environment: %v", cmd.Env)
			}
		})
	}
}

// A file naming root, or nobody at all, changes nothing; one naming a user
// the machine does not have refuses the session rather than start it as
// root.
func TestTheSessionUserFileIsReadStrictly(t *testing.T) {
	dir := t.TempDir()
	geteuid = func() int { return 0 }
	t.Cleanup(func() { sessionUserPath, geteuid = sessionUserFile, os.Geteuid })
	for _, c := range []struct {
		body    string
		wantErr bool
	}{
		{"root\n", false},
		{"\n", false},
		{"no-such-user-here\n", true},
		{"../etc/passwd\n", true},
	} {
		p := filepath.Join(dir, strings.Trim(strings.ReplaceAll(c.body, "/", "_"), "\n.")+"x")
		if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		sessionUserPath = p
		cmd := exec.Command("true")
		cmd.Env = []string{"HOME=/root"}
		err := asSessionUser(cmd)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err %v, want an error %v", c.body, err, c.wantErr)
		}
		if cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil {
			t.Errorf("%q: switched user: %+v", c.body, cmd.SysProcAttr.Credential)
		}
	}
}
