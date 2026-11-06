// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// A stand-in for the model Copilot CLI talks to through its own provider
// setting (COPILOT_PROVIDER_BASE_URL; chat completions, streamed). A message
// with "run a command" in it is answered with a shell command to run, which
// Copilot asks the person to approve; anything else, and the turn after a
// command ran, with a short reply.
//   PORT=8080 node model.mjs
import http from 'node:http';

let n = 0;
const lastUser = (msgs) => {
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role !== 'user') continue;
    const c = msgs[i].content;
    return typeof c === 'string' ? c : (c || []).map(p => p.text || '').join(' ');
  }
  return '';
};
http.createServer((req, res) => {
  let raw = '';
  req.on('data', d => { raw += d; });
  req.on('end', () => {
    if (req.method === 'GET') {
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({ object: 'list', data: [{ id: 'gpt-4.1', object: 'model' }] }));
    }
    let body = {};
    try { body = JSON.parse(raw || '{}'); } catch {}
    const msgs = body.messages || [];
    const afterTool = (msgs[msgs.length - 1] || {}).role === 'tool';
    const tool = !afterTool && lastUser(msgs).includes('run a command')
      ? { name: 'bash', arguments: { command: 'touch made-by-standin && rm -f made-by-standin', description: 'Create and remove a file' } }
      : null;
    const base = { id: `c${++n}`, object: 'chat.completion.chunk', created: Math.floor(Date.now() / 1000), model: body.model || 'gpt-4.1' };
    res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' });
    const send = (o) => res.write(`data: ${JSON.stringify({ ...base, ...o })}\n\n`);
    const delta = tool
      ? { role: 'assistant', tool_calls: [{ index: 0, id: `call_${n}`, type: 'function', function: { name: tool.name, arguments: JSON.stringify(tool.arguments) } }] }
      : { role: 'assistant', content: afterTool ? 'Done.' : 'Hi from the stand-in model.' };
    send({ choices: [{ index: 0, delta, finish_reason: null }] });
    send({ choices: [{ index: 0, delta: {}, finish_reason: tool ? 'tool_calls' : 'stop' }] });
    send({ choices: [], usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 } });
    res.end('data: [DONE]\n\n');
  });
}).listen(Number(process.env.PORT || 8080), '127.0.0.1');
