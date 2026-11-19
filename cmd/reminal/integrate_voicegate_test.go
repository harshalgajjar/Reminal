package main

import (
	"strings"
	"testing"

	"reminal/internal/client"
	"reminal/internal/piext"
)

// What each integration's hook can report decides how voice may type into
// it (client.HookReportsWaiting): an agent whose hook says when it waits on
// the person ("input") is guarded by that report; one whose hook cannot say
// it is typed into only at rest. The table must say exactly what the hooks
// map, so a new integration (or a changed one) cannot fall into the
// permissive row by default.
func TestVoiceGateTableMatchesWhatEachHookReports(t *testing.T) {
	for _, it := range agentTargets() {
		waits, known := client.HookReportsWaiting[it.Bin]
		var reports, saysWaiting, saysReady, saysWorking bool
		switch {
		case it.hooks != nil:
			reports = true
			for _, ev := range it.hooks.events {
				// "notify" is split by its payload into "input" (a permission or
				// a question) or "done" (idle).
				saysReady = saysReady || ev.State == "ready"
				saysWorking = saysWorking || ev.State == "working"
				if ev.State == "input" || ev.State == "notify" {
					saysWaiting = true
				}
			}
		case it.plugin != nil:
			reports = true
			saysWaiting = strings.Contains(it.plugin.body("/usr/local/bin/reminal"), `report("input")`)
		case it.Bin == "pi":
			reports = true
			saysWaiting = strings.Contains(piext.ExtensionSource(), `report("input"`)
		}
		switch {
		case !reports && known:
			t.Errorf("%s (%s): no hook reports anything, but the voice table lists it", it.Name, it.Bin)
		case reports && !known:
			t.Errorf("%s (%s): its hook reports, but the voice table does not say whether it reports waiting", it.Name, it.Bin)
		case it.hooks != nil && saysReady && !saysWorking:
			// An agent typed into at rest only (it cannot report waiting) is at
			// rest while its last report is "ready": a hook that reports "ready"
			// and never "working" would leave it "ready" through every turn.
			t.Errorf("%s (%s): its hook reports ready but never working", it.Name, it.Bin)
		case reports && waits != saysWaiting:
			t.Errorf("%s (%s): the voice table says reports-waiting=%v, its hook says %v", it.Name, it.Bin, waits, saysWaiting)
		}
	}
}
