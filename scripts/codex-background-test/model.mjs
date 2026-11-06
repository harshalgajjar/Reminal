// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// A stand-in for the model Codex talks to (its Responses API, streamed): the
// turn's first request has it run CALLS long commands at once through
// exec_command, which Codex leaves running in the background once they
// outlast yield_time_ms; every later request ends the turn with a message.
//   PORT=8081 CALLS=2 node model.mjs
import http from 'node:http';

const calls = Number(process.env.CALLS || 0);
let n = 0;
const send = (res, items, id) => {
  res.writeHead(200, { 'content-type': 'text/event-stream' });
  const usage = { input_tokens: 10, input_tokens_details: null, output_tokens: 5, output_tokens_details: null, total_tokens: 15 };
  const events = [{ type: 'response.created', response: { id } },
    ...items.map(item => ({ type: 'response.output_item.done', item })),
    { type: 'response.completed', response: { id, usage } }];
  for (const e of events) res.write(`event: ${e.type}\ndata: ${JSON.stringify(e)}\n\n`);
  res.end();
};
http.createServer((req, res) => {
  req.resume();
  req.on('end', () => {
    if (!req.url.includes('/responses')) {
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end('{"data":[],"models":[]}');
    }
    n++;
    const items = n === 1
      ? Array.from({ length: calls }, (_, i) => ({ type: 'function_call', name: 'exec_command', call_id: `call_${i}`,
          arguments: JSON.stringify({ cmd: `sleep ${901 + i}`, yield_time_ms: 1000 }) }))
      : [{ type: 'message', role: 'assistant', id: `msg_${n}`, content: [{ type: 'output_text', text: 'Started it in the background.' }] }];
    send(res, items, `resp_${n}`);
  });
}).listen(Number(process.env.PORT || 8080), '127.0.0.1');
