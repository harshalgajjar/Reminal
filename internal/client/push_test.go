// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// openPush is the RECEIVING side of RFC 8291, written independently of
// sealPush from the RFC's steps, so a round trip proves sealPush produces what
// a browser would decrypt — not merely that it agrees with itself.
func openPush(t *testing.T, uaPriv *ecdh.PrivateKey, auth, msg []byte) []byte {
	t.Helper()
	if len(msg) < 21 {
		t.Fatal("message shorter than the aes128gcm header")
	}
	salt := msg[:16]
	rs := binary.BigEndian.Uint32(msg[16:20])
	idlen := int(msg[20])
	asPubRaw := msg[21 : 21+idlen]
	ct := msg[21+idlen:]
	if rs != pushRecordSize || idlen != 65 {
		t.Fatalf("header rs=%d idlen=%d", rs, idlen)
	}
	asPub, err := ecdh.P256().NewPublicKey(asPubRaw)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...), asPubRaw...)
	ikm, _ := hkdf.Key(sha256.New, secret, auth, string(info), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if len(pt) == 0 || pt[len(pt)-1] != 0x02 {
		t.Fatal("missing last-record delimiter")
	}
	return pt[:len(pt)-1]
}

func newTestSub(t *testing.T, endpoint string) (pushSub, *ecdh.PrivateKey, []byte) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return pushSub{
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}, priv, auth
}

func TestSealPushRoundTrip(t *testing.T) {
	sub, priv, auth := newTestSub(t, "https://fcm.googleapis.com/fcm/send/x")
	want := `{"title":"box","body":"Battery at 12%"}`
	sealed, err := sealPush(sub, []byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(openPush(t, priv, auth, sealed)); got != want {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
	if strings.Contains(string(sealed), "Battery") {
		t.Fatal("plaintext visible in the sealed message")
	}
}

func TestSealPushRejectsBadKeys(t *testing.T) {
	if _, err := sealPush(pushSub{P256dh: "short", Auth: "AAAAAAAAAAAAAAAAAAAAAA"}, []byte("x")); err == nil {
		t.Fatal("accepted a key that is not a P-256 point")
	}
	sub, _, _ := newTestSub(t, "https://x")
	if _, err := sealPush(sub, make([]byte, maxPushPlaintext+1)); err == nil {
		t.Fatal("accepted a message no push service would take")
	}
}

// sendPush must report a dead subscription distinctly, so the watcher can
// forget it instead of retrying forever.
func TestSendPushMapsGone(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wantErr error
		ok      bool
	}{{201, nil, true}, {410, errPushGone, false}, {404, errPushGone, false}, {500, nil, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["endpoint"] == "" || body["payload"] == "" {
				t.Errorf("relay request missing endpoint or payload: %v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"status": tc.status})
		}))
		old := pushURL
		pushURL = func() string { return srv.URL + "/push" }
		sub, _, _ := newTestSub(t, "https://fcm.googleapis.com/fcm/send/x")
		err := sendPush(sub, pushMessage{Title: "t", Body: "b"})
		pushURL = old
		srv.Close()
		switch {
		case tc.ok && err != nil:
			t.Errorf("status %d: unexpected error %v", tc.status, err)
		case tc.wantErr != nil && err != tc.wantErr:
			t.Errorf("status %d: err = %v, want %v", tc.status, err, tc.wantErr)
		case !tc.ok && err == nil:
			t.Errorf("status %d: want an error", tc.status)
		}
	}
}

func pct(v int) *int { return &v }

func TestEvaluatePushCPU(t *testing.T) {
	r := pushRules{CPU: true, CPUPct: 90, CPUMins: 5}
	st := &pushState{}
	t0 := time.Unix(1_000_000, 0)
	at := func(m float64) time.Time { return t0.Add(time.Duration(m * float64(time.Minute))) }
	fire := func(m, cpu float64) int {
		return len(evaluatePush(r, st, pushSample{At: at(m), CPU: cpu, CPUOK: true}, "box"))
	}
	if fire(0, 95) != 0 || fire(4, 95) != 0 {
		t.Fatal("fired before the CPU was high for the whole window")
	}
	if fire(5, 95) != 1 {
		t.Fatal("did not fire after 5 minutes above the line")
	}
	if fire(6, 97) != 0 {
		t.Fatal("fired twice for one sustained spike")
	}
	// Dipping just under the line is not a recovery.
	fire(7, 85)
	if fire(8, 95)+fire(20, 95) != 0 {
		t.Fatal("re-armed on a shallow dip")
	}
	// A real recovery re-arms, but the cooldown still holds.
	fire(21, 20)
	if fire(22, 95)+fire(28, 95) != 0 {
		t.Fatal("fired inside the cooldown")
	}
	if fire(36, 95) != 1 {
		t.Fatal("did not fire again after recovery and cooldown")
	}
}

// A brief spike never reaches the phone.
func TestEvaluatePushCPUSpikeResets(t *testing.T) {
	r := pushRules{CPU: true, CPUPct: 90, CPUMins: 5}
	st := &pushState{}
	t0 := time.Unix(1_000_000, 0)
	for i, cpu := range []float64{99, 99, 10, 99, 99, 99} {
		if n := len(evaluatePush(r, st, pushSample{At: t0.Add(time.Duration(i) * time.Minute), CPU: cpu, CPUOK: true}, "b")); n != 0 {
			t.Fatalf("minute %d: fired on an interrupted spike", i)
		}
	}
}

func TestEvaluatePushBattery(t *testing.T) {
	r := pushRules{Battery: true, BatPct: 20}
	st := &pushState{}
	now := time.Unix(1_000_000, 0)
	step := func(p int, state string) int {
		now = now.Add(10 * time.Second)
		return len(evaluatePush(r, st, pushSample{At: now, Bat: &Battery{Pct: pct(p), State: state}}, "b"))
	}
	if step(30, "discharging") != 0 {
		t.Fatal("fired above the threshold")
	}
	if step(20, "discharging") != 1 {
		t.Fatal("did not fire at the threshold")
	}
	if step(19, "discharging")+step(12, "discharging") != 0 {
		t.Fatal("fired again while still low")
	}
	// Charging re-arms; unplugging low again warns again.
	step(14, "charging")
	if step(14, "discharging") != 1 {
		t.Fatal("did not re-arm after charging")
	}
}

func TestEvaluatePushChargerSettles(t *testing.T) {
	r := pushRules{Charger: true}
	st := &pushState{}
	now := time.Unix(1_000_000, 0)
	var msgs []pushMessage
	step := func(state string) {
		now = now.Add(10 * time.Second)
		msgs = append(msgs, evaluatePush(r, st, pushSample{At: now, Bat: &Battery{Pct: pct(60), State: state}}, "b")...)
	}
	step("charging") // first reading: learns the state, says nothing
	step("discharging")
	step("charging") // a wobble that did not hold
	if len(msgs) != 0 {
		t.Fatalf("announced an unsettled change: %+v", msgs)
	}
	step("discharging")
	step("discharging")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "unplugged") {
		t.Fatalf("want one unplugged alert, got %+v", msgs)
	}
	step("charged")
	step("charged")
	if len(msgs) != 2 || !strings.Contains(msgs[1].Body, "connected") {
		t.Fatalf("want a connected alert, got %+v", msgs)
	}
}

// Turning the charger rule on later must not announce a change that happened
// while it was off.
func TestEvaluatePushChargerTrackedWhileOff(t *testing.T) {
	st := &pushState{}
	now := time.Unix(1_000_000, 0)
	step := func(r pushRules, state string) int {
		now = now.Add(10 * time.Second)
		return len(evaluatePush(r, st, pushSample{At: now, Bat: &Battery{Pct: pct(60), State: state}}, "b"))
	}
	off, on := pushRules{}, pushRules{Charger: true}
	step(off, "charging")
	step(off, "discharging")
	step(off, "discharging")
	if step(on, "discharging") != 0 {
		t.Fatal("announced a stale power change when the rule was switched on")
	}
}

func TestPushStoreUpsertAndRemove(t *testing.T) {
	isolateHome(t)
	sub, _, _ := newTestSub(t, "https://fcm.googleapis.com/fcm/send/a")
	if err := upsertPushSub("dev", sub, defaultPushRules()); err != nil {
		t.Fatal(err)
	}
	r := defaultPushRules()
	r.Charger = true
	if err := upsertPushSub("dev", sub, r); err != nil {
		t.Fatal(err)
	}
	subs, _ := loadPushSubs()
	if len(subs) != 1 || !subs[0].Rules.Charger {
		t.Fatalf("upsert did not replace in place: %+v", subs)
	}
	if err := removePushSub(sub.Endpoint); err != nil {
		t.Fatal(err)
	}
	if subs, _ := loadPushSubs(); len(subs) != 0 {
		t.Fatalf("remove left %d entries", len(subs))
	}
}

func TestPushRulesClamp(t *testing.T) {
	r := pushRules{CPUPct: 500, CPUMins: -3, BatPct: 0}.clamp()
	if r.CPUPct != 100 || r.CPUMins != 1 || r.BatPct != 20 {
		t.Fatalf("clamp = %+v", r)
	}
}

// The range rule: staying under the lower bound for the window is an alert
// too, and it re-arms only after a clear climb back up.
func TestEvaluatePushCPULowerBound(t *testing.T) {
	r := pushRules{CPU: true, CPUPct: 90, CPULow: 10, CPUMins: 2}
	st := &pushState{}
	t0 := time.Unix(1_000_000, 0)
	var got []pushMessage
	at := func(m int, cpu float64) {
		got = append(got, evaluatePush(r, st, pushSample{At: t0.Add(time.Duration(m) * time.Minute), CPU: cpu, CPUOK: true}, "b")...)
	}
	at(0, 5)
	at(1, 4)
	if len(got) != 0 {
		t.Fatal("fired before the low window elapsed")
	}
	at(2, 3)
	if len(got) != 1 || got[0].Tag != "cpu-low" || !strings.Contains(got[0].Body, "down to 3%") {
		t.Fatalf("want one low alert, got %+v", got)
	}
	at(3, 12) // inside the band but within the re-arm margin
	at(4, 5)
	at(40, 5)
	if len(got) != 1 {
		t.Fatalf("re-armed without a clear recovery: %+v", got)
	}
	// No lower bound set: a quiet machine never alerts.
	st2 := &pushState{}
	for m := 0; m < 20; m++ {
		if len(evaluatePush(pushRules{CPU: true, CPUPct: 90, CPUMins: 2}, st2, pushSample{At: t0.Add(time.Duration(m) * time.Minute), CPU: 1, CPUOK: true}, "b")) != 0 {
			t.Fatal("alerted on low CPU with no lower bound")
		}
	}
}

func TestPushRulesClampLowerBound(t *testing.T) {
	if r := (pushRules{CPUPct: 50, CPULow: 50}).clamp(); r.CPULow != 0 {
		t.Fatalf("a lower bound not under the upper one should be dropped, got %d", r.CPULow)
	}
	if r := (pushRules{CPUPct: 50, CPULow: 40}).clamp(); r.CPULow != 40 {
		t.Fatalf("a 10-point band is valid, got lower bound %d", r.CPULow)
	}
	if r := (pushRules{BatPct: 90}).clamp(); r.BatPct != 90 {
		t.Fatalf("a high battery threshold was changed to %d", r.BatPct)
	}
	if r := (pushRules{CPUPct: 90, CPULow: 10}).clamp(); r.CPULow != 10 {
		t.Fatalf("a valid lower bound was changed to %d", r.CPULow)
	}
}

// The time-left rule, and both battery alerts carrying both numbers.
func TestEvaluatePushBatteryTime(t *testing.T) {
	r := pushRules{Battery: true, BatPct: 20, BatTime: true, BatMins: 60}
	st := &pushState{}
	now := time.Unix(1_000_000, 0)
	var got []pushMessage
	step := func(p, mins int, state string) {
		now = now.Add(10 * time.Second)
		got = append(got, evaluatePush(r, st, pushSample{At: now, Bat: &Battery{Pct: pct(p), State: state, Mins: mins}}, "b")...)
	}
	step(50, 0, "discharging") // OS not estimating yet: says nothing
	step(45, 90, "discharging")
	if len(got) != 0 {
		t.Fatalf("fired above the time threshold: %+v", got)
	}
	step(40, 55, "discharging")
	if len(got) != 1 || got[0].Tag != "battery-time" || got[0].Body != "About 55 min of battery left · 40%" {
		t.Fatalf("want one time-left alert with both numbers, got %+v", got)
	}
	step(39, 50, "discharging")
	step(38, 0, "discharging") // estimate drops out: must not re-arm
	step(37, 50, "discharging")
	if len(got) != 1 {
		t.Fatalf("time-left alert repeated without a recovery: %+v", got)
	}
	step(20, 25, "discharging")
	if len(got) != 2 || got[1].Body != "Battery at 20% · about 25 min left" {
		t.Fatalf("percentage alert should carry the time too, got %+v", got)
	}
	// Charging re-arms both.
	step(22, 80, "charging")
	step(21, 40, "discharging")
	if len(got) != 3 || got[2].Tag != "battery-time" {
		t.Fatalf("time-left rule did not re-arm after charging: %+v", got)
	}
}

func TestDurText(t *testing.T) {
	for mins, want := range map[int]string{45: "45 min", 60: "1 h", 80: "1 h 20 min", 150: "2 h 30 min"} {
		if got := durText(mins); got != want {
			t.Errorf("durText(%d) = %q, want %q", mins, got, want)
		}
	}
}

// Power is re-read seconds after a change, not a tick later, so the settle is
// a duration: a change seen twice within it is still a wobble.
func TestEvaluatePushChargerSettlesByTime(t *testing.T) {
	r := pushRules{Charger: true}
	st := &pushState{}
	t0 := time.Unix(1_000_000, 0)
	at := func(ms int, state string) int {
		return len(evaluatePush(r, st, pushSample{At: t0.Add(time.Duration(ms) * time.Millisecond), Bat: &Battery{Pct: pct(60), State: state}}, "b"))
	}
	at(0, "charging")
	if at(1000, "discharging")+at(2000, "discharging") != 0 {
		t.Fatal("announced before the change had held")
	}
	if at(1000+int(pushChargerSettle/time.Millisecond), "discharging") != 1 {
		t.Fatal("did not announce once the change held")
	}
}
