#!/usr/bin/env node
// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// A stand-in for the model behind Claude Code (the Anthropic Messages API),
// which streams a long story with headings when asked for one, a few words
// at a time, so Claude Code renders it as it comes, as a real answer does.
// Every sentence carries an id ("S3x07"), so a check can count what a
// viewer ended up with. Anything else is answered "noted: <what was said>".
import * as http from "node:http";

const PORT = Number(process.env.MODEL_PORT ?? 8099);
export const SECTIONS = 8, SENTENCES = 12;

// The story: SECTIONS sections of SENTENCES sentences, each sentence its id
// and a run of ordinary words, long enough that rows wrap on a phone.
const WORDS = "the lighthouse keeper kept the lamp lit through every storm and the island was never on a map because the surveyors brass instruments found nothing where the rock should have been".split(" ");
export function story() {
  const out = ["The Lighthouse Keeper's Last Map", ""];
  for (let s = 1; s <= SECTIONS; s++) {
    out.push(`## ${s}. Section ${s} of the story`, "");
    const para = [];
    for (let k = 1; k <= SENTENCES; k++) {
      const n = 9 + ((s * 7 + k * 3) % 9);
      const words = [];
      for (let i = 0; i < n; i++) words.push(WORDS[(s * 11 + k * 5 + i) % WORDS.length]);
      para.push(`S${s}x${String(k).padStart(2, "0")} ${words.join(" ")}.`);
      if (k % 4 === 0) { out.push(para.join(" "), ""); para.length = 0; }
    }
  }
  return out.join("\n");
}

function lastUserText(body) {
  const msgs = body.messages || [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (!m || m.role !== "user") continue;
    const c = typeof m.content === "string" ? [m.content] : (m.content || []).map(p => (typeof p === "string" ? p : p.type === "text" ? p.text : "")).filter(Boolean);
    const t = c.map(s => s.trim()).filter(s => s && !s.startsWith("<")).pop();
    if (t) return t;
  }
  return "";
}

const ev = (res, type, data) => res.write(`event: ${type}\ndata: ${JSON.stringify(data)}\n\n`);
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

async function answer(body, res) {
  const said = lastUserText(body);
  const text = /story/i.test(said) ? story() : (said ? `noted: ${said.slice(0, 80)}` : "noted.");
  const id = "msg_" + Date.now().toString(36);
  const usage = { input_tokens: 10, output_tokens: 10 };
  if (!body.stream) {
    res.writeHead(200, { "content-type": "application/json" });
    return res.end(JSON.stringify({ id, type: "message", role: "assistant", model: body.model, content: [{ type: "text", text }], stop_reason: "end_turn", stop_sequence: null, usage }));
  }
  res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache", connection: "keep-alive" });
  ev(res, "message_start", { type: "message_start", message: { id, type: "message", role: "assistant", model: body.model, content: [], stop_reason: null, stop_sequence: null, usage } });
  ev(res, "content_block_start", { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } });
  // A few words a delta, a short pause between: the story streams for
  // some seconds, long enough for a screen to change under it.
  const words = text.split(/(?<=\s)/);
  for (let i = 0; i < words.length; i += 5) {
    ev(res, "content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: words.slice(i, i + 5).join("") } });
    await sleep(Number(process.env.MODEL_DELTA_MS ?? 25));
  }
  ev(res, "content_block_stop", { type: "content_block_stop", index: 0 });
  ev(res, "message_delta", { type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 10 } });
  ev(res, "message_stop", { type: "message_stop" });
  res.end();
}

if (process.argv[1] && process.argv[1].endsWith("model.mjs")) {
  http.createServer((req, res) => {
    let b = "";
    req.on("data", (c) => (b += c));
    req.on("end", () => {
      let body = {};
      try { body = JSON.parse(b || "{}"); } catch (_) {}
      if (req.url.includes("count_tokens")) { res.writeHead(200, { "content-type": "application/json" }); return res.end(JSON.stringify({ input_tokens: 10 })); }
      if (!req.url.includes("/messages")) { res.writeHead(404); return res.end(); }
      answer(body, res).catch(() => { try { res.end(); } catch (_) {} });
    });
  }).listen(PORT, "127.0.0.1", () => console.log(`model on ${PORT}`));
}
