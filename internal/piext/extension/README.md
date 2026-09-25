# reminal for pi

Two things, installed together by `reminal integrate`:

- **pi's live state on your reminal list.** The session shows *working*, *needs
  you*, or *done* wherever you look at your machines, so you can tell from your
  phone which pi to go back to. pi reports it from its own lifecycle, so it is
  exact rather than read off the screen.
- **reminal's tools inside pi.** Your sessions across every machine you own, a
  transcript from one of them, keys sent to one, notes posted onto a window. The
  extension asks reminal what it offers and registers what it is given, so it
  always matches the reminal on the machine.

It never shadows a tool pi already has, and it does nothing at all — quietly —
when reminal is not installed or you are not in a reminal session.

## How it is installed

`reminal integrate` (or `reminal integrate pi`) writes this directory to
`~/.pi/agent/extensions/reminal/`, where pi discovers it, plus a `bin.json`
naming the reminal that installed it. Nothing is downloaded: the sources are
embedded in the reminal binary, so the extension and the reminal it talks to are
always the same version. `reminal integrate --remove` takes it away again.

## Working on it

    scripts/pi-test/run.sh

That builds a container with a real pi in it and runs the whole chain — the
installer, the extension against a real `reminal mcp`, `reminal integrate pi`,
and a real pi loading the result and offering the tools to a model. It touches
nothing of yours: pi gets its own home directory inside the container, and the
repo is mounted read-only.

- `index.ts` — the extension: lifecycle → attention state, and the tool
  registrations.
- `mcp.ts` — a small MCP stdio client, just enough to talk to `reminal mcp`.
- `test/harness.ts` — a stand-in for pi, so both halves can be exercised without
  starting pi or spending a model call.
