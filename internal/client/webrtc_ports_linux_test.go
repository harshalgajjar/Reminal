// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

// ownUDPPorts is the local ports of this process's UDP sockets: the kernel's
// list of them (/proc/net/udp*), kept to the ones this process holds open.
func ownUDPPorts(t *testing.T) map[int]bool {
	t.Helper()
	mine := map[string]bool{}
	fds, _ := os.ReadDir("/proc/self/fd")
	for _, fd := range fds {
		if l, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name())); err == nil && strings.HasPrefix(l, "socket:[") {
			mine[strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]")] = true
		}
	}
	ports := map[int]bool{}
	for _, name := range []string{"/proc/net/udp", "/proc/net/udp6"} {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 10 || !mine[fields[9]] {
				continue
			}
			if i := strings.LastIndex(fields[1], ":"); i >= 0 {
				if p, err := strconv.ParseInt(fields[1][i+1:], 16, 32); err == nil {
					ports[int(p)] = true
				}
			}
		}
		f.Close()
	}
	return ports
}

// gatherWith makes a peer connection with se, lets it gather, and says
// which UDP ports this process holds meanwhile.
func gatherWith(t *testing.T, se webrtc.SettingEngine) map[int]bool {
	t.Helper()
	api := webrtc.NewAPI(webrtc.WithMediaEngine(&webrtc.MediaEngine{}), webrtc.WithInterceptorRegistry(&interceptor.Registry{}), webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.CreateDataChannel("frames", nil); err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(pc)
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	return ownUDPPorts(t)
}

// On Windows a peer connection binds no set port: a program with no
// firewall rule that binds one is asked about by Windows Firewall, in a
// window that stays on top and under which every click reminal sends into a
// mirrored window is dropped. The set port was local name lookup's (5353).
// Read here from the sockets a connection made the Windows way really holds
// (Linux can list its own); the same connection made the default way holds
// 5353, which is what says the reading sees it.
func TestPeerConnectionsOnWindowsBindNoSetPort(t *testing.T) {
	before := ownUDPPorts(t)
	if before[5353] {
		t.Skip("this process already holds port 5353")
	}
	held := gatherWith(t, rtcSettings("windows"))
	if held[5353] {
		t.Errorf("a peer connection set up as on Windows holds UDP port 5353 (ports %v)", held)
	}
	for p := range held {
		if !before[p] && p < 1024 {
			t.Errorf("a peer connection set up as on Windows holds the set port %d", p)
		}
	}
	if def := gatherWith(t, rtcSettings("linux")); !def[5353] {
		t.Skipf("the default set-up holds no port 5353 here (ports %v): this machine cannot show the difference", def)
	}
}
