package client

import (
	"sync"
	"time"
)

// outageGrace is how long the relay link may stay unsteady before the
// terminal is told. A link that drops and comes straight back is not worth a
// line: on a network whose middlebox resets the socket every few minutes, a
// line for each drop and another for each return filled the screen.
const outageGrace = 20 * time.Second

// outageNotice decides when a session tells its terminal that the relay link
// is down, and when that it is back. Both lines go into the person's own
// terminal, between whatever their shell or agent drew, so they are said
// once an outage, and only for a real one:
//   - down is said once the link has been unsteady for outageGrace, or at
//     once while the session has never reached the relay: a session started
//     offline says so straight away, not half a minute after its join link;
//   - back is said once a connection has held for stableThresh, and only
//     when down was said. A connection that comes up and drops again within
//     that time is part of the same outage, not its end.
//
// The serveRelay loop calls failed; the steady timer of a live connection
// calls steady, so the state is behind a mutex.
type outageNotice struct {
	mu        sync.Mutex
	said      bool
	reached   bool      // a connection has reached the relay in this run
	downSince time.Time // when the link last stopped being steady; zero while steady
}

// failed records a connection attempt that failed or a connection that
// dropped, and reports whether the outage should be said now. now forces it
// (a throttle's long wait is worth saying at once).
func (o *outageNotice) failed(at time.Time, now bool) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.downSince.IsZero() {
		o.downSince = at
	}
	if o.said || (!now && o.reached && at.Sub(o.downSince) < outageGrace) {
		return false
	}
	o.said = true
	return true
}

// up records a connection that reached the relay: from then on a drop gets
// the grace.
func (o *outageNotice) up() {
	o.mu.Lock()
	o.reached = true
	o.mu.Unlock()
}

// steady records a connection that has held for stableThresh, and reports
// whether "back online" should be said.
func (o *outageNotice) steady() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.downSince = time.Time{}
	if !o.said {
		return false
	}
	o.said = false
	return true
}
