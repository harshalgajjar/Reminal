// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"strconv"
	"time"
)

// envResumeStartedAtNanos carries a session's start time across a restart
// in place to the nanosecond. envResumeStartedAt has it in whole seconds,
// and alone it cut the time short at a session's first restart: what names
// a run of the session by its start (browserRun) then named a new run, once,
// for a session that was the same run. Both are handed on: a reminal from
// before this reads the seconds.
const envResumeStartedAtNanos = "REMINAL_RESUME_STARTED_AT_NS"

// resumeStartedAtEnv is a session's start time as the next image is handed
// it.
func resumeStartedAtEnv(t time.Time) []string {
	return []string{
		envResumeStartedAt + "=" + strconv.FormatInt(t.Unix(), 10),
		envResumeStartedAtNanos + "=" + strconv.FormatInt(t.UnixNano(), 10),
	}
}

// resumeStartedAt reads it back: to the nanosecond where that was handed
// on and agrees with the seconds, the seconds alone from an older reminal,
// and now when neither is there.
func resumeStartedAt(getenv func(string) string) time.Time {
	secs, _ := strconv.ParseInt(getenv(envResumeStartedAt), 10, 64)
	nanos, _ := strconv.ParseInt(getenv(envResumeStartedAtNanos), 10, 64)
	if nanos > 0 && (secs == 0 || time.Unix(0, nanos).Unix() == secs) {
		return time.Unix(0, nanos)
	}
	if secs == 0 {
		return time.Now()
	}
	return time.Unix(secs, 0)
}
