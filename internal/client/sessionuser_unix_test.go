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
			sessionUserPath, geteuid, sessionUserOwner = c.file, func() int { return c.euid }, uint32(os.Getuid())
			t.Cleanup(func() { sessionUserPath, geteuid, sessionUserOwner = sessionUserFile, os.Geteuid, 0 })
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
			// Its own ~/.local/bin first on its PATH: on its alone.
			for _, want := range []string{"HOME=" + nobody.HomeDir, "USER=nobody", "LOGNAME=nobody", "PATH=" + filepath.Join(nobody.HomeDir, ".local", "bin") + ":/usr/bin:/bin", "KEEP=1"} {
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

// The file is trusted only as root's own: it and its directory neither a
// link nor writable by anyone else. It must name a user the machine has, and
// not root — anything else refuses the session rather than start it as root.
func TestTheSessionUserFileIsReadStrictly(t *testing.T) {
	if _, err := user.Lookup("nobody"); err != nil {
		t.Skip("no user nobody here:", err)
	}
	me := uint32(os.Getuid())
	geteuid = func() int { return 0 }
	t.Cleanup(func() { sessionUserPath, geteuid, sessionUserOwner = sessionUserFile, os.Geteuid, 0 })
	for _, c := range []struct {
		name    string
		body    string
		setup   func(t *testing.T, dir, file string)
		owner   uint32
		wantErr bool
	}{
		{"a user, root's own", "nobody\n", nil, me, false},
		{"root", "root\n", nil, me, true},
		{"nobody named", "\n", nil, me, true},
		{"a user the machine does not have", "no-such-user-here\n", nil, me, true},
		{"not a user name", "../etc/passwd\n", nil, me, true},
		{"someone else's", "nobody\n", nil, me + 1, true},
		{"writable by others", "nobody\n", func(t *testing.T, _, f string) { chmod(t, f, 0o666) }, me, true},
		{"its directory writable by others", "nobody\n", func(t *testing.T, d, _ string) { chmod(t, d, 0o777) }, me, true},
		{"a link", "nobody\n", func(t *testing.T, d, f string) {
			real := filepath.Join(d, "real")
			if err := os.Rename(f, real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, f); err != nil {
				t.Fatal(err)
			}
		}, me, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "reminal")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "session-user")
			if err := os.WriteFile(file, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			chmod(t, dir, 0o755) // whatever the umask
			chmod(t, file, 0o644)
			if c.setup != nil {
				c.setup(t, dir, file)
			}
			sessionUserPath, sessionUserOwner = file, c.owner
			cmd := exec.Command("true")
			cmd.Env = []string{"HOME=/root"}
			err := asSessionUser(cmd)
			if (err != nil) != c.wantErr {
				t.Fatalf("err %v, want an error %v", err, c.wantErr)
			}
			if switched := cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil; switched == c.wantErr {
				t.Fatalf("switched %v with err %v", switched, err)
			}
		})
	}
}

func chmod(t *testing.T, p string, m os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, m); err != nil {
		t.Fatal(err)
	}
}
