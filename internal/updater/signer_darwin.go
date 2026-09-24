// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build darwin

package updater

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sameSigner refuses a new build unless its app is signed exactly as this
// one's is — the same designated requirement, which names the bundle id and
// the certificate. On macOS that is what a Screen Recording or Accessibility
// grant is tied to, and it is the one check a build cannot pass by describing
// itself: only a build signed with our certificate satisfies it.
//
// An install that is itself unsigned has no requirement to compare against,
// and cannot tell a genuine build from another — so it does not switch.
func sameSigner(newBin string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	selfApp, newApp := bundleRoot(self), bundleRoot(newBin)
	if selfApp == "" || newApp == "" {
		return fmt.Errorf("switching needs the signed reminal.app on both sides — reinstall reminal and try again")
	}
	out, err := exec.Command("/usr/bin/codesign", "-d", "-r-", selfApp).CombinedOutput()
	if err != nil {
		return fmt.Errorf("could not read this install's signature: %v", err)
	}
	var req string
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "designated => "); i >= 0 {
			req = strings.TrimSpace(line[i+len("designated => "):])
		}
	}
	if !strings.Contains(req, "certificate") {
		return fmt.Errorf("this install is not signed with a certificate, so it cannot tell a genuine build from another — reinstall reminal and try again")
	}
	var stderr bytes.Buffer
	cmd := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", "-R="+req, newApp)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the downloaded app is not signed as this one is — nothing was installed (%s)", strings.TrimSpace(stderr.String()))
	}
	return nil
}
