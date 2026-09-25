#!/usr/bin/env node
// A stand-in for `reminal mcp`, so the extension can be tested against a tool
// list that changes underneath it.
//
// The real reminal changes its answer for reasons a test cannot stage on demand:
// it upgrades itself in place mid-session and comes back offering a different
// set, and what a session is entitled to is not fixed for the session's life
// either. Both end the same way — a tools/list_changed notification and a
// different answer to tools/list — which is exactly what this fakes.
//
// It offers whatever tool names are listed, one per line, in the file named by
// $FAKE_TOOLS. Rewrite that file and it announces the change, the way reminal
// does.

import * as fs from "node:fs";

const toolsFile = process.env.FAKE_TOOLS;

function currentNames() {
	try {
		return fs
			.readFileSync(toolsFile, "utf8")
			.split("\n")
			.map((s) => s.trim())
			.filter(Boolean);
	} catch {
		return [];
	}
}

// A marker the test can flip by rewriting the tools file: "!upgraded" changes
// what every tool says about itself, the way a reminal that updated in place
// would, and "!die" makes the server exit.
function toolsFor(names) {
	const upgraded = names.includes("!upgraded");
	return names
		.filter((n) => !n.startsWith("!"))
		.map((name) => ({
			name,
			description: upgraded ? `fake ${name} (upgraded)` : `fake ${name}`,
			inputSchema: {
				type: "object",
				properties: upgraded
					? { echo: { type: "string" }, added_later: { type: "string" } }
					: { echo: { type: "string" } },
			},
		}));
}

function send(msg) {
	process.stdout.write(`${JSON.stringify(msg)}\n`);
}

let announced = currentNames().join(",");
setInterval(() => {
	const now = currentNames().join(",");
	if (now === announced) return;
	announced = now;
	if (now.includes("!die")) {
		process.exit(7); // the server going away underneath the client
	}
	send({ jsonrpc: "2.0", method: "notifications/tools/list_changed" });
}, 50).unref();

let buf = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => {
	buf += chunk;
	let nl = buf.indexOf("\n");
	while (nl >= 0) {
		const line = buf.slice(0, nl).trim();
		buf = buf.slice(nl + 1);
		nl = buf.indexOf("\n");
		if (!line) continue;
		const msg = JSON.parse(line);
		if (typeof msg.id !== "number") continue; // a notification from the client
		switch (msg.method) {
			case "initialize":
				send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2024-11-05", capabilities: { tools: { listChanged: true } } } });
				break;
			case "tools/list":
				send({ jsonrpc: "2.0", id: msg.id, result: { tools: toolsFor(currentNames()) } });
				break;
			case "tools/call": {
				// Two blocks, and a big one made of the box-drawing an agent's screen
				// is full of: enough to cross pipe-chunk boundaries, so a decoder that
				// works a chunk at a time mangles it.
				const content = [{ type: "text", text: `${msg.params.name}: ${JSON.stringify(msg.params.arguments)}` }];
				if (msg.params.name === "big") {
					content.push({ type: "text", text: "─│✓⏺".repeat(40000) });
				}
				send({ jsonrpc: "2.0", id: msg.id, result: { content } });
				break;
			}
			default:
				send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: `no ${msg.method}` } });
		}
	}
});
process.stdin.resume();
