package client

import "os/exec"

// reapDetached waits on a detached child in the background so it does not
// linger in the process table after it exits.
//
// This replaced `cmd.Process.Release()`, which looks like it does the same job
// and does not. Release only drops our handle on the child: on Unix the child
// is still our child, and when it exits the kernel keeps its entry until
// someone calls Wait. That is invisible when the parent is a short-lived CLI --
// the parent exits first, init inherits the child and reaps it -- and it is a
// slow leak whenever the parent outlives what it spawned. The daemon is that
// parent: it spawns a session for every `reminal new` that arrives over the
// machine channel (the "+" button on the web), so each of those sessions left
// an entry behind when it ended, for as long as the daemon ran.
//
// Waiting here is safe for these callers because all of them hand the child
// *os.File stdio (/dev/null) rather than a pipe, so Wait blocks on the process
// alone and returns as soon as it exits. If the parent exits first the
// goroutine simply dies with it, and the child reparents to init exactly as it
// did under Release.
func reapDetached(cmd *exec.Cmd) {
	go func() { _ = cmd.Wait() }()
}
