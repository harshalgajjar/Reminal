#!/usr/bin/env node
// A local OpenAI-compatible endpoint, so a real pi turn can run in the rig.
//
// The attention half of reminal's pi extension only reports anything when pi
// actually runs — agent_start, turn_start, turn_end, agent_settled. Those never
// fire for a pi with no model, which is the state every credential-free test
// leaves it in. So the rig brings its own model. It is not pretending to be an
// LLM: it says one sentence in the shape openai-completions expects, and it can
// be told to take its time.

import * as http from "node:http";

const PORT = Number(process.env.FAKE_MODEL_PORT ?? 8099);

/** How long the answer takes to stream once it starts. */
const STREAM_MS = Number(process.env.FAKE_MODEL_STREAM_MS ?? 6000);

/**
 * How long to say nothing at all first.
 *
 * This is the setting that makes the test mean something. While the model
 * stalls, pi's screen does not move, so the screen detector has nothing to go on
 * and would read the session as finished. Anything still reporting "working"
 * through the stall can only have come from the agent's own hook.
 */
const DELAY_MS = Number(process.env.FAKE_MODEL_DELAY_MS ?? 0);

const WORDS = ["Working", "on", "it,", "then", "done."];

/**
 * A tool to call before answering, when asked to.
 *
 * Set FAKE_MODEL_TOOL to a tool name and the first reply is a tool call for it
 * rather than prose. That is the only way to test the last link in the chain —
 * a model choosing one of reminal's tools, pi executing it, and the extension
 * proxying it through to reminal — which registration alone does not prove.
 */
const TOOL = process.env.FAKE_MODEL_TOOL ?? "";

function sse(res, payload) {
	res.write(`data: ${JSON.stringify(payload)}\n\n`);
}

function answerWithToolCall(res) {
	res.writeHead(200, {
		"content-type": "text/event-stream",
		"cache-control": "no-cache",
		connection: "keep-alive",
	});
	const base = {
		id: `chatcmpl-${Date.now()}`,
		object: "chat.completion.chunk",
		created: Math.floor(Date.now() / 1000),
		model: "fake-1",
	};
	const call = { index: 0, id: "call_1", type: "function", function: { name: TOOL, arguments: "" } };
	sse(res, { ...base, choices: [{ index: 0, delta: { role: "assistant", tool_calls: [call] }, finish_reason: null }] });
	sse(res, {
		...base,
		choices: [{ index: 0, delta: { tool_calls: [{ index: 0, function: { arguments: "{}" } }] }, finish_reason: null }],
	});
	sse(res, { ...base, choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }] });
	res.write("data: [DONE]\n\n");
	res.end();
}

function answer(res) {
	res.writeHead(200, {
		"content-type": "text/event-stream",
		"cache-control": "no-cache",
		connection: "keep-alive",
	});
	const base = {
		id: `chatcmpl-${Date.now()}`,
		object: "chat.completion.chunk",
		created: Math.floor(Date.now() / 1000),
		model: "fake-1",
	};
	sse(res, { ...base, choices: [{ index: 0, delta: { role: "assistant" }, finish_reason: null }] });

	const gap = Math.max(1, Math.floor(STREAM_MS / WORDS.length));
	let i = 0;
	const tick = setInterval(() => {
		if (i < WORDS.length) {
			const content = `${i ? " " : ""}${WORDS[i]}`;
			sse(res, { ...base, choices: [{ index: 0, delta: { content }, finish_reason: null }] });
			i++;
			return;
		}
		clearInterval(tick);
		sse(res, { ...base, choices: [{ index: 0, delta: {}, finish_reason: "stop" }] });
		sse(res, {
			...base,
			choices: [],
			usage: { prompt_tokens: 10, completion_tokens: WORDS.length, total_tokens: 10 + WORDS.length },
		});
		res.write("data: [DONE]\n\n");
		res.end();
	}, gap);
}

http
	.createServer((req, res) => {
		if (req.method === "GET" && req.url.startsWith("/v1/models")) {
			res.writeHead(200, { "content-type": "application/json" });
			res.end(JSON.stringify({ data: [{ id: "fake-1", name: "Fake 1" }] }));
			return;
		}
		if (!(req.method === "POST" && req.url.startsWith("/v1/chat/completions"))) {
			res.writeHead(404).end();
			return;
		}
		let body = "";
		req.on("data", (c) => {
			body += c;
		});
		req.on("end", () => {
			// Call the tool once, then answer for real: a request that already
			// carries a tool result is the second turn.
			const asked = TOOL !== "" && !body.includes('"tool"');
			// The stall happens before any headers go out, so pi is waiting on the
			// request itself and paints nothing at all.
			setTimeout(() => (asked ? answerWithToolCall(res) : answer(res)), DELAY_MS);
		});
	})
	.listen(PORT, "127.0.0.1", () => {
		process.stdout.write(`fake model on http://127.0.0.1:${PORT}/v1\n`);
	});
