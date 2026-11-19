# Voice

Talk to the agent in a session, and hear it answer. Speech recognition and
the voice run in the browser, on the person's device. Only text leaves it.

## How it fits together

```
person ── mic ──> voice frame (web/voice, in the viewer) ── text ──> viewer ── speech_in ──> session
                        ^                                                                    │
                        └──────── speak {text} <── viewer <── speech <── speak MCP tool <── agent
```

- The viewer shows the frame `web/voice/voice.html` in a same-origin iframe
  and passes messages to and from it (`web/voice/bridge.js` lists them).
- What the person says reaches the session as a `speech_in` message and is
  typed in as `[voice] ...` then Return. Long talk is sent in parts as
  `[voice, continues] ...` so the agent can follow along.
- The agent answers with reminal's MCP `speak` tool
  (`cmd/reminal/mcp_speak.go`), which reaches the session over its control
  socket and goes to the viewers as a `speech` message.

### What the session will type

`internal/client/speech_gate.go` decides, from what is in front at that
moment: words are typed only into a known agent that is not showing a
prompt, in a terminal that is not hiding input. Never at a permission
prompt (a stray "1" and Return would approve it), into a shell (the line
would run), or at a password prompt. It checks before the words and again
before the Return. When it refuses, the viewer gets a notice and the frame
says why.

It needs a positive reason to believe the agent is at its input box: the
agent's own hook (`reminal integrate`) has reported since that program
started (a turn's state, or Claude Code's start-up "ready"), or the agent's
input box has a mark of its own that is on screen now (OpenCode, Kilo).
Without either, nothing is typed: an agent's dialogs (tool approvals, trust
and consent screens) come in wordings no list of prompts can keep up with.

## The frame's files (`internal/client/web/voice/`)

| File | What it does |
| --- | --- |
| `voice.html` | The panel's markup and styles. |
| `voice.js` | Entry point: loads the models, starts and stops, wires the panel and the viewer's messages to the conversation. |
| `conversation.js` | Turn-taking: when the person has finished, replies, talking over each other, held lines, cut-ins. |
| `speaker.js` | The agent's voice behind one interface: Kokoro, or the device's own speech synthesis. |
| `speakable.js` | Turns written text into something a voice can say (numbers, abbreviations, symbols). |
| `stt-worker.js` | Speech to text (Moonshine via transformers.js), in a worker. |
| `tts-worker.js`, `kokoro.js`, `g2p.js`, `cmudict.txt` | The natural voice: Kokoro-82M in a worker, with our own phonemizer from CMUdict. |
| `turn-model.js` | Whether the person has finished, from how they sounded (smart-turn v3.2). |
| `level.js` | Loudness for the viewer's mic button. |
| `panel.js` | Everything shown. Decides nothing. |
| `tuning.js` | Every timing and threshold, in one place, with what each is for. |
| `bridge.js` | postMessage to and from the viewer, and the message list. |
| `assets.js`, `integrity.js`, `hashes.js` | Where libraries and models come from, and the check every file passes before use. |
| `size.js` | Tells the viewer the panel's height before the libraries load. |
| `NOTICE` | Licences of what the frame loads. |

## Libraries and models

Everything the frame loads comes from `/voice-assets/` on its own origin, at
exact versions: an npm version, or a Hugging Face commit. There are no
other hosts in its Content-Security-Policy.

- The hosted viewer serves `/voice-assets/` from an R2 bucket
  (`cloudflare/src/voiceassets.ts`).
- A local relay passes those requests on to the hosted site
  (`internal/client/voice_assets.go`, or `REMINAL_VOICE_ASSETS`). This means
  a local relay makes outbound requests when voice is opened.
- `voice/assets/manifest.json` lists every file with its size and SHA-256.
  `voice/assets/mirror.mjs fetch OUT` downloads them and refuses any that
  do not match.

### Integrity

The frame shares the viewer's origin, so its downloaded models stay in
storage, but code running in it could reach the viewer. So nothing from
`/voice-assets/` runs or is used until its bytes match the SHA-256 in
`hashes.js`, which ships inside reminal (`integrity.js`):

- `guardFetch()` hands on only checked bytes for every fetch of an asset;
- modules, WebAssembly and the audio worklet are loaded from blob: URLs of
  checked bytes;
- an asset not in the list is answered as not found.

A file's last byte is held back until the whole file has ended at its pinned
size and matched, and any refusal stops voice and names the file.

What this protects against, exactly: whoever serves or can write
`/voice-assets/` (the bucket, and for a local relay the hosted site it asks)
can stop voice from working, but cannot run code in the frame or change
what it hears or says. It does not protect against a compromise of the
origin that serves the viewer and `hashes.js` themselves: that is the
viewer's own trust, unchanged by voice.

After changing the manifest, run `node voice/assets/hashes.mjs`; a Go test
keeps `hashes.js` and the manifest the same.

Moving the frame to an origin of its own is the next step for isolation.

## Licences

All permissive: Kokoro-82M, transformers.js and the Kokoro loading code
(Apache-2.0), Moonshine and ONNX Runtime (MIT), smart-turn v3 (BSD-2-Clause),
vad-web (ISC) and Silero VAD v5 (MIT), CMUdict (BSD). No GPL code or data.
See `web/voice/NOTICE`.

## Testing

- Go: `go test ./internal/client -run 'Speech|Speak|Voice'` and
  `go test ./internal/pty -run HidesInput`.
- Worker: `cloudflare/voiceassets_test.go`.
- In a browser: run a local relay and a session in a container (never on
  your own machine), open the viewer, and turn voice on. Chrome's fake
  microphone (`--use-file-for-fake-audio-capture`) plays a recording in
  place of the mic.
