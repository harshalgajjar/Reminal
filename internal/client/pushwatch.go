// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// The daemon watches this machine's CPU and power on behalf of every phone
// that subscribed, and pushes when one of that phone's rules trips. It runs
// whether or not anyone is viewing — which is the point: the alert matters
// most when nobody is looking — and costs nothing on a machine with no
// subscribers, because it only samples when some phone asked for something.

// pushTick is how often the watcher samples CPU. Power is not left to this
// tick: a plug or unplug wakes the watcher straight away (see
// watchPowerChanges), so "charger unplugged" arrives while the person is still
// standing at the desk.
const pushTick = 10 * time.Second

// pushCPUCooldown is the least time between two CPU alerts to one phone. A
// build that pins the CPU for an hour should say so once, not six times.
const pushCPUCooldown = 30 * time.Minute

// pushChargerSettle is how long a power change must hold before it is
// announced, so a wobbly cable is one alert, not a burst. Short, because the
// watcher re-reads power this long after any change instead of waiting for
// the next tick.
const pushChargerSettle = 3 * time.Second

// pushSample is one reading of the machine.
type pushSample struct {
	At    time.Time
	CPU   float64
	CPUOK bool
	Bat   *Battery
}

// pushState is one phone's memory of what it has already been told.
type pushState struct {
	cpuHigh pushBand // above the upper line
	cpuLow  pushBand // below the lower line

	batDisarmed  bool // fired; re-arms on charging or a clear recovery
	timeDisarmed bool // same, for the time-left rule

	plugged      *bool // last settled "on AC" state; nil until first reading
	pendingPlug  bool
	pendingSince time.Time // when the unsettled change was first seen; zero if none
}

// pushBand tracks one "outside the range" condition: how long it has held,
// whether it has already been announced, and when.
type pushBand struct {
	since    time.Time
	firedAt  time.Time
	disarmed bool // announced; re-arms only after a clear recovery
}

// step advances the band by one sample. out: the condition holds now.
// recovered: the reading is far enough back inside to re-arm (a margin, so
// hovering at the line cannot alternate fire / re-arm / fire). Reports the
// duration held when an alert is due.
func (b *pushBand) step(at time.Time, out, recovered bool, need time.Duration) (time.Duration, bool) {
	if !out {
		b.since = time.Time{}
		if recovered {
			b.disarmed = false
		}
		return 0, false
	}
	if b.since.IsZero() {
		b.since = at
	}
	held := at.Sub(b.since)
	cooled := b.firedAt.IsZero() || at.Sub(b.firedAt) >= pushCPUCooldown
	if b.disarmed || !cooled || held < need {
		return 0, false
	}
	b.disarmed, b.firedAt = true, at
	return held, true
}

// evaluatePush advances st by one sample and returns the alerts rules ask for.
// Pure: no I/O, so the edge cases (hysteresis, cooldown, debounce) are tested
// directly rather than by waiting on a real battery.
func evaluatePush(rules pushRules, st *pushState, s pushSample, host string) []pushMessage {
	var out []pushMessage

	if s.CPUOK && rules.CPU {
		mins := time.Duration(rules.CPUMins) * time.Minute
		if m, ok := st.cpuHigh.step(s.At, s.CPU >= float64(rules.CPUPct), s.CPU < float64(rules.CPUPct)-10, mins); ok {
			out = append(out, pushMessage{Title: host, Body: fmt.Sprintf("CPU at %.0f%% for %s", s.CPU, humanMins(m)), Tag: "cpu"})
		}
		if rules.CPULow > 0 {
			if m, ok := st.cpuLow.step(s.At, s.CPU <= float64(rules.CPULow), s.CPU > float64(rules.CPULow)+5, mins); ok {
				out = append(out, pushMessage{Title: host, Body: fmt.Sprintf("CPU down to %.0f%% for %s", s.CPU, humanMins(m)), Tag: "cpu-low"})
			}
		}
	}

	if b := s.Bat; b != nil && b.Pct != nil {
		pct := *b.Pct
		onAC := b.State != "discharging"

		// Both battery alerts carry both numbers: a percentage says little
		// without the time it buys, and a time says little without the charge.
		if !onAC && pct <= rules.BatPct {
			if rules.Battery && !st.batDisarmed {
				body := fmt.Sprintf("Battery at %d%% — plug in soon", pct)
				if b.Mins > 0 {
					body = fmt.Sprintf("Battery at %d%% · about %s left", pct, durText(b.Mins))
				}
				out = append(out, pushMessage{Title: host, Body: body, Tag: "battery"})
				st.batDisarmed = true
			}
		} else if onAC || pct > rules.BatPct+5 {
			st.batDisarmed = false
		}

		// Time left, only where the OS estimates it. A reading of 0 means the
		// OS declined to guess (it does for a minute or two after any power
		// change), which neither trips the rule nor re-arms it.
		if !onAC && b.Mins > 0 && b.Mins <= rules.BatMins {
			if rules.BatTime && !st.timeDisarmed {
				out = append(out, pushMessage{
					Title: host,
					Body:  fmt.Sprintf("About %s of battery left · %d%%", durText(b.Mins), pct),
					Tag:   "battery-time",
				})
				st.timeDisarmed = true
			}
		} else if onAC || b.Mins > rules.BatMins+15 {
			st.timeDisarmed = false
		}

		// Tracked even while the charger rule is off, so switching it on
		// later does not announce a change that happened hours ago.
		switch {
		case st.plugged == nil:
			v := onAC
			st.plugged = &v
		case onAC == *st.plugged:
			st.pendingSince = time.Time{}
		default:
			if st.pendingSince.IsZero() || st.pendingPlug != onAC {
				st.pendingPlug, st.pendingSince = onAC, s.At
			}
			if s.At.Sub(st.pendingSince) >= pushChargerSettle {
				v := onAC
				st.plugged, st.pendingSince = &v, time.Time{}
				if rules.Charger {
					body := fmt.Sprintf("Charger unplugged — on battery at %d%%", pct)
					if b.Mins > 0 {
						body += fmt.Sprintf(" · about %s left", durText(b.Mins))
					}
					if onAC {
						body = fmt.Sprintf("Charger connected — %d%%", pct)
					}
					out = append(out, pushMessage{Title: host, Body: body, Tag: "charger"})
				}
			}
		}
	}
	return out
}

func humanMins(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m <= 1 {
		return "a minute"
	}
	return fmt.Sprintf("%d minutes", m)
}

// durText renders an OS time estimate the way people say it: "45 min",
// "1 h", "1 h 20 min".
func durText(mins int) string {
	h, m := mins/60, mins%60
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		return fmt.Sprintf("%d h", h)
	default:
		return fmt.Sprintf("%d h %d min", h, m)
	}
}

// pushHostLabel is the notification title: the name people call this machine.
func pushHostLabel() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "reminal"
	}
	return strings.TrimSuffix(h, ".local")
}

// runPushWatcher is the daemon's alert loop.
func runPushWatcher(stop <-chan struct{}) {
	states := map[string]*pushState{}
	t := time.NewTicker(pushTick)
	defer t.Stop()
	power := make(chan struct{}, 1)
	go watchPowerChanges(stop, power)
	// recheck confirms a power change once it has had pushChargerSettle to
	// hold, instead of leaving it for the next tick.
	recheck := time.NewTimer(time.Hour)
	recheck.Stop()
	for {
		fresh := false
		select {
		case <-stop:
			return
		case <-t.C:
		case <-power:
			fresh = true
			recheck.Reset(pushChargerSettle + 200*time.Millisecond)
		case <-recheck.C:
			fresh = true
		}
		pushStoreMu.Lock()
		subs, err := loadPushSubs()
		pushStoreMu.Unlock()
		if err != nil || len(subs) == 0 {
			clear(states)
			continue
		}
		wantCPU, wantAny := false, false
		for _, e := range subs {
			wantCPU = wantCPU || e.Rules.CPU
			wantAny = wantAny || e.Rules.any()
		}
		if !wantAny {
			continue
		}
		s := pushSample{At: time.Now()}
		if fresh {
			s.Bat = FreshBattery()
		} else {
			s.Bat = CurrentBattery()
		}
		if wantCPU && !fresh {
			s.CPU, s.CPUOK = cpuPercent()
		}
		host := pushHostLabel()
		live := map[string]bool{}
		for _, e := range subs {
			ep := e.Sub.Endpoint
			live[ep] = true
			st := states[ep]
			if st == nil {
				st = &pushState{}
				states[ep] = st
			}
			for _, m := range evaluatePush(e.Rules.clamp(), st, s, host) {
				m.At = s.At.UnixMilli()
				go deliverPush(e, m)
			}
		}
		for ep := range states {
			if !live[ep] {
				delete(states, ep)
			}
		}
	}
}

// deliverPush sends one alert, and forgets the subscription if the phone is
// gone or its owner has been removed from this machine.
func deliverPush(e pushEntry, m pushMessage) {
	if !e.stillOwner() {
		_ = removePushSub(e.Sub.Endpoint)
		return
	}
	m.URL = "/"
	err := sendPush(e.Sub, m)
	if errors.Is(err, errPushGone) {
		_ = removePushSub(e.Sub.Endpoint)
		return
	}
	if err != nil {
		log.Printf("alert not delivered: %v", err)
	}
}
