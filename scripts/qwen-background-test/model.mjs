// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// A stand-in for the model Qwen Code talks to (an OpenAI-compatible one;
// chat completions, streamed): the turn's first request starts what MONITORS
// says of monitors, beside a background shell when SHELL_TOO=1; every later
// request ends the turn with a message.
//   PORT=8081 MONITORS=1 SHELL_TOO=1 node model.mjs
import http from 'node:http';

const monitors = Number(process.env.MONITORS || 0);
const shell = process.env.SHELL_TOO === '1';
let n = 0;
http.createServer((req, res) => {
  let raw = '';
  req.on('data', d => { raw += d; });
  req.on('end', () => {
    if (req.method === 'GET') {
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end('{"object":"list","data":[{"id":"gpt-4.1","object":"model"}]}');
    }
    let body = {};
    try { body = JSON.parse(raw || '{}'); } catch {}
    // Its own helpers (memory, titles) ask with no tools: answered, not counted.
    const main = (body.tools || []).length > 0;
    if (main) n++;
    const calls = main && n === 1 ? [
      ...(shell ? [{ name: 'run_shell_command', arguments: { command: 'sleep 900', is_background: true, description: 'dev server' } }] : []),
      ...Array.from({ length: monitors }, (_, i) => ({ name: 'monitor', arguments: { command: `sleep ${901 + i}`, description: `watch ${i + 1}` } })),
    ].map((c, i) => ({ index: i, id: `call_${i}`, type: 'function', function: { name: c.name, arguments: JSON.stringify(c.arguments) } })) : [];
    const base = { id: `c${n}`, object: 'chat.completion.chunk', created: Math.floor(Date.now() / 1000), model: 'gpt-4.1' };
    const usage = { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 };
    if (!body.stream) {
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({ ...base, object: 'chat.completion', choices: [{ index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' }], usage }));
    }
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    const send = (o) => res.write(`data: ${JSON.stringify({ ...base, ...o })}\n\n`);
    send({ choices: [{ index: 0, delta: calls.length ? { role: 'assistant', tool_calls: calls } : { role: 'assistant', content: 'Started them in the background.' }, finish_reason: null }] });
    send({ choices: [{ index: 0, delta: {}, finish_reason: calls.length ? 'tool_calls' : 'stop' }] });
    send({ choices: [], usage });
    res.end('data: [DONE]\n\n');
  });
}).listen(Number(process.env.PORT || 8080), '127.0.0.1');
