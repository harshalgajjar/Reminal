// A minimal MCP stdio client — just enough to talk to `reminal mcp`.
//
// We speak the protocol rather than shelling out to individual reminal
// subcommands for one reason: reminal decides which tools a session gets, and
// that answer is its to give, not ours to predict. A hard-coded list here would
// move the decision into this file and go stale the moment reminal's own answer
// changed — offering the model tools it cannot actually use. So we ask, and
// offer exactly what we are handed.
//
// And we keep listening. reminal's answer can change while pi is running — it
// announces that with the protocol's tools/list_changed notification — so this
// client surfaces those, and the extension re-asks rather than working from what
// it heard at startup.

import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";

export interface McpTool {
	name: string;
	description?: string;
	inputSchema?: unknown;
}

interface Pending {
	resolve: (value: unknown) => void;
	reject: (err: Error) => void;
	settle: () => void;
}

/** How long a tool call may take before we give up on it. */
const CALL_TIMEOUT_MS = 120_000;

/**
 * How long the handshake and a tool list may take. Short on purpose: pi's
 * startup waits on both, and neither does any real work.
 */
const ASK_TIMEOUT_MS = 5_000;

export class McpClient {
	private proc: ChildProcessWithoutNullStreams | null = null;
	private nextId = 1;
	private pending = new Map<number, Pending>();
	private buf = "";
	private closed = false;
	private readonly bin: string;

	/** Called when reminal says its tool list has changed. */
	onToolsChanged: (() => void) | undefined;

	constructor(bin: string) {
		this.bin = bin;
	}

	/**
	 * Start `reminal mcp` and shake hands. Ask for the tools separately, with
	 * list() — the answer can change later, so there is one path that reads it.
	 *
	 * Throws if reminal is missing or does not answer in time; the caller treats
	 * that as "no tools this session" and carries on, because the attention
	 * reporting is worth having on its own.
	 */
	async start(): Promise<void> {
		const proc = spawn(this.bin, ["mcp"], { stdio: ["pipe", "pipe", "pipe"] });
		this.proc = proc;

		proc.on("error", (e) => this.fail(`could not run ${this.bin}: ${e.message}`));
		// Fail every in-flight call rather than leaving the agent waiting on a
		// process that is already gone.
		proc.on("exit", (code) => this.fail(`${this.bin} mcp exited (code ${code ?? "unknown"})`));
		// The server's stderr is diagnostics, not protocol. Drain it so a chatty
		// server can never fill the pipe and wedge itself.
		proc.stderr.on("data", () => {});
		// setEncoding, not toString() per chunk: a character whose bytes straddle a
		// chunk boundary would be decoded as two replacement characters, and since
		// no continuation byte is ever ASCII the JSON still parses — so the damage
		// is silent, and it lands squarely on the box-drawing an agent's screen is
		// made of. The stream decoder holds the partial character instead.
		proc.stdout.setEncoding("utf8");
		proc.stdout.on("data", (chunk: string) => this.feed(chunk));

		await this.request("initialize", {
			protocolVersion: "2024-11-05",
			capabilities: {
				// We act on tools/list_changed, so say so: a server that checks
				// before announcing should know it is worth announcing to us.
				tools: { listChanged: true },
			},
			clientInfo: { name: "reminal-pi", version: "1" },
		}, { timeoutMs: ASK_TIMEOUT_MS });
		this.notify("notifications/initialized");
	}

	/** Ask which tools this session is offered, now. */
	async list(timeoutMs = ASK_TIMEOUT_MS): Promise<McpTool[]> {
		const listed = (await this.request("tools/list", {}, { timeoutMs })) as { tools?: McpTool[] } | undefined;
		return (listed?.tools ?? []).filter((t) => typeof t?.name === "string" && t.name !== "");
	}

	/**
	 * Call one tool and return its result blocks, kept apart.
	 *
	 * Not joined into one string: reminal sends a notice — a version skew, say —
	 * as its own block precisely so it is never inlined into result text that the
	 * model is meant to parse, and several of its tools answer in JSON. Gluing
	 * the two together turns a parseable answer into prose with a warning on the
	 * front.
	 */
	async call(name: string, args: unknown, signal?: AbortSignal): Promise<string[]> {
		const res = (await this.request("tools/call", { name, arguments: args ?? {} }, {
			timeoutMs: CALL_TIMEOUT_MS,
			signal,
		})) as { content?: Array<{ text?: string }>; isError?: boolean } | undefined;
		const texts = (res?.content ?? [])
			.map((c) => (typeof c?.text === "string" ? c.text : ""))
			.filter((t) => t !== "");
		if (res?.isError) throw new Error(texts.join("\n") || `${name} failed`);
		return texts;
	}

	stop(): void {
		this.closed = true;
		const p = this.proc;
		this.proc = null;
		try {
			p?.kill();
		} catch {
			// already gone
		}
	}

	// ---- transport ----------------------------------------------------------

	// MCP's stdio transport is newline-delimited JSON-RPC, so reassemble lines
	// across chunk boundaries before parsing.
	private feed(text: string): void {
		this.buf += text;
		let nl = this.buf.indexOf("\n");
		while (nl >= 0) {
			const line = this.buf.slice(0, nl).trim();
			this.buf = this.buf.slice(nl + 1);
			nl = this.buf.indexOf("\n");
			if (line === "") continue;
			let msg: { id?: number; method?: string; result?: unknown; error?: { message?: string } };
			try {
				msg = JSON.parse(line);
			} catch {
				continue; // not protocol — ignore rather than tear the session down
			}
			if (typeof msg.id !== "number") {
				// A notification. The one we care about says reminal's answer to
				// tools/list is no longer what we were told.
				if (msg.method === "notifications/tools/list_changed") this.onToolsChanged?.();
				continue;
			}
			const p = this.pending.get(msg.id);
			if (!p) continue;
			p.settle();
			if (msg.error) p.reject(new Error(msg.error.message ?? "mcp error"));
			else p.resolve(msg.result);
		}
	}

	private request(
		method: string,
		params: unknown,
		opts: { timeoutMs: number; signal?: AbortSignal },
	): Promise<unknown> {
		return new Promise((resolve, reject) => {
			if (this.closed || !this.proc) {
				reject(new Error("reminal mcp is not running"));
				return;
			}
			const id = this.nextId++;
			const timer = setTimeout(() => {
				settle();
				reject(new Error(`${method} timed out`));
			}, opts.timeoutMs);
			const onAbort = () => {
				settle();
				reject(new Error(`${method} cancelled`));
			};
			const settle = () => {
				this.pending.delete(id);
				clearTimeout(timer);
				opts.signal?.removeEventListener("abort", onAbort);
			};
			if (opts.signal?.aborted) {
				settle();
				reject(new Error(`${method} cancelled`));
				return;
			}
			opts.signal?.addEventListener("abort", onAbort, { once: true });
			this.pending.set(id, { resolve, reject, settle });
			this.write({ jsonrpc: "2.0", id, method, params });
		});
	}

	private notify(method: string): void {
		this.write({ jsonrpc: "2.0", method });
	}

	private write(msg: unknown): void {
		try {
			this.proc?.stdin.write(`${JSON.stringify(msg)}\n`);
		} catch (e) {
			this.fail(`writing to ${this.bin} mcp failed: ${(e as Error).message}`);
		}
	}

	private fail(message: string): void {
		this.closed = true;
		for (const [, p] of this.pending) {
			p.settle();
			p.reject(new Error(message));
		}
		this.pending.clear();
	}
}
