// A stand-in for pi, so the extension can be exercised against a real reminal
// without starting pi or spending a model call. Two halves, matching the
// extension's two jobs: the tools it offers pi, and the attention state it
// reports back to reminal.
//
// Run: REMINAL_BIN=/path/to/reminal node --experimental-strip-types test/harness.ts
// (or any TypeScript runner; it needs nothing but node's own modules).

import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import reminalExtension from "../index.ts";

type Handler = (event: unknown, ctx: unknown) => unknown;

/** One fake pi: collects what the extension subscribes to and registers. */
function fakePi() {
	const handlers = new Map<string, Handler[]>();
	const tools = new Map<string, { label: string; description: string; parameters: unknown; execute: Function }>();
	const notices: string[] = [];
	const ctx = { ui: { notify: (m: string) => notices.push(m) } };
	// pi activates a newly registered tool, and an extension may narrow the set.
	let active = [...BUILT_INS];
	const pi = {
		on(event: string, handler: Handler) {
			handlers.set(event, [...(handlers.get(event) ?? []), handler]);
		},
		registerTool(tool: any) {
			tools.set(tool.name, tool);
			if (!active.includes(tool.name)) active.push(tool.name);
		},
		// pi's built-ins — the names an extension must not shadow.
		getAllTools: () => BUILT_INS.map((name) => ({ name })),
		getActiveTools: () => [...active],
		setActiveTools: (names: string[]) => {
			active = [...names];
		},
	} as unknown as ExtensionAPI;
	const emit = async (event: string, payload: unknown = {}) => {
		const out = [];
		for (const h of handlers.get(event) ?? []) out.push(await h(payload, ctx));
		return out;
	};
	return { pi, handlers, tools, notices, ctx, emit, activeTools: () => [...active] };
}

const BUILT_INS = ["read", "bash", "edit", "write", "grep", "find", "ls"];

// ---------------------------------------------------------------------------
// 1. the tools pi is offered
// ---------------------------------------------------------------------------

delete process.env.REMINAL_SESSION; // not inside a session: nothing to report
const a = fakePi();
reminalExtension(a.pi);

for (const event of ["agent_start", "turn_start", "turn_end", "agent_settled", "project_trust", "session_start", "session_shutdown"]) {
	assert.ok(a.handlers.has(event), `no handler for ${event}`);
}

// The trust prompt must never decide for the user.
assert.deepEqual(await a.emit("project_trust"), [{ trusted: "undecided" }]);

await a.emit("session_start", { type: "session_start", reason: "startup" });
assert.equal(a.notices.length, 0, `startup complained: ${a.notices.join("; ")}`);
assert.ok(a.tools.size > 0, "no reminal tools were offered");
console.log(`offered ${a.tools.size} tools: ${[...a.tools.keys()].sort().join(", ")}`);

// A second session must not start a second server or re-offer anything.
const offeredOnce = a.tools.size;
await a.emit("session_start", { type: "session_start", reason: "resume" });
assert.equal(a.tools.size, offeredOnce, "re-offered tools on the next session");

for (const name of BUILT_INS) assert.ok(!a.tools.has(name), `shadowed pi's ${name}`);

// Every tool needs a schema and labels pi can put in front of a model.
for (const [name, tool] of a.tools) {
	assert.equal((tool.parameters as Record<string, unknown>)?.type, "object", `${name} has no object schema`);
	assert.ok(tool.description.length > 0, `${name} has no description`);
	assert.ok(tool.label.length > 0, `${name} has no label`);
}

// A real round trip through the proxy: list_sessions takes no arguments and
// answers even with nothing running.
const listed = await a.tools.get("list_sessions")!.execute("call-1", {}, undefined, undefined, a.ctx);
assert.ok(Array.isArray(listed.content), "tool result has no content array");
assert.equal(listed.content[0]?.type, "text");
console.log("list_sessions ->", String(listed.content[0].text).slice(0, 100).replace(/\n/g, " ⏎ "));

// A cancelled call comes back promptly instead of hanging the turn.
await assert.rejects(
	() => a.tools.get("list_sessions")!.execute("call-2", {}, AbortSignal.abort(), undefined, a.ctx),
	/cancelled/,
);

// A bad argument surfaces as a tool error, not a crash.
await assert.rejects(
	() => a.tools.get("read_transcript")!.execute("call-3", { session: "NOPE99" }, undefined, undefined, a.ctx),
	(e: Error) => e.message.length > 0,
);

await a.emit("session_shutdown", { type: "session_shutdown", reason: "quit" });

// ---------------------------------------------------------------------------
// 2. the attention state reminal is told
// ---------------------------------------------------------------------------

// A session id of our own, so this never disturbs a real one.
const id = `PITEST${process.pid}`;
const statePath = path.join(os.homedir(), ".reminal", `hook-${id}.state`);
fs.rmSync(statePath, { force: true });
process.env.REMINAL_SESSION = id;

const b = fakePi();
reminalExtension(b.pi);

/** What reminal would read for this session, once the hook process lands. */
async function reported(): Promise<string | undefined> {
	for (let i = 0; i < 100; i++) {
		try {
			const { state, ts } = JSON.parse(fs.readFileSync(statePath, "utf8"));
			// Only trust a write from this moment, not a leftover from the last step.
			if (Date.now() - Date.parse(ts) < 2000) return state;
		} catch {
			// not written yet
		}
		await new Promise((r) => setTimeout(r, 50));
	}
	return undefined;
}

for (const [event, want] of [
	["agent_start", "working"],
	["agent_settled", "done"],
	["project_trust", "input"],
	["turn_start", "working"],
	["session_shutdown", "done"],
] as const) {
	fs.rmSync(statePath, { force: true });
	await b.emit(event, { type: event });
	assert.equal(await reported(), want, `${event} should report ${want}`);
}

// Repeating a state must not spawn a process per turn. The state is "working"
// again after this turn_start; the turn_end behind it has nothing new to say.
fs.rmSync(statePath, { force: true });
await b.emit("turn_start", { type: "turn_start" });
assert.equal(await reported(), "working");
fs.rmSync(statePath, { force: true });
await b.emit("turn_end", { type: "turn_end" });
await new Promise((r) => setTimeout(r, 300));
assert.equal(fs.existsSync(statePath), false, "rewrote an unchanged state");

fs.rmSync(statePath, { force: true });
delete process.env.REMINAL_SESSION;

// ---------------------------------------------------------------------------
// 3. keeping up when reminal's answer changes
// ---------------------------------------------------------------------------

// Against a stand-in for `reminal mcp` whose tool list this test controls. The
// real thing changes its answer for reasons a test cannot stage: it upgrades
// itself in place mid-session, and what a session is entitled to is not fixed
// for the session's life. Both arrive as tools/list_changed.
// Copied out to be made executable: the extension spawns reminal as a program,
// and this checkout may well be read-only (the container rig mounts it that way).
const fakeBin = path.join(os.tmpdir(), `reminal-pi-fake-${process.pid}.mjs`);
const toolsFile = path.join(os.tmpdir(), `reminal-pi-faketools-${process.pid}`);
fs.copyFileSync(path.join(import.meta.dirname, "fake-reminal.mjs"), fakeBin);
fs.chmodSync(fakeBin, 0o755);
fs.writeFileSync(toolsFile, "always_here\n");
process.env.REMINAL_BIN = fakeBin;
process.env.FAKE_TOOLS = toolsFile;

const c = fakePi();
reminalExtension(c.pi);
await c.emit("session_start", { type: "session_start", reason: "startup" });
assert.deepEqual([...c.tools.keys()], ["always_here"]);

/** Rewrite what the server offers, then wait for the extension to catch up. */
async function offer(names: string[], settled: () => boolean, what: string) {
	fs.writeFileSync(toolsFile, `${names.join("\n")}\n`);
	for (let i = 0; i < 100; i++) {
		if (settled()) return;
		await new Promise((r) => setTimeout(r, 50));
	}
	throw new Error(`${what}: never happened. offered to the model: ${c.activeTools()}`);
}

// A tool that appears has to become usable without restarting pi — and active,
// or the model is never offered it.
await offer(["always_here", "appeared"], () => c.activeTools().includes("appeared"), "a new tool should be offered");
const echoed = await c.tools.get("appeared")!.execute("call-4", { echo: "hi" }, undefined, undefined, c.ctx);
assert.match(echoed.content[0].text, /appeared: \{"echo":"hi"\}/);

// A tool that goes away has to stop being offered: a tool the model can see is
// a tool it will try. pi's own tools, and the one still on offer, stay put.
await offer(["always_here"], () => !c.activeTools().includes("appeared"), "a withdrawn tool should stop being offered");
assert.ok(c.activeTools().includes("always_here"), "dropped a tool that is still on offer");
for (const name of BUILT_INS) {
	assert.ok(c.activeTools().includes(name), `dropped pi's own ${name}`);
}
assert.deepEqual(c.notices, [], `complained: ${c.notices.join("; ")}`);

await c.emit("session_shutdown", { type: "session_shutdown", reason: "quit" });
fs.rmSync(toolsFile, { force: true });
fs.rmSync(fakeBin, { force: true });

console.log("ok");
