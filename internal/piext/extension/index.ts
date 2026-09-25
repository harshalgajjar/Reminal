// reminal's pi extension.
//
// Two jobs, both about the same thing — knowing what your machines are doing
// while you are not looking at them:
//
//  1. It tells reminal what this pi is up to, so the session shows "working",
//     "needs you", or "done" on your phone without you opening it. pi has exact
//     lifecycle events, so reminal no longer has to guess from what's on screen.
//
//  2. It hands pi reminal's own tools — the session list, a transcript from
//     another machine, notes on a window — discovered from the reminal on this
//     machine, so pi gets whatever that install offers and nothing it doesn't.
//
// Neither job requires reminal to be running: outside a reminal session the
// reporting is skipped, and if reminal is missing altogether the extension
// quietly does less instead of failing pi's startup.

import { spawn } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { McpClient } from "./mcp.ts";

/**
 * Attention states reminal understands. "input" is the loud one — it is what
 * turns a session amber on the list and asks the user to come back.
 */
type Attn = "working" | "input" | "done";

/**
 * Rewrite an unchanged state after this long. reminal expires an agent's
 * reported state so a crashed agent cannot pin a session to "working" forever,
 * so a genuinely long run has to keep saying so.
 */
const REFRESH_MS = 5 * 60 * 1000;

export default function reminalExtension(pi: ExtensionAPI): void {
	const bin = resolveBin();

	// ---- 1. report what this session is doing -------------------------------

	// Outside a reminal session there is nothing to report to. `reminal hook` is
	// a silent no-op there anyway; skipping spares us a process per turn.
	const reporting = (process.env.REMINAL_SESSION ?? "") !== "";
	let last: { state: Attn; at: number } | undefined;

	// One `reminal hook` at a time, and only ever the newest state.
	//
	// Each report is a separate process racing to write the same file, and the
	// last writer wins. Two fired back to back — which is exactly what a fast
	// turn does, agent_start then agent_settled — land in whatever order the OS
	// schedules them. When they land backwards the session is left reading
	// "working" after it has finished, and stays that way until the state
	// expires: the stuck pill, from a turn that ended cleanly.
	//
	// So a write in flight parks the next one instead of racing it. Only the
	// newest parked state is kept, since an older one it superseded has nothing
	// left to say.
	let writing = false;
	let parked: Attn | undefined;

	const write = (state: Attn): void => {
		writing = true;
		const done = () => {
			writing = false;
			const next = parked;
			parked = undefined;
			if (next) write(next);
		};
		try {
			const p = spawn(bin, ["hook", state], { stdio: "ignore", detached: true });
			// A hook that fails is not worth interrupting anyone over, but it must
			// still release the queue behind it.
			p.on("error", done);
			p.on("exit", done);
			p.unref();
		} catch {
			// no reminal on PATH — the rest of the extension still works
			done();
		}
	};

	const report = (state: Attn): void => {
		if (!reporting) return;
		if (last && last.state === state && Date.now() - last.at < REFRESH_MS) return;
		last = { state, at: Date.now() };
		// Never block a lifecycle handler: the write happens on its own.
		if (writing) {
			parked = state;
			return;
		}
		write(state);
	};

	// A run is under way. agent_start covers the whole loop; turn_start and
	// turn_end keep a long one from aging out mid-work.
	pi.on("agent_start", () => report("working"));
	pi.on("turn_start", () => report("working"));
	pi.on("turn_end", () => report("working"));

	// agent_settled, not agent_end: it fires once the run is truly over, with no
	// retry, compaction, or queued follow-up still to come, and it fires even
	// when the run was interrupted or failed. That is exactly "your turn".
	pi.on("agent_settled", () => report("done"));

	// pi is about to ask whether you trust this directory, and will sit there
	// until you answer. That is the one moment pi genuinely needs you. Stay out
	// of the decision itself: "undecided" lets pi ask, as it would anyway.
	pi.on("project_trust", () => {
		report("input");
		return { trusted: "undecided" as const };
	});

	// ---- 2. offer reminal's tools to the model ------------------------------

	// Not in the factory: the tool registry isn't readable yet during extension
	// load, and offering a tool whose name pi already uses would silently shadow
	// a built-in. session_start is the first point where we can look before we
	// leap — and it still lands before the first turn, so the model sees the
	// tools from its very first message.
	let mcp: McpClient | undefined;
	let started = false;

	// What we have put in front of the model, and what pi had before we did.
	const ours = new Set<string>();
	let theirs = new Set<string>();

	/**
	 * Bring pi's tools into line with what reminal offers this session right now.
	 *
	 * Called at startup and again whenever reminal says its answer changed — it
	 * can, while pi is running: reminal upgrades itself in place and comes back
	 * with a different set, and what a session is entitled to is not fixed for
	 * the session's life either. Both directions matter. A tool that appeared
	 * should be usable without restarting pi, and a tool that went away has to
	 * stop being offered, because a tool the model can see is a tool it will try.
	 */
	async function syncTools(client: McpClient, ui: ExtensionContext["ui"]): Promise<void> {
		const offered = await client.list();
		const names = new Set(offered.map((t) => t.name));
		const shadowed: string[] = [];

		for (const tool of offered) {
			if (ours.has(tool.name)) continue; // already registered, and still offered
			if (theirs.has(tool.name)) {
				shadowed.push(tool.name);
				continue;
			}
			ours.add(tool.name);
			pi.registerTool({
				name: tool.name,
				label: label(tool.name),
				description: tool.description ?? `reminal: ${tool.name}`,
				// reminal's own schema, passed through untouched: it is already JSON
				// Schema, and rewriting it here could only lose detail.
				parameters: asSchema(tool.inputSchema),
				async execute(_toolCallId, params, signal) {
					const text = await client.call(tool.name, params, signal);
					return { content: [{ type: "text", text }], details: {} };
				},
			});
		}

		// pi has no way to unregister a tool, but it does not have to be active.
		// Dropping it from the active set is what the model sees, which is the
		// part that matters. Everything else stays exactly as the user left it.
		const withdrawn = [...ours].filter((name) => !names.has(name));
		if (withdrawn.length > 0) {
			for (const name of withdrawn) ours.delete(name);
			const gone = new Set(withdrawn);
			pi.setActiveTools(pi.getActiveTools().filter((name) => !gone.has(name)));
		}

		if (shadowed.length > 0) {
			ui.notify(`reminal tools pi already has by these names, left alone: ${shadowed.join(", ")}`, "warning");
		}
	}

	pi.on("session_start", async (_event, ctx: ExtensionContext) => {
		if (started) return; // one server per runtime, however many sessions it sees
		started = true;

		const client = new McpClient(bin);
		try {
			await client.start();
		} catch (e) {
			client.stop();
			// Missing or unhappy reminal: say so once, quietly, and carry on. pi is
			// useful without these tools, and throwing here would take pi with us.
			ctx.ui.notify(`reminal tools unavailable: ${(e as Error).message}`, "warning");
			return;
		}
		mcp = client;
		theirs = new Set(pi.getAllTools().map((t) => t.name));

		// One at a time, and never on top of a sync already running: the
		// announcements are not ours to pace, and two passes at once would race
		// over the same registry.
		let queue: Promise<void> = Promise.resolve();
		const sync = () => {
			queue = queue
				.then(() => syncTools(client, ctx.ui))
				.catch((e: Error) => ctx.ui.notify(`reminal tools: ${e.message}`, "warning"));
			return queue;
		};
		client.onToolsChanged = sync;
		await sync();
	});

	pi.on("session_shutdown", () => {
		// Quitting mid-run would otherwise leave "working" behind until it expired.
		report("done");
		mcp?.stop();
		mcp = undefined;
	});
}

/**
 * Find the reminal binary.
 *
 * `reminal integrate` writes bin.json next to this extension with the absolute
 * path of the install that set it up, so the extension keeps working when
 * reminal is not on PATH — the normal case for a fresh install in a shell that
 * started before it.
 */
function resolveBin(): string {
	const fromEnv = (process.env.REMINAL_BIN ?? "").trim();
	if (fromEnv !== "") return fromEnv;
	try {
		const here = path.dirname(fileURLToPath(import.meta.url));
		const parsed: unknown = JSON.parse(fs.readFileSync(path.join(here, "bin.json"), "utf8"));
		if (isObject(parsed) && typeof parsed.bin === "string" && parsed.bin.trim() !== "") {
			return parsed.bin.trim();
		}
	} catch {
		// not written, unreadable, or malformed — fall through to PATH
	}
	return "reminal";
}

/** "read_transcript" -> "Read Transcript", for the tool's line in pi's UI. */
function label(name: string): string {
	return name
		.split(/[_\-\s]+/)
		.filter((w) => w !== "")
		.map((w) => w.charAt(0).toUpperCase() + w.slice(1))
		.join(" ");
}

// pi types tool parameters as TypeBox, which at runtime is plain JSON Schema —
// what MCP hands us. The cast says "trust the server's schema"; the fallback
// covers a tool that takes no arguments.
function asSchema(inputSchema: unknown): never {
	const schema = isObject(inputSchema) ? inputSchema : { type: "object", properties: {} };
	return schema as never;
}

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}
