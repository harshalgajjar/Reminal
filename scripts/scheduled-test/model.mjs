// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// A stand-in for the models Claude Code and Qwen Code talk to: Anthropic's
// Messages API (/v1/messages) and an OpenAI-compatible one
// (/v1/chat/completions), both streamed. A message asking to "schedule"
// sets two jobs to run later: Claude Code loads its CronCreate through
// ToolSearch first, Qwen Code calls cron_create (which it runs though it
// does not list it). Anything else, and every
// turn after, ends with a short reply.
//   PORT=8080 node model.mjs
import http from 'node:http';

const JOBS = [
  { cron: '*/5 * * * *', prompt: 'check the build', recurring: true },
  { cron: '7 15 * * *', prompt: 'remind me', recurring: false },
];
// The last message a person sent (a tool's result is not one), and where.
const lastUser = (msgs) => {
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role !== 'user') continue;
    const c = msgs[i].content;
    if (typeof c === 'string') return { text: c, at: i };
    if ((c || []).some(p => p.type === 'tool_result')) continue;
    return { text: (c || []).map(p => p.text || '').join(' '), at: i };
  }
  return { text: '', at: -1 };
};
const sse = (res, events, named) => {
  res.writeHead(200, { 'content-type': 'text/event-stream' });
  for (const e of events) res.write(named ? `event: ${e.type}\ndata: ${JSON.stringify(e)}\n\n` : `data: ${JSON.stringify(e)}\n\n`);
  if (!named) res.write('data: [DONE]\n\n');
  res.end();
};
// Claude Code: a turn of tool calls, or of text.
function anthropic(body, res) {
  const tools = (body.tools || []).map(t => t.name);
  const msgs = body.messages || [];
  const said = lastUser(msgs);
  // Asked, and its CronCreate not called since.
  const done = msgs.slice(said.at + 1).some(m => m.role === 'assistant' && (m.content || []).some?.(p => p.type === 'tool_use' && p.name === 'CronCreate'));
  const asked = /schedule/.test(said.text) && !done;
  const calls = !asked ? [] : tools.includes('CronCreate')
    ? JOBS.map(j => ({ name: 'CronCreate', input: j }))
    : tools.includes('ToolSearch') ? [{ name: 'ToolSearch', input: { query: 'select:CronCreate', max_results: 1 } }] : [];
  const id = `msg_${Date.now()}`;
  const events = [{ type: 'message_start', message: { id, type: 'message', role: 'assistant', model: body.model || 'claude', content: [], stop_reason: null, usage: { input_tokens: 10, output_tokens: 1 } } }];
  if (calls.length) {
    calls.forEach((c, i) => events.push(
      { type: 'content_block_start', index: i, content_block: { type: 'tool_use', id: `toolu_${i}_${id}`, name: c.name, input: {} } },
      { type: 'content_block_delta', index: i, delta: { type: 'input_json_delta', partial_json: JSON.stringify(c.input) } },
      { type: 'content_block_stop', index: i }));
  } else {
    events.push({ type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } },
      { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'Done.' } },
      { type: 'content_block_stop', index: 0 });
  }
  events.push({ type: 'message_delta', delta: { stop_reason: calls.length ? 'tool_use' : 'end_turn', stop_sequence: null }, usage: { output_tokens: 5 } }, { type: 'message_stop' });
  if (body.stream) return sse(res, events, true);
  res.writeHead(200, { 'content-type': 'application/json' });
  res.end(JSON.stringify({ id, type: 'message', role: 'assistant', model: body.model || 'claude', content: [{ type: 'text', text: 'Done.' }], stop_reason: 'end_turn', usage: { input_tokens: 10, output_tokens: 5 } }));
}
// Qwen Code: a turn of tool calls, or of text.
function openai(body, res) {
  const msgs = body.messages || [];
  const asked = (msgs[msgs.length - 1] || {}).role === 'user' && /schedule/.test(lastUser(msgs).text);
  // cron_create is not among the tools it lists, and it runs the call all
  // the same (Qwen Code 0.26.9).
  const calls = asked && (body.tools || []).length > 0
    ? JOBS.map((j, i) => ({ index: i, id: `call_${i}`, type: 'function', function: { name: 'cron_create', arguments: JSON.stringify(j) } })) : [];
  const base = { id: `c${Date.now()}`, object: 'chat.completion.chunk', created: Math.floor(Date.now() / 1000), model: 'gpt-4.1' };
  if (!body.stream) {
    res.writeHead(200, { 'content-type': 'application/json' });
    return res.end(JSON.stringify({ ...base, object: 'chat.completion', choices: [{ index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } }));
  }
  sse(res, [
    { ...base, choices: [{ index: 0, delta: calls.length ? { role: 'assistant', tool_calls: calls } : { role: 'assistant', content: 'Done.' }, finish_reason: null }] },
    { ...base, choices: [{ index: 0, delta: {}, finish_reason: calls.length ? 'tool_calls' : 'stop' }] },
    { ...base, choices: [], usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 } },
  ], false);
}
http.createServer((req, res) => {
  let raw = '';
  req.on('data', d => { raw += d; });
  req.on('end', () => {
    let body = {};
    try { body = JSON.parse(raw || '{}'); } catch {}
    if (req.url.startsWith('/v1/messages/count_tokens')) { res.writeHead(200, { 'content-type': 'application/json' }); return res.end('{"input_tokens":10}'); }
    if (req.url.startsWith('/v1/messages')) return anthropic(body, res);
    if (req.url.startsWith('/v1/chat/completions')) return openai(body, res);
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end('{"object":"list","data":[{"id":"gpt-4.1","object":"model"}]}');
  });
}).listen(Number(process.env.PORT || 8080), '127.0.0.1');
