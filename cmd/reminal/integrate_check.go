package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"reminal/internal/atomicfile"
	"reminal/internal/piext"
	"reminal/internal/protocol"
	"reminal/internal/session"
)

// ---- `reminal integrate --check` ------------------------------------------
//
// Reads each agent's registration BACK, the way integrate wrote it, so that
// "is Claude set up for reminal on this machine?" has an answer without
// guessing from a harness's behaviour. The Machines view asks this over the
// machine channel before offering to run the setup, and shows the restart
// hint afterwards — an agent loads its MCP servers only when it starts.

// integrationReport checks every known agent (or only the named ones).
func integrationReport(home, exe string, only []string) []protocol.IntegrationStatus {
	var out []protocol.IntegrationStatus
	for _, t := range agentTargets() {
		if len(only) > 0 && !matchesAny(t, only) {
			continue
		}
		out = append(out, checkIntegration(t, home, exe))
	}
	return out
}

// checkIntegration reads one agent's config and reports what it registers.
func checkIntegration(t agentTarget, home, exe string) protocol.IntegrationStatus {
	st := protocol.IntegrationStatus{
		Bin: t.Bin, Name: t.Name,
		HooksWanted: t.hooks != nil,
		Restart:     restartHint(t),
		Resume:      t.resumeHint(),
		Since:       setUpAt(home)[t.Bin],
	}
	if _, err := exec.LookPath(t.Bin); err == nil {
		st.Installed = true
	}
	switch {
	case t.install != nil:
		// The extension route (pi): the package in pi's extensions directory
		// is the registration, and it bakes in the installing reminal's path.
		dir := piext.Dir(home)
		st.Config = tildePath(home, filepath.Join(dir, "package.json"))
		st.Known = true
		if raw, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
			st.Integrated = strings.Contains(string(raw), `"reminal-pi"`)
			// Also whether it is the extension THIS binary carries. Upgrading
			// reminal replaces the binary at the same path and leaves the installed
			// copy alone, so every path can match while pi loads older code — and
			// saying "current" there is how a fix reaches nobody.
			st.Current = st.Integrated && extensionNames(dir, exe) && piext.UpToDate(home)
			st.ConfigMtime = mtime(filepath.Join(dir, "package.json"))
		} else if !os.IsNotExist(err) {
			st.Error = err.Error()
		}
	case t.checkTOML != "":
		path := filepath.Join(home, t.checkTOML)
		st.Config = tildePath(home, path)
		st.Known = true
		if raw, err := os.ReadFile(path); err == nil {
			st.Integrated, st.Current = tomlRegisters(string(raw), exe)
			st.ConfigMtime = mtime(path)
		} else if !os.IsNotExist(err) {
			st.Error = err.Error()
		}
	default:
		file, key := t.file, t.keyPath
		if t.checkFile != "" {
			file, key = t.checkFile, t.checkKey
		}
		if file == "" {
			return st // nothing we know how to read; Known stays false
		}
		path := filepath.Join(home, file)
		st.Config = tildePath(home, path)
		st.Known = true
		entry, err := jsonEntry(path, key, mcpServerName)
		switch {
		case err != nil:
			st.Error = err.Error()
		case entry != nil:
			st.Integrated = true
			st.Current = samePath(commandOf(entry), exe)
		}
		st.ConfigMtime = mtime(path)
	}
	if t.hooks != nil {
		st.Hooks = hooksInstalled(t.hooks, home)
		// Set up for another version of the agent than the one installed
		// now (upgraded or taken back since): not set up as it is — the
		// Machines view offers to set it up again (hooks_heal.go).
		if st.Hooks && st.Installed && hookVersionMismatch(t.hooks, home, agentVersionIfGated(t)) {
			st.Hooks = false
		}
	}
	// A plugin (OpenCode's) is code of ours the agent loads, kept where the
	// agent reads it: upgrading reminal leaves the copy written before, so
	// one that is not what this binary writes is not current, and the
	// Machines view offers to set it up again, as for pi's extension.
	if t.plugin != nil && st.Current && !pluginCurrent(t.plugin, home, exe) {
		st.Current = false
	}
	return st
}

// jsonEntry returns the map stored under name at keyPath in a JSON file — nil
// when the file, the path, or the entry is absent; an error only when the
// file exists and is not JSON (integrate would refuse to touch it too).
func jsonEntry(path string, keyPath []string, name string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, nil
	}
	root := map[string]any{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON", tildePath(os.Getenv("HOME"), path))
	}
	node := root
	for _, k := range keyPath {
		next, ok := node[k].(map[string]any)
		if !ok {
			return nil, nil
		}
		node = next
	}
	entry, _ := node[name].(map[string]any)
	return entry, nil
}

// commandOf is the executable an MCP entry launches: a "command" string, or
// the first element of a "command" list (opencode's shape).
func commandOf(entry map[string]any) string {
	switch c := entry["command"].(type) {
	case string:
		return c
	case []any:
		if len(c) > 0 {
			s, _ := c[0].(string)
			return s
		}
	}
	return ""
}

// tomlRegisters reads Codex's config.toml just far enough to find the
// [mcp_servers.reminal] table and its command — no TOML library for one
// section of one file.
func tomlRegisters(raw, exe string) (integrated, current bool) {
	in := false
	for _, line := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "[") {
			in = s == "[mcp_servers."+mcpServerName+"]"
			if in {
				integrated = true
			}
			continue
		}
		if !in || !strings.HasPrefix(s, "command") {
			continue
		}
		if i := strings.Index(s, "="); i >= 0 {
			v := strings.TrimSpace(s[i+1:])
			v = strings.Trim(v, `"'`)
			if samePath(v, exe) {
				current = true
			}
		}
	}
	return integrated, current
}

// extensionNames reports whether any file of the pi extension carries exe —
// Install bakes the installing reminal's path in, so a package written by a
// reminal that has since moved names a path that is not this one.
func extensionNames(dir, exe string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil && strings.Contains(string(raw), exe) {
			return true
		}
	}
	return false
}

// hooksInstalled is whether every event in the spec carries a reminal-tagged
// entry — the same marker applyHooks uses to find its own on a re-run.
func hooksInstalled(spec *hookSpec, home string) bool {
	path := filepath.Join(home, spec.file)
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	root := map[string]any{}
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	node := root
	for _, k := range spec.key {
		next, ok := node[k].(map[string]any)
		if !ok {
			return false
		}
		node = next
	}
	for _, ev := range spec.events {
		if ev.MinVersion != "" {
			continue // written only for an agent new enough: not needed to be set up
		}
		list, _ := node[ev.Event].([]any)
		found := false
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				if _, mine := m[hookMarker]; mine {
					found = true
					break
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// samePath is whether two executable paths name the same file, allowing for
// a symlink on either side (a /usr/local/bin/reminal that points into an app
// bundle is the same reminal).
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return ra == rb
}

func mtime(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.ModTime().Unix()
}

func tildePath(home, path string) string {
	if home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

// restartHint is the one line a user needs after a setup: an agent loads its
// MCP servers when it starts, so a running copy has to be quit and started
// again — with its resume option, or the conversation is gone.
func restartHint(t agentTarget) string {
	if t.resumeHint() == "" {
		return fmt.Sprintf("Quit %s and start it again so it loads the tools.", t.Name)
	}
	return fmt.Sprintf("Quit %s with its exit command, then run `%s` — a plain `%s` starts a new chat.", t.Name, t.resumeHint(), t.Bin)
}

// ---- When each agent was set up -------------------------------------------
//
// An agent's config file is a poor clock: Claude Code rewrites ~/.claude.json
// as it runs, so its mtime says nothing about when reminal registered there.
// integrate keeps its own note of the moment instead.

func setUpFile(home string) string { return filepath.Join(home, ".reminal", "integrated.json") }

func setUpAt(home string) map[string]int64 {
	out := map[string]int64{}
	raw, err := os.ReadFile(setUpFile(home))
	if err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// noteSetUp records (or forgets) that bin was set up by reminal just now.
func noteSetUp(home, bin string, on bool) {
	m := setUpAt(home)
	if on {
		m[bin] = time.Now().Unix()
	} else {
		delete(m, bin)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(setUpFile(home)), 0o700); err != nil {
		return
	}
	_ = atomicfile.Write(setUpFile(home), raw, 0o600)
}

// ---- PATH as the user's shell sees it -------------------------------------

// widenPATH extends this process's PATH with the user's login-shell PATH and
// the usual per-user install locations. integrate is also run by the daemon
// (from the Machines view), whose service PATH is the bare system one, where
// a `claude` under ~/.local/bin or /opt/homebrew/bin is "not installed".
func widenPATH(home string) {
	sep := string(os.PathListSeparator)
	have := map[string]bool{}
	var parts []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || have[p] {
			return
		}
		have[p] = true
		parts = append(parts, p)
	}
	// The login shell's PATH first: it is the order the user's own shell
	// resolves `claude` in, and this process is standing in for that shell.
	for _, p := range strings.Split(session.LoginEnv("PATH")["PATH"], sep) {
		add(p)
	}
	for _, p := range strings.Split(os.Getenv("PATH"), sep) {
		add(p)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{
			filepath.Join(home, ".local", "bin"), filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".antigravity", "bin"), filepath.Join(home, ".cursor", "bin"),
			"/opt/homebrew/bin", "/usr/local/bin",
		} {
			add(p)
		}
	}
	os.Setenv("PATH", strings.Join(parts, sep))
}

// agentVersionIfGated is the agent's version when some hook event of its
// needs one (hookEvent.MinVersion), else "": not asked for nothing.
func agentVersionIfGated(t agentTarget) string {
	for _, ev := range t.hooks.events {
		if ev.MinVersion != "" {
			return checkVersion(t.Bin)
		}
	}
	return ""
}

// checkVersion is how --check asks an agent its version (a test sets it).
var checkVersion = installedVersion
