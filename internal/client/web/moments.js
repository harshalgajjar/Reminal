  // ── Moments: share cards made from what this viewer already saw ─────────
  // Receipts for an agent's run, a sped-up clip of it, memes from its lines,
  // a framed shot of the screen, and a weekly recap. Everything is drawn here,
  // in the browser, from the session's own output; nothing is sent anywhere
  // until the person shares or saves it. Every card goes through scrub() first:
  // secrets, tokens and keys, e-mail and IP addresses, home folders and the
  // user names seen in them or in prompts, this machine's name, and the
  // session's id and PIN are hidden.
  //
  // app.js feeds it: reminalMoments.output (every chunk of the session's
  // output), .attn (the session's attention state as the agent pushes it) and
  // .input (keys sent). It reads app.js's term, sessionId and lastHostInfo.
  (function () {
    'use strict';

    // ---------------------------------------------------------------- scrub
    function escRe(s) { return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); }
    function hostName() {
      try { return (n => n.length >= 3 ? n : '')((lastHostInfo && lastHostInfo.hostname || '').replace(/\.local$/, '')); } catch (_) { return ''; }
    }
    // scrub hides what must not leave the device. keep: the result is exactly as
    // long as the input, for text laid out in a terminal grid.
    // Names of people on this machine, learned from home folders and prompts
    // seen in the output, so "harshal@laptop ~ %" hides the name as well.
    const users = new Set();
    const NOT_USERS = /^(shared|public|guest|default|root|runner|admin|user|users|home|me)$/i;
    function learnUser(n) { if (n && n.length >= 2 && n.length <= 32 && !NOT_USERS.test(n) && users.size < 20) users.add(n); }
    // linkSpans are where s has links (a scheme, ://, up to the next space),
    // found in one pass; inLinks(spans) is a test for an index into s, for
    // indexes asked in increasing order (a replace's matches), so the whole
    // scrub stays one pass over s.
    function linkSpans(s) {
      const out = [];
      for (const m of s.matchAll(/[a-z][\w+.-]*:\/\/\S+/gi)) out.push([m.index, m.index + m[0].length]);
      return out;
    }
    function inLinks(spans) {
      let i = 0;
      return at => {
        while (i < spans.length && spans[i][1] <= at) i++;
        return i < spans.length && spans[i][0] <= at;
      };
    }
    // keySegment: one part of a path that is a key by itself (a webhook's
    // secret): long, mixed case, and digits or longer still.
    const keySegment = seg => seg.length >= 16 && /[A-Z]/.test(seg) && /[a-z]/.test(seg) && (/\d/.test(seg) || seg.length >= 20);
    function scrub(s, keep) {
      if (!s) return s;
      const mask = m => keep ? '•'.repeat(m.length) : '•••';
      s = s.replace(/\b(sk-ant-[\w-]{8,}|sk-[A-Za-z0-9_-]{10,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_\w{20,}|xox[abprs]-[\w-]{10,}|AKIA[0-9A-Z]{16}|AIza[\w-]{30,}|eyJ[\w-]{8,}\.[\w-]{8,}\.[\w-]{8,})/g, mask);
      // A bearer token, and the value after any name that sounds like a secret,
      // in any case and quoting: API_KEY=…, "apiKey": "…", --password=…
      s = s.replace(/\b(Bearer|Basic|Token)(\s+)([\w.~+\/=-]{6,})/gi, (m, k, sp, v) => k + sp + mask(v));
      s = s.replace(/\b([\w.-]{0,40}?(?:api[_-]?key|access[_-]?key|private[_-]?key|secret|token|passw(?:or)?d|passphrase|pwd|pass|credentials?|auth(?:orization)?|key))(["']?\s*[=:]\s*["']?)([^\s"',;]+)/gi, (m, k, eq, v) => k + eq + mask(v));
      // Base64 (a PEM body, a key file): mixed case and digits, / and + inside.
      // With a / it must be long, so a path like owner/Repo/pull/445 stays, and
      // never part of a link (https://github.com/owner/Repo/releases/tag/v3):
      // a key in a link's query is the key=value rule's.
      // Both this and the e-mail rule start only at the start of a run (the
      // character before is taken as `pre`): tried at every place inside a long
      // run, a hex dump, they would take seconds. Not a lookbehind, which an
      // older Safari can't parse at all.
      const inLink = inLinks(linkSpans(s));
      s = s.replace(/(^|[^A-Za-z0-9+\/])((?=[A-Za-z0-9+\/]*\d)(?=[A-Za-z0-9+\/]*[A-Z])(?=[A-Za-z0-9+\/]*[a-z])[A-Za-z0-9+\/]{24,}={0,2})/g, (m, pre, v, at) => {
        if (!v.includes('/')) return pre + mask(v);
        if (v.length >= 40 && !inLink(at)) return pre + mask(v);
        // A path (short, or in a link): it stays, but a part of it that is a key
        // by itself is hidden.
        return pre + v.split('/').map(seg => keySegment(seg) ? mask(seg) : seg).join('/');
      });
      s = s.replace(/\b(?=[\w-]*\d)(?=[\w-]*[A-Za-z])[\w-]{32,}\b/g, mask);
      s = s.replace(/(^|[^\w.+-])([\w.+-]+@[\w-]+\.[\w.-]+)/g, (m, pre, v) => pre + mask(v));
      // A password given after a space: --password x, sshpass -p x, curl -u user:x.
      s = s.replace(/(--(?:password|passwd|pass|token|api-key|apikey|secret)\s+|\bsshpass\s+-p\s*|\s-u\s+[^\s:]+:)([^\s"']+)/gi, (m, k, v) => k + mask(v));
      // A prompt's user@host: the user is a name. The host is followed by a
      // prompt's ":", "~", "$"..., so a package spec (react@latest my-app) isn't one.
      s = s.replace(/(^|[\s(\[])([a-z_][\w.-]{1,31})@(?=[\w-]+(?::|\s*[~$%#>\/]|\s+[\w.-]+\]))/gi, (m, pre, u) => { learnUser(u); return pre + (keep ? '•'.repeat(u.length) : 'me') + '@'; });
      s = s.replace(/\b(?:\d{1,3}\.){3}\d{1,3}\b/g, m => m === '127.0.0.1' ? m : mask(m));
      // IPv6 and MAC addresses: hex groups between colons, with a letter in them
      // (so a clock like 12:30:45 stays).
      s = s.replace(/(^|[^\w:])((?=[0-9a-f:]*[a-f])(?:[0-9a-f]{0,4}:){2,7}[0-9a-f]{1,4})(?![\w:])/gi, (m, pre, v) => pre + mask(v));
      s = s.replace(/#p=[^\s'"]+/g, mask);
      // The session's PIN: after "PIN", after "reminal connect <id>", or as itself.
      s = s.replace(/\b(PIN\b\s*[:#=]?\s*)(\d{6,8})\b/gi, (m, k, v) => k + mask(v));
      s = s.replace(/(reminal\s+connect\s+\S+\s+)(\d{6,8})\b/gi, (m, k, v) => k + mask(v));
      s = s.replace(/(\/Users\/|\/home\/|[A-Za-z]:\\Users\\)([^\/\\\s'"]+)/g, (m, pre, name) => { learnUser(name); return keep ? pre + '•'.repeat(name.length) : '~'; });
      const words = [...users];
      const host = hostName();
      if (host) words.push(host);
      try { if (sessionId) words.push(sessionId); } catch (_) {}
      try { const pin = typeof sharePin === 'function' && sharePin(); if (pin) words.push(String(pin)); } catch (_) {}
      // Whole words only: a user called "dev" leaves "development" alone.
      for (const w of words) {
        s = s.replace(new RegExp('(^|[^\\w])(' + escRe(w) + ')(?![\\w])', 'gi'), (m, pre, v) => pre + (keep ? '•'.repeat(v.length) : (w === host ? 'my machine' : '•••')));
      }
      return s;
    }

    // ------------------------------------------------------------- recorder
    const LOG_BYTES = 6 * 1024 * 1024;   // per session, the newest output kept for clips
    const LOG_AGE = 90 * 60 * 1000;
    const RUNS_KEY = 'reminal:moments:runs';
    const sessions = new Map();
    const COARSE = window.matchMedia && matchMedia('(pointer: coarse)').matches;

    // The scrollback a viewer is sent on connecting is old output, not new: lines
    // in it are never offered as moments, and a clip "since you connected" starts
    // after it. A connect (the page's body gains .connected) opens the window.
    let replayUntil = 0;
    const REPLAY_MS = 4000;
    try {
      let was = document.body.classList.contains('connected');
      new MutationObserver(() => {
        const now = document.body.classList.contains('connected');
        if (now && !was) replayUntil = Date.now() + REPLAY_MS;
        was = now;
      }).observe(document.body, { attributes: true, attributeFilter: ['class'] });
    } catch (_) {}
    function isReplay(s, at) { return at < replayUntil || at < s.firstAt + REPLAY_MS; }

    function sess(id) {
      id = String(id || '').toUpperCase();
      let s = sessions.get(id);
      if (!s) {
      // Only the few sessions looked at last are kept, each up to LOG_BYTES.
      if (sessions.size >= 3) {
        let curId = ''; try { curId = String(sessionId || '').toUpperCase(); } catch (_) {}
        for (const k of sessions.keys()) { if (sessions.size < 3) break; if (k !== curId) sessions.delete(k); }
      }
        s = { id, log: [], bytes: 0, dec: new TextDecoder(), partial: '', attn: '', run: null, runs: [],
              moments: [], seenMoment: new Set(), firstAt: 0 };
        sessions.set(id, s);
      }
      return s;
    }
    function cur() { try { return sessionId ? sess(sessionId) : null; } catch (_) { return null; } }

    function stripAnsi(t) {
      return t
        .replace(/\x1b\][^\x07\x1b]*(\x07|\x1b\\)/g, '')
        // Colours vanish; a move forward is a space; any other move or erase ends the line.
        .replace(/\x1b\[[0-9;?<>=!]*[ -\/]*[@-~]/g, m => m.endsWith('m') ? '' : m.endsWith('C') ? ' ' : '\n')
        .replace(/\x1b[()][0-9A-Za-z]/g, '')
        .replace(/\x1b[=>78DEHMNOZc]/g, '')
        .replace(/\r+\n/g, '\n')
        .replace(/\r/g, '\n')
        .replace(/[\x00-\x08\x0b-\x1f\x7f]/g, '');
    }

    function newRun(s, at) {
      return { start: at, end: 0, host: hostName(), filesRead: new Set(), filesChanged: new Set(),
               cmds: new Set(), commits: new Map(), passFirst: null, passLast: null, failLast: null, okPkgs: new Set(),
               waits: 0, approvals: [], away: 0, hiddenSince: document.hidden ? at : 0, joinedLate: false, waited: 0, inputSince: 0 };
    }

    const TOOL_RE = /^\s*[⏺●•✻⎿]?\s*(Read|Update|Edit|MultiEdit|Write|Bash|Grep|Glob|Search|List|WebFetch|Task)\((.{1,300}?)\)?\s*$/;
    const CODEX_RE = /^\s*[•●]\s*(Ran|Edited|Read|Added|Deleted|Explored)\s+(.{1,300})$/;
    const COMMIT_RE = /\[([\w.\/-]+)(?: \(root-commit\))? ([0-9a-f]{7,12})\] (.{1,200})/;
    const MOMENT_KINDS = [
      { kind: 'right', re: /you'?re absolutely right|you are absolutely right|you'?re right!/i, top: "ME: THAT'S NOT IT", bottom: "IT: YOU'RE ABSOLUTELY RIGHT", title: "Told I'm absolutely right" },
      { kind: 'sorry', re: /\bI apologi[sz]e\b|my apologies|sorry (about|for) (that|the)/i, top: 'IT BROKE THE BUILD', bottom: 'I APOLOGIZE FOR THE CONFUSION', title: 'Received a heartfelt apology' },
      { kind: 'see', re: /I see the (issue|problem)|found the (issue|bug|problem)|I see what'?s happening/i, top: '47 MINUTES IN', bottom: 'I SEE THE ISSUE NOW', title: 'The agent saw the issue' },
      // Before "perfect": "Perfect! All 142 tests pass" is a win, not a joke on it.
      { kind: 'green', re: /all (\d+ )?tests pass|tests? (are )?(now )?passing/i, top: 'AFTER THREE HOURS', bottom: 'ALL TESTS PASS', title: 'All green' },
      { kind: 'perfect', re: /^\s*[⏺●•]?\s*(Perfect|Excellent|Great)!/, top: 'TESTS STILL RED', bottom: 'PERFECT! EVERYTHING WORKS', title: 'Everything is perfect (allegedly)' },
      { kind: 'should', re: /should (now )?work|this (should|will) fix|that should do it/i, top: 'ATTEMPT #6', bottom: 'THIS SHOULD FIX IT', title: 'Attempt number six' },
      { kind: 'again', re: /different approach|let me try (again|another|a simpler)/i, top: 'PLAN A FAILED', bottom: 'LET ME TRY A DIFFERENT APPROACH', title: 'Plan B unlocked' },
      { kind: 'tests', re: /\b(remov|delet|skipp?)\w* (the |that )?(failing |flaky )?tests?\b/i, top: 'TESTS FAILING', bottom: 'NOT ANYMORE', title: 'Fixed the tests (by removing them)' },
    ];

    // A reply wraps onto further rows on a narrow screen: the moment takes the
    // rows that follow it until a new item, a box or a pause, then is kept.
    const CONT_STOP = /^\s*([⏺●•✻✶✳✢·⎿│╭╰─❯>]|\S+\()/;
    function keepMoment(s) {
      const p = s.pendMoment;
      if (!p) return;
      s.pendMoment = null; clearTimeout(p.timer);
      const text = p.text.slice(0, 220);
      if (s.seenMoment.has(text)) return;
      if (s.seenMoment.size > 500) s.seenMoment.clear();
      s.seenMoment.add(text);
      s.moments.push({ kind: p.k.kind, text, at: p.at });
      if (s.moments.length > 40) s.moments.shift();
      if (!p.old) offerMoment(s, p.k, text);
    }
    function readLine(s, line, at) {
      line = line.replace(/\s+$/, '');
      if (s.pendMoment) {
        // A cursor move or a split CR LF reads as an empty row: not the reply's end.
        if (!line.trim()) return;
        if (!CONT_STOP.test(line) && s.pendMoment.text.length < 220) {
          s.pendMoment.text += ' ' + line.trim();
          return;
        }
        keepMoment(s);
      }
      if (line.length < 3) return;
      for (const k of MOMENT_KINDS) {
        if (k.re.test(line)) {
          s.pendMoment = { k, at, old: isReplay(s, at), text: line.replace(/^\s*[⏺●•✻⎿>]\s*/, '').trim() };
          s.pendMoment.timer = setTimeout(() => keepMoment(s), 1500);
          break;
        }
      }
      const r = s.run;
      if (!r) return;
      let m;
      if ((m = TOOL_RE.exec(line))) {
        const tool = m[1], arg = m[2].trim();
        if (tool === 'Read') r.filesRead.add(arg);
        else if (/Update|Edit|MultiEdit|Write/.test(tool)) r.filesChanged.add(arg);
        else if (tool === 'Bash') r.cmds.add(arg);
      } else if ((m = CODEX_RE.exec(line))) {
        if (m[1] === 'Ran') r.cmds.add(m[2]);
        else if (m[1] === 'Read' || m[1] === 'Explored') r.filesRead.add(m[2]);
        else r.filesChanged.add(m[2]);
      }
      if ((m = COMMIT_RE.exec(line))) r.commits.set(m[2], m[3]);
      if ((m = /(\d+) (?:tests? )?(passed|passing)\b/i.exec(line)) || (m = /Tests?:\s+(\d+) passed/i.exec(line))) {
        const n = +m[1];
        if (r.passFirst === null) r.passFirst = n;
        r.passLast = n;
        if (!/fail/i.test(line)) r.failLast = 0;
      }
      if ((m = /(\d+) (?:tests? )?(failed|failing)\b/i.exec(line))) r.failLast = +m[1];
      if ((m = /^ok\s+(\S+)\s/.exec(line))) r.okPkgs.add(m[1]);
    }

    // trimLog drops the oldest entries past the size or age cap, in one splice.
    // An attention change in the log carries no bytes.
    function trimLog(s, at) {
      let n = 0;
      while (n < s.log.length - 1 && (s.bytes > LOG_BYTES || at - s.log[n].t > LOG_AGE)) {
        if (s.log[n].b) s.bytes -= s.log[n].b.byteLength;
        n++;
      }
      if (n) s.log.splice(0, n);
    }
    function output(id, u8) {
      if (!id || !u8 || !u8.byteLength) return;
      const s = sess(id), at = Date.now();
      if (!s.firstAt) s.firstAt = at;
      s.log.push({ t: at, b: u8.slice(), old: isReplay(s, at) });
      s.bytes += u8.byteLength;
      trimLog(s, at);
      const text = s.partial + stripAnsi(s.dec.decode(u8, { stream: true }));
      const lines = text.split('\n');
      s.partial = lines.pop().slice(-400);
      for (const l of lines) readLine(s, l, at);
    }

    function attn(id, a) {
      if (!id) return;
      const s = sess(id), at = Date.now(), prev = s.attn, first = !s.attnSeen;
      s.log.push({ t: at, attn: a || '' });
      trimLog(s, at);
      // Time spent waiting on the person is not the agent's time.
      if (s.run && prev === 'input' && a !== 'input' && s.run.inputSince) { s.run.waited += at - s.run.inputSince; s.run.inputSince = 0; }
      s.attn = a || '';
      s.attnSeen = true;
      if (a === 'working' || a === 'input') {
        // A run already going when this viewer connected: it began before we saw it.
        if (!s.run) { s.run = newRun(s, at); s.run.joinedLate = first; }
        if (a === 'input' && prev !== 'input') { s.run.waits++; s.run.answered = false; s.run.inputSince = at; }
      } else if (s.run) {
        finishRun(s, at);
      }
    }

    function input(id, data) {
      const s = id && sessions.get(String(id).toUpperCase());
      if (!s || !s.run || s.attn !== 'input' || s.run.answered) return;
      if (/[\r\n]|^[0-9yYnN]$/.test(data)) {
        s.run.answered = true;
        s.run.approvals.push({ at: Date.now(), phone: COARSE });
      }
    }

    document.addEventListener('visibilitychange', () => {
      const at = Date.now();
      for (const s of sessions.values()) {
        const r = s.run;
        if (!r) continue;
        if (document.hidden) r.hiddenSince = at;
        else if (r.hiddenSince) { r.away += at - r.hiddenSince; r.hiddenSince = 0; }
      }
    });

    function plainRun(r) {
      return { start: r.start, end: r.end, host: r.host || '', filesRead: r.filesRead.size,
               filesChanged: r.filesChanged.size, cmds: r.cmds.size, commits: r.commits.size,
               passFirst: r.passFirst, passLast: r.passLast,
               failLast: r.failLast, okPkgs: r.okPkgs.size, waits: r.waits, approvals: r.approvals, away: r.away,
               joinedLate: !!r.joinedLate, waited: r.waited || 0 };
    }
    function finishRun(s, at) {
      const r = s.run;
      s.run = null;
      if (r.hiddenSince) { r.away += at - r.hiddenSince; r.hiddenSince = 0; }
      if (r.inputSince) { r.waited += at - r.inputSince; r.inputSince = 0; }
      r.end = at;
      if (r.end - r.start < 4000) return;
      const p = plainRun(r);
      p.session = s.id;
      s.runs.push(p);
      saveRun(p);
      // Wrapped's ready-made set is out of date with this run in.
      if (state) state.wrappedAll = null;
      offerReceipt(s, p);
    }
    function loadRuns() {
      try { const v = JSON.parse(localStorage.getItem(RUNS_KEY) || '[]'); return Array.isArray(v) ? v : []; } catch (_) { return []; }
    }
    function saveRun(p) {
      try {
        const all = loadRuns();
        all.push(p);
        localStorage.setItem(RUNS_KEY, JSON.stringify(all.slice(-400)));
      } catch (_) {}
    }

    // ------------------------------------------------------------ helpers
    const MONO = 'Menlo, "DejaVu Sans Mono", "SF Mono", Consolas, monospace';
    const SANS = 'Geist, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif';
    function mk(w, h) { const c = document.createElement('canvas'); c.width = w; c.height = h; return c; }
    function rr(ctx, x, y, w, h, r) {
      ctx.beginPath();
      ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r);
      ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath();
    }
    function wrap(ctx, text, maxW) {
      const out = [];
      for (const para of String(text).split('\n')) {
        let line = '';
        for (const word of para.split(/\s+/)) {
          const t = line ? line + ' ' + word : word;
          if (ctx.measureText(t).width > maxW && line) { out.push(line); line = word; }
          else line = t;
          while (ctx.measureText(line).width > maxW && line.length > 1) {
            let k = line.length - 1;
            while (k > 1 && ctx.measureText(line.slice(0, k)).width > maxW) k--;
            out.push(line.slice(0, k)); line = line.slice(k);
          }
        }
        out.push(line);
      }
      return out;
    }
    // fitWrap: the largest font (≤ max) at which text wraps into at most maxLines within maxW.
    // maxLines may be a function of the font size (a box of fixed height).
    function fitWrap(ctx, text, font, max, min, maxW, maxLines) {
      const most = px => typeof maxLines === 'function' ? maxLines(px) : maxLines;
      for (let px = max; px >= min; px -= 2) {
        ctx.font = font.replace('{px}', px);
        const lines = wrap(ctx, text, maxW);
        if (lines.length <= most(px)) return { px, lines };
      }
      ctx.font = font.replace('{px}', min);
      const lines = wrap(ctx, text, maxW), n = most(min);
      if (lines.length <= n) return { px: min, lines };
      const cut = lines.slice(0, n);
      cut[n - 1] = cut[n - 1].replace(/\s*\S{0,12}$/, '') + '…';
      return { px: min, lines: cut };
    }
    // The agent's reply dot, drawn: the ⏺ character is an emoji on some phones.
    function dot(ctx, x, y, px) {
      ctx.beginPath(); ctx.arc(x + px * 0.32, y + px * 0.62, px * 0.26, 0, 7); ctx.fillStyle = '#3fb950'; ctx.fill();
    }
    function dur(ms) {
      const m = Math.round(ms / 60000);
      if (m < 1) return Math.max(1, Math.round(ms / 1000)) + ' s';
      if (m < 60) return m + ' min';
      const h = Math.floor(m / 60), r = m % 60;
      return h + ' h' + (r ? ' ' + r + ' min' : '');
    }
    function clock(t) {
      const d = new Date(t);
      let h = d.getHours(); const ap = h < 12 ? 'am' : 'pm'; h = h % 12 || 12;
      return h + ':' + String(d.getMinutes()).padStart(2, '0') + ' ' + ap;
    }
    function day(t) { return new Date(t).toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' }); }
    function plural(n, w) { return n + ' ' + w + (n === 1 ? '' : 's'); }

    // The app icon: a blue "re" on a dark rounded square. On a dark card a thin
    // edge keeps the square from melting into the background.
    function mark(ctx, x, y, size, ink) {
      ctx.save();
      rr(ctx, x, y, size, size, size * 0.22);
      ctx.fillStyle = '#0d1117';
      ctx.fill();
      if (ink !== 'dark') { ctx.lineWidth = Math.max(1, size / 24); ctx.strokeStyle = 'rgba(230,237,243,.28)'; ctx.stroke(); }
      ctx.fillStyle = '#58a6ff';
      ctx.font = '700 ' + Math.round(size * 0.5) + 'px ' + SANS;
      ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
      ctx.fillText('re', x + size / 2, y + size * 0.5);
      ctx.restore();
    }
    // watermark: "made with reminal · reminal.app" at the foot of every card.
    function watermark(ctx, w, h, ink, y) {
      ctx.save();
      const size = 34;
      ctx.font = '600 26px ' + SANS;
      const text = 'made with reminal  ·  reminal.app';
      const tw = ctx.measureText(text).width;
      const x = (w - tw - size - 14) / 2;
      const yy = y != null ? y : h - 64;
      mark(ctx, x, yy - size / 2, size, ink === 'dark' ? 'dark' : 'light');
      ctx.fillStyle = ink === 'dark' ? 'rgba(13,17,23,.72)' : 'rgba(230,237,243,.82)';
      ctx.textBaseline = 'middle'; ctx.textAlign = 'left';
      ctx.fillText(text, x + size + 14, yy + 1);
      ctx.restore();
    }

    // ------------------------------------------------- terminal snapshots
    const PALETTE = ['#484f58', '#ff7b72', '#3fb950', '#d29922', '#58a6ff', '#bc8cff', '#39c5cf', '#b1bac4',
                     '#6e7681', '#ffa198', '#56d364', '#e3b341', '#79c0ff', '#d2a8ff', '#56d4dd', '#ffffff'];
    function pal(i) {
      if (i < 16) return PALETTE[i];
      if (i < 232) {
        i -= 16;
        const v = n => (n ? 55 + n * 40 : 0);
        return 'rgb(' + v(Math.floor(i / 36)) + ',' + v(Math.floor(i / 6) % 6) + ',' + v(i % 6) + ')';
      }
      const g = 8 + (i - 232) * 10;
      return 'rgb(' + g + ',' + g + ',' + g + ')';
    }
    function hex(n) { return '#' + n.toString(16).padStart(6, '0'); }
    // snapRows reads rows [from, from+count) of an xterm buffer as styled runs,
    // scrubbed in place (a hidden word keeps its width, so the grid holds).
    // A long line (a key, say) wraps over several rows: it is scrubbed whole,
    // then each cell takes its own slice back, by the cell's offset in the
    // text, so a wide character doesn't shift the rest.
    function cleanCells(t, buf, y, cache) {
      let y0 = y;
      while (y0 > 0 && buf.getLine(y0) && buf.getLine(y0).isWrapped) y0--;
      if (cache.has(y0)) return cache.get(y0).get(y);
      const per = new Map(), parts = [];
      let cell = buf.getNullCell(), off = 0;
      for (let yy = y0; yy === y0 || (buf.getLine(yy) && buf.getLine(yy).isWrapped); yy++) {
        const line = buf.getLine(yy);
        if (!line) break;
        const cells = [];
        for (let x = 0; x < t.cols; x++) {
          cell = line.getCell(x, cell);
          if (!cell) break;
          const ch = cell.getWidth() === 0 ? '' : (cell.getChars() || ' ');
          cells.push([off, ch.length]); parts.push(ch); off += ch.length;
        }
        per.set(yy, cells);
      }
      // A line longer than any screen of it (a minified file, a dump) is hidden
      // whole: no rule is tried across tens of thousands of characters.
      const raw = parts.join(''), clean = raw.length > 8000 ? raw.replace(/\S/g, '•') : scrub(raw, true), out = new Map();
      for (const [yy, cells] of per) out.set(yy, clean === raw ? null : cells.map(([o, n]) => clean.substr(o, n)));
      cache.set(y0, out);
      return out.get(y);
    }
    function snapRows(t, from, count) {
      const buf = t.buffer.active, rows = [], cache = new Map();
      let cell = buf.getNullCell();
      for (let y = from; y < from + count; y++) {
        const line = buf.getLine(y);
        if (!line) { rows.push([]); continue; }
        const clean = cleanCells(t, buf, y, cache);
        const runs = [];
        let run = null;
        for (let x = 0; x < t.cols; x++) {
          cell = line.getCell(x, cell);
          if (!cell) break;
          if (cell.getWidth() === 0) continue;
          let fg = cell.isFgRGB() ? hex(cell.getFgColor()) : cell.isFgPalette() ? pal(cell.getFgColor()) : null;
          let bg = cell.isBgRGB() ? hex(cell.getBgColor()) : cell.isBgPalette() ? pal(cell.getBgColor()) : null;
          if (cell.isInverse()) { const f = fg; fg = bg || '#0d1117'; bg = f || '#e6edf3'; }
          const bold = !!cell.isBold(), dim = !!cell.isDim();
          const ch = clean ? (clean[x] || ' ') : (cell.getChars() || ' ');
          if (run && run.fg === fg && run.bg === bg && run.bold === bold && run.dim === dim) run.text += ch;
          else { run = { x, text: ch, fg, bg, bold, dim }; runs.push(run); }
        }
        rows.push(runs);
      }
      return rows;
    }
    function rowText(runs) { return runs.map(r => r.text).join('').replace(/\s+$/, ''); }
    const GLYPH_FALLBACK = { '\u23F4': '\u25C2', '\u23F5': '\u25B8', '\u23F8': '\u2016', '\u23F9': '\u25A0' };
    // drawRows lays styled rows out at (x, y), px font, a cell ch wide.
    function drawRows(ctx, rows, x, y, px, opts) {
      opts = opts || {};
      const lh = Math.round(px * 1.32);
      ctx.save();
      ctx.font = px + 'px ' + MONO;
      const cw = ctx.measureText('M').width;
      ctx.textBaseline = 'top'; ctx.textAlign = 'left';
      const cut = opts.cols || 1e9;
      rows.forEach((runs, i) => {
        const yy = y + i * lh;
        for (const r of runs) {
          if (r.x >= cut) break;
          let text = r.text.slice(0, Math.max(0, cut - r.x));
          // A background overlaps the next row by a hair: no seam between rows.
          if (r.bg) { ctx.fillStyle = r.bg; ctx.fillRect(x + r.x * cw, yy - 0.5, text.length * cw + 0.5, lh + 1); }
          if (!text.trim()) continue;
          ctx.font = (r.bold ? '700 ' : '') + px + 'px ' + MONO;
          ctx.fillStyle = r.fg || opts.fg || '#e6edf3';
          ctx.globalAlpha = r.dim ? 0.6 : 1;
          // The agent's dot is drawn: ⏺ is an emoji on some phones, and squeezed into a cell ● is an oval.
          if (/[\u23FA\u25CF]/.test(text)) {
            [...text].forEach((ch, i) => { if (/[\u23FA\u25CF]/.test(ch)) { ctx.beginPath(); ctx.arc(x + (r.x + i + 0.5) * cw, yy + lh / 2, cw * 0.44, 0, 7); ctx.fill(); } });
            text = text.replace(/[\u23FA\u25CF]/g, ' ');
          }
          // So is a hidden character's \u2022, one to a cell, so the text after it keeps its column.
          if (text.includes('\u2022')) {
            [...text].forEach((ch, i) => { if (ch === '\u2022') { ctx.beginPath(); ctx.arc(x + (r.x + i + 0.5) * cw, yy + lh / 2, cw * 0.17, 0, 7); ctx.fill(); } });
            text = text.replace(/\u2022/g, ' ');
          }
          // Glyphs a phone's fonts lack (Claude Code's mode arrows) would be empty
          // boxes: drawn as near ones every font has.
          text = text.replace(/[\u23F4\u23F5\u23F8\u23F9]/g, m => GLYPH_FALLBACK[m]);
          // Held to its cells: a glyph from a fallback font (a dot, a box line) can run wider.
          ctx.fillText(text, x + r.x * cw, yy + (lh - px) / 2, text.length * cw);
          ctx.globalAlpha = 1;
        }
      });
      ctx.restore();
      return { lh, cw };
    }
    // viewRows: the rows on screen now, trailing blank rows dropped, and how
    // wide the widest is (so a card can crop the empty right side away).
    function viewRows() {
      const buf = term.buffer.active;
      let rows = snapRows(term, buf.viewportY, term.rows);
      while (rows.length > 1 && !rowText(rows[rows.length - 1])) rows.pop();
      while (rows.length > 1 && !rowText(rows[0])) rows.shift();
      const width = Math.max(10, ...rows.map(r => rowText(r)).filter(t => !/^[\s─━═╭╮╰╯│┌┐└┘-]*$/.test(t)).map(t => t.length));
      return { rows, width };
    }

    // ---------------------------------------------------------- 1. receipt
    function liveRun(s) {
      if (!s || !s.run) return null;
      const p = plainRun(s.run);
      p.end = Date.now();
      p.live = true;
      // Waiting on you right now: that's not agent time either.
      if (s.run.inputSince) p.waited += p.end - s.run.inputSince;
      return p;
    }
    function receiptLines(r, opts) {
      const L = [];
      const total = r.end - r.start;
      const approvals = r.approvals || [];
      L.push(['MACHINE', opts.showHost && r.host ? r.host.toUpperCase() : 'MY MACHINE']);
      L.push(['DATE', day(r.start).toUpperCase()]);
      // No clock times: with them, the card reads like a timesheet entry.
      L.push(['-']);
      if (r.filesRead) L.push(['FILES READ', String(r.filesRead)]);
      if (r.filesChanged) L.push(['FILES CHANGED', String(r.filesChanged)]);
      if (r.cmds) L.push(['COMMANDS RUN', String(r.cmds)]);
      if (r.commits) L.push(['COMMITS', String(r.commits)]);
      if (r.passLast != null) L.push(['TESTS PASSING', (r.passFirst != null && r.passFirst !== r.passLast ? r.passFirst + ' → ' : '') + r.passLast]);
      else if (r.okPkgs) L.push(['TEST PACKAGES OK', String(r.okPkgs)]);
      if (r.failLast) L.push(['TESTS FAILING', String(r.failLast)]);
      if (r.waits) L.push(['ASKED FOR YOU', plural(r.waits, 'time')]);
      if (approvals.length) {
        const ph = approvals.filter(a => a.phone).length;
        L.push(['YOU SAID YES' + (ph ? ' FROM PHONE' : ''), String(ph || approvals.length)]);
      }
      L.push(['-']);
      L.push([r.live ? 'AGENT TIME SO FAR' : 'AGENT TIME', dur(Math.max(0, total - (r.waited || 0))).toUpperCase(), 'big']);
      if ((r.waited || 0) > 60000) L.push(['WAITED ON YOU', dur(r.waited).toUpperCase()]);
      if (r.away > 60000) L.push(['WHILE YOU WERE AWAY', dur(r.away).toUpperCase()]);
      if (r.joinedLate) L.push(['', '(IT STARTED BEFORE YOU LOOKED)']);
      return L;
    }
    function drawReceipt(r, opts) {
      const lines = receiptLines(r, opts);
      const PW = 700, px = 27, lh = 44;
      const noteLines = [];
      const m = mk(1, 1).getContext('2d');
      m.font = 'italic ' + px + 'px ' + MONO;
      // A note can't put a price on the card: amounts of money are blanked out.
      // So are the words a bill is made of.
      const CUR = 'usd|eur|gbp|inr|cad|aud|jpy|cny|chf|krw|thb|sgd|rs\\.?|dollars?|bucks|euros?|pounds?|rupees?|yen';
      const MONEY = new RegExp('[$€£¥₹₩฿]\\s?\\d[\\d,.]*|\\b(' + CUR + ')\\s?\\d[\\d,.]*|\\d[\\d,.]*\\s?(' + CUR + ')\\b', 'gi');
      const BILL = /\b(paid|payment|invoices?|inv|receipt|total|subtotal|amount( due)?|balance|bill(ed|able)?|charged?|refund(ed)?|txn|transaction|tax|vat|gst)\b(\s*[:#.]?\s*\d[\d,.]*)?|\b\d[\d,.]*\s*(hours?|hrs?|h)\b/gi;
      if (opts.note) noteLines.push(...wrap(m, scrub('"' + opts.note + '"').replace(MONEY, '•••').replace(BILL, '•••'), PW - 96).slice(0, 3));
      const lastOk = (r.approvals || [])[(r.approvals || []).length - 1];
      // No time on it: the card carries no clock times at all.
      const stamp = lastOk ? (lastOk.phone ? 'I SAID YES · FROM MY PHONE' : 'I SAID YES') : '';
      const PH = 250 + lines.length * lh + noteLines.length * lh + (noteLines.length ? 30 : 0) + 110 + (stamp ? 120 : 0);
      // 4:5 for a post, taller when the receipt is long: never under the watermark.
      const W = 1080, H = Math.max(1350, PH + 230), c = mk(W, H), ctx = c.getContext('2d');
      const g = ctx.createLinearGradient(0, 0, W, H);
      g.addColorStop(0, opts.bg[0]); g.addColorStop(1, opts.bg[1]);
      ctx.fillStyle = g; ctx.fillRect(0, 0, W, H);
      const px0 = (W - PW) / 2, py0 = Math.max(60, (H - 130 - PH) / 2);
      ctx.save();
      ctx.translate(W / 2, py0 + PH / 2); ctx.rotate(-0.018); ctx.translate(-W / 2, -(py0 + PH / 2));
      ctx.shadowColor = 'rgba(0,0,0,.35)'; ctx.shadowBlur = 40; ctx.shadowOffsetY = 18;
      ctx.beginPath();
      const zz = 14;
      ctx.moveTo(px0, py0);
      for (let x = px0; x < px0 + PW; x += zz * 2) { ctx.lineTo(x + zz, py0 + zz); ctx.lineTo(Math.min(x + zz * 2, px0 + PW), py0); }
      ctx.lineTo(px0 + PW, py0 + PH);
      for (let x = px0 + PW; x > px0; x -= zz * 2) { ctx.lineTo(x - zz, py0 + PH - zz); ctx.lineTo(Math.max(x - zz * 2, px0), py0 + PH); }
      ctx.closePath();
      ctx.fillStyle = '#fbf8f1'; ctx.fill();
      ctx.shadowColor = 'transparent';
      // It's a joke receipt, and the paper says so all over: any crop of it
      // still reads "not a receipt", the way a specimen banknote says SPECIMEN.
      ctx.save();
      ctx.clip();
      ctx.translate(W / 2, py0 + PH / 2); ctx.rotate(-0.5);
      ctx.font = '700 22px ' + SANS; ctx.fillStyle = 'rgba(31,111,235,.15)'; ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
      const motto = 'AI AGENT LOG  ·  NOT A RECEIPT  ·  NOT A TIMESHEET  ·  NO PAYMENT  ·  ';
      const span = Math.hypot(PW, PH);
      for (let i = 0, yy = -span / 2; yy < span / 2; yy += 58, i++) ctx.fillText(motto.repeat(6), (i % 2) * 120, yy);
      ctx.restore();
      const ink = '#22201c', L0 = px0 + 48, R0 = px0 + PW - 48;
      let y = py0 + 70;
      mark(ctx, W / 2 - 28, y - 10, 56, 'dark');
      y += 80;
      ctx.fillStyle = ink; ctx.textAlign = 'center'; ctx.textBaseline = 'alphabetic';
      ctx.font = '700 34px ' + MONO; ctx.fillText('MY AGENT’S RUN', W / 2, y);
      y += 38;
      ctx.font = px - 4 + 'px ' + MONO; ctx.fillStyle = '#6b665c';
      ctx.fillText('WHAT IT DID, NOT A RECEIPT', W / 2, y);
      y += 36;
      ctx.textAlign = 'left';
      for (const [k, v, big] of lines) {
        if (k === '-') {
          ctx.fillStyle = '#9b958a'; ctx.font = px + 'px ' + MONO;
          let dash = ''; while (ctx.measureText(dash + '- ').width < R0 - L0) dash += '- ';
          ctx.fillText(dash, L0, y + 10); y += lh; continue;
        }
        ctx.fillStyle = ink;
        ctx.font = (big ? '700 ' + (px + 6) : px) + 'px ' + MONO;
        ctx.fillText(k, L0, y + 10);
        ctx.textAlign = 'right'; ctx.fillText(v, R0, y + 10); ctx.textAlign = 'left';
        y += big ? lh + 8 : lh;
      }
      if (noteLines.length) {
        y += 18;
        ctx.font = 'italic ' + px + 'px ' + MONO; ctx.fillStyle = ink; ctx.textAlign = 'center';
        for (const l of noteLines) { ctx.fillText(l, W / 2, y + 10); y += lh; }
        ctx.textAlign = 'left';
      }
      y += 40;
      ctx.font = px - 4 + 'px ' + MONO; ctx.fillStyle = '#6b665c'; ctx.textAlign = 'center';
      ctx.fillText('THANK YOU FOR NOT HOVERING', W / 2, y);
      if (stamp) {
        // A friendly mark, not an official one: no red, no "APPROVED".
        ctx.save();
        ctx.translate(W / 2 + 40, y + 80); ctx.rotate(-0.08);
        ctx.strokeStyle = 'rgba(46,160,67,.8)'; ctx.fillStyle = 'rgba(35,134,54,.9)'; ctx.lineWidth = 4;
        ctx.font = '700 25px ' + SANS;
        const lines2 = stamp.split(' · ');
        const sw = Math.max(...lines2.map(l => ctx.measureText(l).width)) + 44;
        rr(ctx, -sw / 2, -46, sw, 92, 46); ctx.stroke();
        ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
        if (lines2.length > 1) { ctx.fillText(lines2[0], 0, -14); ctx.fillText(lines2[1], 0, 20); } else ctx.fillText(lines2[0], 0, 2);
        ctx.restore();
      }
      ctx.restore();
      watermark(ctx, W, H, opts.dark ? 'light' : 'dark');
      return c;
    }

    // ------------------------------------------------------------- 3. meme
    function drawMeme(o) {
      const W = 1080, H = 1080, c = mk(W, H), ctx = c.getContext('2d');
      const quote = scrub(o.quote || '');
      const top = scrub(o.top || ''), bottom = scrub(o.bottom || '');
      const IMPACT = '900 {px}px Impact, Haettenschweiler, "Anton", "Arial Narrow Bold", "Arial Black", sans-serif';
      const outlined = (text, y, base) => {
        if (!text) return y;
        const f = fitWrap(ctx, text.toUpperCase(), IMPACT, 96, 44, W - 100, 3);
        ctx.font = IMPACT.replace('{px}', f.px);
        ctx.textAlign = 'center'; ctx.lineJoin = 'round';
        ctx.lineWidth = Math.max(6, f.px / 9); ctx.strokeStyle = '#000'; ctx.fillStyle = '#fff';
        const lh = f.px * 1.05;
        const y0 = base === 'top' ? y : y - (f.lines.length - 1) * lh;
        ctx.textBaseline = base === 'top' ? 'top' : 'alphabetic';
        f.lines.forEach((l, i) => { ctx.strokeText(l, W / 2, y0 + i * lh); ctx.fillText(l, W / 2, y0 + i * lh); });
        // The edge facing the middle, for laying out what goes between.
        return base === 'top' ? y0 + (f.lines.length - 1) * lh + f.px : y0 - f.px * 0.9;
      };
      const termPanel = (x, y, w, h, text, px) => {
        rr(ctx, x, y, w, h, 22); ctx.fillStyle = '#0d1117'; ctx.fill();
        ctx.strokeStyle = '#30363d'; ctx.lineWidth = 3; ctx.stroke();
        ['#ff5f57', '#febc2e', '#28c840'].forEach((col, i) => { ctx.beginPath(); ctx.arc(x + 34 + i * 30, y + 32, 9, 0, 7); ctx.fillStyle = col; ctx.fill(); });
        const f = fitWrap(ctx, text, '{px}px ' + MONO, px, 22, w - 124, p => Math.floor((h - 90) / (p * 1.4)));
        ctx.font = f.px + 'px ' + MONO; ctx.textAlign = 'left'; ctx.textBaseline = 'top';
        const lh = f.px * 1.4, th = f.lines.length * lh, ty = y + 60 + Math.max(0, (h - 60 - th) / 2);
        dot(ctx, x + 40, ty, f.px);
        ctx.fillStyle = '#e6edf3';
        f.lines.forEach((l, i) => ctx.fillText(l, x + 84, ty + i * lh));
      };
      if (o.template === 'twopanel') {
        ctx.fillStyle = '#f6f8fa'; ctx.fillRect(0, 0, W, H);
        ctx.fillStyle = '#0d1117'; ctx.fillRect(0, H / 2, W, H / 2);
        ctx.textAlign = 'left'; ctx.textBaseline = 'top';
        ctx.fillStyle = '#57606a'; ctx.font = '700 34px ' + SANS; ctx.fillText('WHAT I ASKED', 60, 56);
        const f1 = fitWrap(ctx, top || 'Fix the failing test', '700 {px}px ' + SANS, 76, 34, W - 120, 4);
        ctx.font = '700 ' + f1.px + 'px ' + SANS; ctx.fillStyle = '#0d1117';
        f1.lines.forEach((l, i) => ctx.fillText(l, 60, 120 + i * f1.px * 1.15));
        ctx.fillStyle = '#8b949e'; ctx.font = '700 34px ' + SANS; ctx.fillText('WHAT I GOT', 60, H / 2 + 50);
        const f2 = fitWrap(ctx, quote || bottom, '{px}px ' + MONO, 52, 24, W - 180, 6);
        ctx.font = f2.px + 'px ' + MONO;
        dot(ctx, 60, H / 2 + 120, f2.px);
        ctx.fillStyle = '#e6edf3'; f2.lines.forEach((l, i) => ctx.fillText(l, 60 + f2.px * 1.6, H / 2 + 120 + i * f2.px * 1.4));
        watermark(ctx, W, H, 'light', H - 50);
      } else if (o.template === 'quote') {
        const g = ctx.createLinearGradient(0, 0, W, H);
        g.addColorStop(0, '#1f1147'); g.addColorStop(1, '#0b3d5c');
        ctx.fillStyle = g; ctx.fillRect(0, 0, W, H);
        ctx.fillStyle = 'rgba(255,255,255,.12)'; ctx.font = '700 460px Georgia, serif'; ctx.textBaseline = 'top'; ctx.textAlign = 'left';
        ctx.fillText('“', 40, -40);
        const f = fitWrap(ctx, quote || bottom, '600 {px}px Georgia, "Times New Roman", serif', 84, 36, W - 180, 7);
        ctx.font = '600 ' + f.px + 'px Georgia, "Times New Roman", serif'; ctx.fillStyle = '#fff';
        const lh = f.px * 1.25, y0 = (H - f.lines.length * lh) / 2 - 30;
        f.lines.forEach((l, i) => ctx.fillText(l, 90, y0 + i * lh));
        ctx.font = '500 36px ' + SANS; ctx.fillStyle = 'rgba(255,255,255,.75)';
        ctx.fillText('— ' + (top || 'my coding agent, ' + clock(o.at || Date.now())), 90, y0 + f.lines.length * lh + 40);
        watermark(ctx, W, H, 'light');
      } else if (o.template === 'achievement') {
        ctx.fillStyle = '#0d1117'; ctx.fillRect(0, 0, W, H);
        const cx = W / 2, cy = 330;
        const gg = ctx.createRadialGradient(cx, cy, 10, cx, cy, 190);
        gg.addColorStop(0, '#ffe08a'); gg.addColorStop(1, '#b8860b');
        ctx.beginPath(); ctx.arc(cx, cy, 150, 0, 7); ctx.fillStyle = gg; ctx.fill();
        ctx.lineWidth = 10; ctx.strokeStyle = '#ffd34d'; ctx.stroke();
        ctx.fillStyle = '#5c4300'; ctx.font = '900 150px ' + SANS; ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
        ctx.fillText('★', cx, cy + 6);
        ctx.fillStyle = '#d29922'; ctx.font = '800 38px ' + SANS; ctx.textBaseline = 'top';
        ctx.fillText('ACHIEVEMENT UNLOCKED', cx, 530);
        const f = fitWrap(ctx, top || 'Mission accomplished', '800 {px}px ' + SANS, 74, 36, W - 140, 3);
        ctx.font = '800 ' + f.px + 'px ' + SANS; ctx.fillStyle = '#fff';
        f.lines.forEach((l, i) => ctx.fillText(l, cx, 600 + i * f.px * 1.12));
        const f2 = fitWrap(ctx, '“' + (quote || bottom) + '”', 'italic {px}px ' + SANS, 40, 24, W - 180, 3);
        ctx.font = 'italic ' + f2.px + 'px ' + SANS; ctx.fillStyle = '#8b949e';
        const y2 = 620 + f.lines.length * f.px * 1.12 + 20;
        f2.lines.forEach((l, i) => ctx.fillText(l, cx, y2 + i * f2.px * 1.3));
        watermark(ctx, W, H, 'light');
      } else {
        const g = ctx.createLinearGradient(0, 0, 0, H);
        g.addColorStop(0, '#2d333b'); g.addColorStop(1, '#161b22');
        ctx.fillStyle = g; ctx.fillRect(0, 0, W, H);
        const above = outlined(top, 40, 'top'), below = outlined(bottom, H - 110, 'bottom');
        const py = above + 40, ph = Math.max(220, Math.min(480, below - 40 - py));
        termPanel(70, py + Math.max(0, (below - 40 - py - ph) / 2), W - 140, ph, quote || bottom, 50);
        watermark(ctx, W, H, 'light', H - 44);
      }
      return c;
    }

    // ------------------------------------------------------- 6. phone shot
    // phoneRows: the newest `want` rows of the terminal, rewrapped to a phone's
    // `cols` the way the viewer on a phone shows them (a wide desktop terminal
    // would otherwise be unreadably small in a phone frame).
    const RULE_ONLY = /^[\s─━═╭╮╰╯│┌┐└┘├┤┬┴┼-]*$/;
    // t: the terminal read (the page's own, or a clip's offscreen one).
    function phoneRows(cols, want, t) {
      t = t || term;
      const buf = t.buffer.active, W = t.cols;
      const text = y => { const l = buf.getLine(y); return l ? l.translateToString(true) : ''; };
      let y = buf.viewportY + t.rows - 1;
      while (y > buf.viewportY && !text(y).trim()) y--;
      const out = [];
      while (y >= 0 && out.length < want) {
        let y0 = y;
        while (y0 > 0 && buf.getLine(y0) && buf.getLine(y0).isWrapped) y0--;
        const cells = [];
        snapRows(t, y0, y - y0 + 1).forEach((runs, i, all) => {
          const row = [];
          for (const r of runs) for (const ch of r.text) row.push({ ch, fg: r.fg, bg: r.bg, bold: r.bold, dim: r.dim });
          if (i < all.length - 1) while (row.length < W) row.push({ ch: ' ' });
          cells.push(...row);
        });
        while (cells.length && cells[cells.length - 1].ch === ' ' && !cells[cells.length - 1].bg) cells.pop();
        out.unshift(...rewrap(cells, cols));
        y = y0 - 1;
      }
      return out.slice(-want).map(toRuns);
    }
    function rewrap(cells, cols) {
      // A highlighted line (a message the person sent, as an agent shows it) keeps
      // its background across the hang and out to the row's end, a block as it is
      // on screen, not ragged at each cut.
      const bg = lineBg(cells);
      const fill = row => bg ? row.concat(Array.from({ length: Math.max(0, cols - row.length) }, () => ({ ch: ' ', bg }))) : row;
      if (cells.length <= cols) return [fill(cells)];
      const str = cells.map(c => c.ch).join('');
      if (RULE_ONLY.test(str)) return [cells.slice(0, cols)];
      // Continuation rows hang under the text, past any bullet or box edge, as
      // agents print them; a box's left edge carries on down beside them.
      const at = str.search(/[A-Za-z0-9"'(\[{<$~.\/]/);
      const hang = Math.min(at < 0 ? 0 : at, Math.floor(cols / 3));
      const edge = cells.slice(0, hang).map(c => /[│┃|]/.test(c.ch) ? c : { ch: ' ', bg });
      const rows = [];
      let rest = cells, first = true;
      while (rest.length) {
        const room = first ? cols : cols - hang;
        let cut = room;
        if (rest.length > room) {
          const s = rest.slice(0, room + 1).map(c => c.ch).join('');
          const sp = s.lastIndexOf(' ');
          if (sp > room * 0.5) cut = sp + 1;
        }
        const piece = rest.slice(0, cut);
        rows.push(fill(first ? piece : edge.concat(piece)));
        rest = rest.slice(cut);
        while (rest.length && rest[0].ch === ' ') rest = rest.slice(1);
        first = false;
      }
      return rows;
    }
    // lineBg is the background most of a line's text sits on, when one does
    // (three in five of its letters), else undefined.
    function lineBg(cells) {
      const n = {};
      let text = 0;
      for (const c of cells) {
        if (c.ch === ' ') continue;
        text++;
        if (c.bg) n[c.bg] = (n[c.bg] || 0) + 1;
      }
      const top = Object.entries(n).sort((a, b) => b[1] - a[1])[0];
      return top && top[1] >= text * 0.6 ? top[0] : undefined;
    }
    function toRuns(cells) {
      const runs = [];
      let run = null;
      cells.forEach((c, x) => {
        if (run && run.fg === c.fg && run.bg === c.bg && run.bold === c.bold && run.dim === c.dim) run.text += c.ch;
        else { run = { x, text: c.ch, fg: c.fg, bg: c.bg, bold: c.bold, dim: c.dim }; runs.push(run); }
      });
      return runs;
    }

    // A phone, drawn to a real one's proportions (about 1 : 2.06), its screen
    // showing this terminal as the reminal viewer shows it on a phone.
    function drawDevice(ctx, x, y, w, h, s) {
      const R = w * 0.165, rim = w * 0.014, bez = w * 0.034;
      // Side buttons first; the frame covers their inner half.
      ctx.fillStyle = '#4a4a4f';
      const btn = (bx, by, bh) => { rr(ctx, bx, y + h * by, w * 0.022, h * bh, w * 0.008); ctx.fill(); };
      btn(x - w * 0.012, 0.165, 0.036); btn(x - w * 0.012, 0.235, 0.062); btn(x - w * 0.012, 0.315, 0.062);
      btn(x + w - w * 0.01, 0.26, 0.095);
      ctx.save();
      ctx.shadowColor = 'rgba(0,0,0,.42)'; ctx.shadowBlur = w * 0.12; ctx.shadowOffsetY = w * 0.06;
      const metal = ctx.createLinearGradient(x, 0, x + w, 0);
      metal.addColorStop(0, '#2b2b2e'); metal.addColorStop(0.04, '#77777c'); metal.addColorStop(0.1, '#3a3a3d');
      metal.addColorStop(0.9, '#3a3a3d'); metal.addColorStop(0.96, '#77777c'); metal.addColorStop(1, '#2b2b2e');
      rr(ctx, x, y, w, h, R); ctx.fillStyle = metal; ctx.fill();
      ctx.restore();
      rr(ctx, x + rim, y + rim, w - rim * 2, h - rim * 2, R - rim); ctx.fillStyle = '#050506'; ctx.fill();
      const sx = x + bez, sy = y + bez, sw = w - bez * 2, sh = h - bez * 2, sr = R - bez;
      ctx.save();
      rr(ctx, sx, sy, sw, sh, sr); ctx.clip();
      ctx.fillStyle = '#0d1117'; ctx.fillRect(sx, sy, sw, sh);
      const u = sw / 100;
      // Status bar: time, the island, signal, wi-fi, battery.
      const sbH = u * 12.5, mid = sy + sbH * 0.56;
      ctx.fillStyle = '#f0f3f6'; ctx.font = '600 ' + (u * 4.3) + 'px ' + SANS; ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
      ctx.fillText(clock(Date.now()).replace(/ [ap]m/, ''), sx + u * 17, mid);
      rr(ctx, sx + sw / 2 - u * 15.5, sy + u * 3, u * 31, u * 9.2, u * 4.6); ctx.fillStyle = '#000'; ctx.fill();
      ctx.fillStyle = '#f0f3f6';
      for (let i = 0; i < 4; i++) { const bh = u * (1.4 + i * 0.75); rr(ctx, sx + u * 68 + i * u * 1.55, mid + u * 1.6 - bh, u * 1.05, bh, u * 0.3); ctx.fill(); }
      ctx.strokeStyle = '#f0f3f6'; ctx.lineWidth = u * 0.75; ctx.lineCap = 'round';
      for (let i = 0; i < 3; i++) { ctx.beginPath(); ctx.arc(sx + u * 77.6, mid + u * 1.7, u * (0.9 + i * 1.25), Math.PI * 1.27, Math.PI * 1.73); ctx.stroke(); }
      ctx.lineWidth = u * 0.45; rr(ctx, sx + u * 82, mid - u * 1.55, u * 6.4, u * 3.1, u * 0.9); ctx.globalAlpha = 0.45; ctx.stroke(); ctx.globalAlpha = 1;
      rr(ctx, sx + u * 82.6, mid - u * 0.95, u * 4.4, u * 1.9, u * 0.5); ctx.fill();
      rr(ctx, sx + u * 88.7, mid - u * 0.6, u * 0.6, u * 1.2, u * 0.3); ctx.globalAlpha = 0.45; ctx.fill(); ctx.globalAlpha = 1;
      // The viewer's header: mark, name, and the live session pill.
      const hy = sy + sbH, hH = u * 11;
      ctx.fillStyle = '#0d1117'; ctx.fillRect(sx, hy, sw, hH);
      ctx.fillStyle = '#21262d'; ctx.fillRect(sx, hy + hH - u * 0.25, sw, u * 0.25);
      mark(ctx, sx + u * 5, hy + hH / 2 - u * 2.9, u * 5.8, 'light');
      ctx.textAlign = 'left'; ctx.font = '700 ' + (u * 4.2) + 'px ' + SANS;
      ctx.fillStyle = '#58a6ff'; ctx.fillText('re', sx + u * 13, hy + hH / 2);
      ctx.fillStyle = '#e6edf3'; ctx.fillText('minal', sx + u * 13 + ctx.measureText('re').width, hy + hH / 2);
      const label = 'live';
      ctx.font = '600 ' + (u * 3.3) + 'px ' + SANS;
      const lw = ctx.measureText(label).width + u * 9;
      rr(ctx, sx + sw - u * 5 - lw, hy + hH / 2 - u * 3.1, lw, u * 6.2, u * 3.1); ctx.fillStyle = 'rgba(63,185,80,.14)'; ctx.fill();
      ctx.strokeStyle = 'rgba(63,185,80,.5)'; ctx.lineWidth = u * 0.25; ctx.stroke();
      ctx.beginPath(); ctx.arc(sx + sw - u * 5 - lw + u * 3.4, hy + hH / 2, u * 1.1, 0, 7); ctx.fillStyle = '#3fb950'; ctx.fill();
      ctx.fillStyle = '#7ee787'; ctx.fillText(label, sx + sw - u * 5 - lw + u * 6, hy + hH / 2 + u * 0.1);
      // The key bar and the home indicator.
      const homeH = u * 7, kbH = u * 11, ky = sy + sh - homeH - kbH;
      ctx.fillStyle = '#161b22'; ctx.fillRect(sx, ky, sw, kbH + homeH);
      const keys = ['↑', '↓', '←', '→', 'Tab', 'Esc', '^C'];
      const gap = u * 1.4, kw = (sw - u * 6 - gap * (keys.length - 1)) / keys.length;
      ctx.font = '500 ' + (u * 3.3) + 'px ' + SANS; ctx.textAlign = 'center';
      keys.forEach((k, i) => {
        const kx = sx + u * 3 + i * (kw + gap);
        rr(ctx, kx, ky + u * 1.8, kw, kbH - u * 3.6, u * 1.6); ctx.fillStyle = '#21262d'; ctx.fill();
        ctx.fillStyle = '#c9d1d9'; ctx.fillText(k, kx + kw / 2, ky + kbH / 2 + u * 0.2);
      });
      rr(ctx, sx + sw / 2 - u * 18, sy + sh - u * 3.4, u * 36, u * 1.3, u * 0.65); ctx.fillStyle = 'rgba(240,243,246,.85)'; ctx.fill();
      // The terminal, at a phone's width.
      const cols = Math.max(20, Math.min(term.cols, 44)), pad = u * 3.2;
      ctx.font = '10px ' + MONO;
      const fpx = Math.floor((sw - pad * 2) / (cols * ctx.measureText('M').width / 10) * 10) / 10;
      const lh = Math.round(fpx * 1.32), top = hy + hH + u * 2.4;
      const rows = phoneRows(cols, Math.max(1, Math.floor((ky - top - u * 1.5) / lh)));
      drawRows(ctx, rows, sx + pad, top, fpx, { cols });
      // A faint sheen across the glass.
      const glare = ctx.createLinearGradient(sx, sy, sx + sw, sy + sh);
      glare.addColorStop(0, 'rgba(255,255,255,.06)'); glare.addColorStop(0.35, 'rgba(255,255,255,0)');
      ctx.fillStyle = glare; ctx.fillRect(sx, sy, sw, sh);
      ctx.restore();
    }
    const BGS = [['#ff9a8b', '#a18cd1'], ['#43cea2', '#185a9d'], ['#f7971e', '#ffd200'], ['#0f2027', '#2c5364'], ['#ee9ca7', '#ffdde1'], ['#141e30', '#243b55']];
    function drawPhone(o) {
      const W = 1080, H = o.tall ? 1920 : 1350, c = mk(W, H), ctx = c.getContext('2d');
      const bg = BGS[o.bg % BGS.length];
      const g = ctx.createLinearGradient(0, 0, W, H);
      g.addColorStop(0, bg[0]); g.addColorStop(1, bg[1]);
      ctx.fillStyle = g; ctx.fillRect(0, 0, W, H);
      const light = o.bg === 4 || o.bg === 2;
      const stamp = scrub(o.stamp || '');
      let top = o.tall ? 150 : 64;
      if (stamp) {
        const f = fitWrap(ctx, stamp, '800 {px}px ' + SANS, 62, 34, W - 160, 2);
        ctx.font = '800 ' + f.px + 'px ' + SANS; ctx.fillStyle = light ? '#1b1f24' : '#fff';
        ctx.textAlign = 'center'; ctx.textBaseline = 'top';
        f.lines.forEach((l, i) => ctx.fillText(l, W / 2, top + i * f.px * 1.12));
        top += f.lines.length * f.px * 1.12 + 10;
        if (o.time) {
          ctx.font = '500 28px ' + SANS; ctx.globalAlpha = 0.8;
          ctx.fillText(day(Date.now()) + '  ·  ' + clock(Date.now()), W / 2, top);
          ctx.globalAlpha = 1; top += 44;
        }
      }
      const bottom = H - (o.tall ? 170 : 96);
      let ph = bottom - top - 36, pw = ph / 2.06;
      if (pw > W * 0.62) { pw = W * 0.62; ph = pw * 2.06; }
      drawDevice(ctx, (W - pw) / 2, top + 30, pw, ph, cur() || {});
      watermark(ctx, W, H, light ? 'dark' : 'light', H - (o.tall ? 90 : 44));
      return c;
    }

    // ---------------------------------------------------------- 4. wrapped
    function periodRuns(period) {
      const all = loadRuns();
      const now = Date.now();
      const since = period === 'week' ? now - 7 * 864e5 : period === 'month' ? now - 30 * 864e5 : 0;
      return all.filter(r => r.end >= since);
    }
    function lateness(t) { const d = new Date(t); return ((d.getHours() + 24 - 5) % 24) * 60 + d.getMinutes(); }
    function wrappedSlides(period) {
      const R = periodRuns(period);
      const label = period === 'week' ? 'week' : period === 'month' ? 'month' : 'time';
      const S = [];
      const total = R.reduce((a, r) => a + (r.end - r.start), 0);
      const away = R.reduce((a, r) => a + (r.away || 0), 0);
      const machines = new Set(R.map(r => r.host || '?')).size;
      const approvals = R.flatMap(r => r.approvals || []);
      const latest = approvals.slice().sort((a, b) => lateness(b.at) - lateness(a.at))[0];
      const longest = R.slice().sort((a, b) => (b.end - b.start) - (a.end - a.start))[0];
      const changed = R.reduce((a, r) => a + (r.filesChanged || 0), 0);
      const commits = R.reduce((a, r) => a + (r.commits || 0), 0);
      const gained = R.reduce((a, r) => a + Math.max(0, (r.passLast || 0) - (r.passFirst || 0)), 0);
      if (!R.length) {
        S.push({ kicker: 'YOUR ' + label.toUpperCase() + ' WITH REMINAL', big: 'Nothing yet', sub: 'No agent runs were recorded on this device in this period. Start an agent in a session and come back.' });
        return S;
      }
      S.push({ kicker: 'YOUR ' + label.toUpperCase() + ' WITH REMINAL', big: dur(total), sub: 'of agent work, across ' + plural(R.length, 'run') + (machines > 1 ? ' on ' + machines + ' machines' : '') + '.' });
      if (away > 60000) S.push({ kicker: 'WHILE YOU WERE AWAY', big: dur(away), sub: 'your agents kept working with this screen off. You did not need to watch.' });
      if (longest) S.push({ kicker: 'LONGEST RUN', big: dur(longest.end - longest.start), sub: 'On ' + day(longest.start) + (longest.filesChanged ? ', ' + plural(longest.filesChanged, 'file') + ' changed' : '') + '.' });
      if (latest) S.push({ kicker: 'LATEST APPROVAL', big: clock(latest.at), sub: 'You said yes ' + (latest.phone ? 'from your phone ' : '') + 'on ' + day(latest.at) + '. ' + plural(approvals.length, 'approval') + ' in all.' });
      if (changed || commits || gained) S.push({ kicker: 'SHIPPED', big: changed ? plural(changed, 'file') : plural(commits, 'commit'), sub: [changed && commits ? plural(commits, 'commit') : '', gained ? gained + ' more tests passing' : ''].filter(Boolean).join(' · ') || 'changed by your agents.' });
      const lateCount = approvals.filter(a => { const h = new Date(a.at).getHours(); return h >= 0 && h < 5; }).length;
      let p;
      if (lateCount >= 2) p = ['The 3 am Approver', '☾', 'Your agents never sleep, and apparently neither do you.'];
      else if (longest && longest.end - longest.start > 3600e3) p = ['The Marathoner', '∞', 'You hand over big jobs and let them run.'];
      else if (away > total * 0.5) p = ['The Walk-Away Engineer', '↗', 'You start the work and go live your life.'];
      else if (R.length >= 15) p = ['The Delegator', '⚡', 'Many small jobs, handed out fast.'];
      else p = ['The Pair Programmer', '⌘', 'You and your agent, side by side.'];
      S.push({ kicker: 'YOUR AGENT PERSONALITY', big: p[0], glyph: p[1], sub: p[2] });
      return S;
    }
    const WRAP_BG = [['#7928ca', '#ff0080'], ['#0070f3', '#00dfd8'], ['#f5a623', '#f81ce5'], ['#11998e', '#38ef7d'], ['#fc466b', '#3f5efb'], ['#ff512f', '#dd2476'], ['#1d2b64', '#f8cdda']];
    function drawSlide(sl, i, n) {
      const W = 1080, H = 1920, c = mk(W, H), ctx = c.getContext('2d');
      const bg = WRAP_BG[i % WRAP_BG.length];
      const g = ctx.createLinearGradient(0, 0, W, H);
      g.addColorStop(0, bg[0]); g.addColorStop(1, bg[1]);
      ctx.fillStyle = g; ctx.fillRect(0, 0, W, H);
      ctx.globalAlpha = 0.12; ctx.fillStyle = '#fff';
      for (let k = 0; k < 6; k++) { ctx.beginPath(); ctx.arc((k * 397 + i * 211) % W, (k * 631 + i * 157) % H, 120 + (k * 53) % 200, 0, 7); ctx.fill(); }
      ctx.globalAlpha = 1;
      for (let k = 0; k < n; k++) { rr(ctx, 60 + k * ((W - 120) / n), 60, (W - 120) / n - 10, 8, 4); ctx.fillStyle = k <= i ? '#fff' : 'rgba(255,255,255,.35)'; ctx.fill(); }
      ctx.fillStyle = '#fff'; ctx.textAlign = 'left'; ctx.textBaseline = 'top';
      ctx.font = '800 44px ' + SANS; ctx.fillText(sl.kicker, 80, 520);
      let y = 600;
      if (sl.glyph) { ctx.font = '400 220px ' + SANS; ctx.fillText(sl.glyph, 70, 300); }
      const f = fitWrap(ctx, sl.big, '900 {px}px ' + SANS, 190, 70, W - 160, 3);
      ctx.font = '900 ' + f.px + 'px ' + SANS;
      f.lines.forEach((l, k) => ctx.fillText(l, 76, y + k * f.px * 1.02));
      y += f.lines.length * f.px * 1.02 + 50;
      const f2 = fitWrap(ctx, sl.sub, '600 {px}px ' + SANS, 54, 34, W - 160, 6);
      ctx.font = '600 ' + f2.px + 'px ' + SANS; ctx.globalAlpha = 0.92;
      f2.lines.forEach((l, k) => ctx.fillText(l, 80, y + k * f2.px * 1.3));
      ctx.globalAlpha = 1;
      watermark(ctx, W, H, 'light', H - 110);
      return c;
    }

    // -------------------------------------------------------- 2. timelapse
    function pickMime() {
      const want = ['video/mp4;codecs=avc1.42E01E', 'video/mp4', 'video/webm;codecs=vp9', 'video/webm;codecs=vp8', 'video/webm'];
      for (const m of want) { try { if (window.MediaRecorder && MediaRecorder.isTypeSupported(m)) return m; } catch (_) {} }
      return '';
    }
    function activity(text) {
      if (/(\d+) (tests? )?(passed|passing)|all tests pass|^ok\s/im.test(text)) return 'tests green ✓';
      if (/\[[\w.\/-]+ [0-9a-f]{7,12}\]/.test(text)) return 'committed';
      if (/(go|npm|pnpm|yarn|cargo) test|pytest|jest|vitest/i.test(text)) return 'running tests';
      if (/\b(Update|Edit|Write|MultiEdit)\(|Edited /.test(text)) return 'editing code';
      if (/\bRead\(|Grep\(|Glob\(|Explored/.test(text)) return 'reading the code';
      if (/\bBash\(|Ran /.test(text)) return 'running commands';
      return '';
    }
    let lapseBusy = false;
    async function makeTimelapse(canvas, o, onState) {
      const s = cur();
      if (!s || !s.log.length) throw new Error('Nothing recorded in this session yet.');
      const mime = pickMime();
      if (!mime || !canvas.captureStream) throw new Error('This browser cannot record video.');
      const t0 = o.from, t1 = o.to || Date.now();
      const before = s.log.filter(e => e.t < t0 && e.b);
      const events = s.log.filter(e => e.t >= t0 && e.t <= t1);
      if (!events.some(e => e.b)) throw new Error('No output in that stretch.');
      const W = canvas.width, H = canvas.height, ctx = canvas.getContext('2d');
      const cols = term.cols, rows = term.rows;
      const host = document.createElement('div');
      host.style.cssText = 'position:fixed;left:-20000px;top:0;width:2000px;height:2000px;visibility:hidden;pointer-events:none';
      document.body.appendChild(host);
      const ht = new Terminal({ cols, rows, scrollback: 200, allowProposedApi: true });
      ht.open(host);
      const write = b => new Promise(res => ht.write(b, res));
      try {
        // A first pass finds the widest line the stretch prints, so the clip
        // crops the empty right side of a wide terminal and its text is larger.
        let widest = 20;
        const measure = () => {
          const b = ht.buffer.active;
          for (let y = 0; y < b.length; y++) {
            const l = b.getLine(y);
            const t = l ? l.translateToString(true) : '';
            if (/^[\s─━═╭╮╰╯│┌┐└┘-]*$/.test(t)) continue; // a rule spans the width whatever it holds
            widest = Math.max(widest, t.length);
          }
        };
        for (const e of events) if (e.b) await write(e.b);
        measure();
        ht.reset();
        for (const e of before.slice(-400)) await write(e.b);
        // Idle stretches are squeezed to 0.4 s, then the whole fits the length asked for.
        let vt = 0, prev = events[0].t;
        for (const e of events) { vt += Math.min(e.t - prev, 400); e.vt = vt; prev = e.t; }
        vt = Math.max(vt, 1);
        const END = 1800, FPS = 30, frameMs = 1000 / FPS;
        const playMs = o.seconds * 1000 - END;
        const scale = Math.max(1, vt / playMs);
        const realMs = t1 - t0;
        const speed = Math.max(1, Math.round(realMs / (o.seconds * 1000)));
        // From under the caption (330) to above the progress bar (H - 190), less the panel's padding.
        const avail = H - 190 - 330 - 60;
        // A wide terminal's lines are rewrapped to a phone's width, as the phone
        // shot does: 170 columns in a 9:16 frame would be a grey smear.
        const WRAP = 64, rewrapped = widest + 1 > 80;
        const visCols = rewrapped ? WRAP : Math.min(cols, Math.max(40, widest + 1));
        const pxW = Math.floor((W - 90) / (visCols * 0.602));
        const fpx = rewrapped ? Math.max(8, Math.min(30, pxW)) : Math.max(8, Math.min(30, pxW, Math.floor(avail / (rows * 1.32))));
        const showRows = rewrapped ? Math.floor(avail / Math.round(fpx * 1.32)) : Math.min(rows, Math.floor(avail / Math.round(fpx * 1.32)));
        const stream = canvas.captureStream(FPS);
        const rec = new MediaRecorder(stream, { mimeType: mime, videoBitsPerSecond: 8e6 });
        const chunks = [];
        rec.ondataavailable = ev => { if (ev.data && ev.data.size) chunks.push(ev.data); };
        const stopped = new Promise(res => { rec.onstop = res; });
        let idx = 0, caption = 'getting started', capAt = 0, waiting = false, realAt = t0;
        const frame = (vnow, final) => {
          const g = ctx.createLinearGradient(0, 0, W, H);
          g.addColorStop(0, '#161b22'); g.addColorStop(1, '#0d1117');
          ctx.fillStyle = g; ctx.fillRect(0, 0, W, H);
          ctx.fillStyle = '#e6edf3'; ctx.textAlign = 'left'; ctx.textBaseline = 'top';
          ctx.font = '800 54px ' + SANS;
          ctx.fillText(o.title ? scrub(o.title) : 'My agent at work', 60, 90);
          ctx.font = '600 34px ' + SANS; ctx.fillStyle = '#8b949e';
          ctx.fillText(speed + '× speed  ·  ' + dur(realMs) + ' of work', 60, 162);
          const cap = waiting ? 'waiting for you…' : caption;
          ctx.font = '700 34px ' + SANS;
          const cw = ctx.measureText(cap).width + 56;
          rr(ctx, 60, 230, cw, 64, 32); ctx.fillStyle = waiting ? '#9e6a03' : '#238636'; ctx.fill();
          ctx.fillStyle = '#fff'; ctx.textBaseline = 'middle'; ctx.fillText(cap, 88, 263);
          const el = realAt - t0;
          const hh = Math.floor(el / 3600e3), mm = Math.floor(el / 60e3) % 60, ss = Math.floor(el / 1000) % 60;
          ctx.font = '700 34px ' + MONO; ctx.fillStyle = '#e6edf3'; ctx.textAlign = 'right';
          ctx.fillText('+' + hh + ':' + String(mm).padStart(2, '0') + ':' + String(ss).padStart(2, '0'), W - 60, 263);
          const ty = 330;
          rr(ctx, 30, ty, W - 60, showRows * Math.round(fpx * 1.32) + 40, 24); ctx.fillStyle = '#0d1117'; ctx.fill();
          ctx.strokeStyle = '#30363d'; ctx.lineWidth = 2; ctx.stroke();
          const b = ht.buffer.active;
          ctx.save();
          rr(ctx, 30, ty, W - 60, showRows * Math.round(fpx * 1.32) + 40, 24); ctx.clip();
          const shown = rewrapped ? phoneRows(WRAP, showRows, ht) : snapRows(ht, b.baseY + rows - showRows, showRows);
          drawRows(ctx, shown, 45, ty + 20, fpx, { cols: visCols });
          ctx.restore();
          const pct = Math.min(1, vnow / Math.max(1, vt));
          rr(ctx, 60, H - 190, W - 120, 10, 5); ctx.fillStyle = '#30363d'; ctx.fill();
          rr(ctx, 60, H - 190, (W - 120) * pct, 10, 5); ctx.fillStyle = '#3fb950'; ctx.fill();
          watermark(ctx, W, H, 'light', H - 100);
          if (final) {
            ctx.fillStyle = 'rgba(13,17,23,.86)'; ctx.fillRect(0, 0, W, H);
            ctx.fillStyle = '#fff'; ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
            ctx.font = '900 120px ' + SANS; ctx.fillText(dur(realMs), W / 2, H / 2 - 160);
            ctx.font = '600 46px ' + SANS; ctx.fillStyle = '#c9d1d9';
            ctx.fillText('of work in ' + o.seconds + ' seconds', W / 2, H / 2 - 50);
            const r = s.runs.find(x => x.start >= t0 - 1000 && x.start <= t1) || null;
            const bits = r ? [r.filesChanged ? plural(r.filesChanged, 'file') + ' changed' : '', r.passLast != null ? r.passLast + ' tests passing' : '', r.commits ? plural(r.commits, 'commit') : ''].filter(Boolean) : [];
            ctx.font = '700 44px ' + SANS; ctx.fillStyle = '#3fb950';
            bits.forEach((t, k) => ctx.fillText(t, W / 2, H / 2 + 60 + k * 64));
            watermark(ctx, W, H, 'light', H - 100);
          }
        };
        frame(0, false);
        rec.start(250);
        const startAt = performance.now();
        let k = 0;
        while (idx < events.length) {
          const vnow = (performance.now() - startAt) * scale;
          const batch = [];
          let text = '';
          while (idx < events.length && events[idx].vt <= vnow) {
            const e = events[idx++];
            realAt = e.t;
            if (e.b) { batch.push(e.b); }
            else if (e.attn !== undefined) waiting = e.attn === 'input';
          }
          if (batch.length) {
            let n = 0; for (const b of batch) n += b.byteLength;
            const all = new Uint8Array(n); let off = 0;
            for (const b of batch) { all.set(b, off); off += b.byteLength; }
            await write(all);
            text = stripAnsi(new TextDecoder().decode(all));
            const a = activity(text);
            if (a && (a !== caption) && performance.now() - capAt > 700) { caption = a; capAt = performance.now(); }
          }
          frame(vnow, false);
          if (onState) onState(Math.min(0.97, vnow / vt));
          k = Math.max(k + 1, Math.floor((performance.now() - startAt) / frameMs));
          await new Promise(r => setTimeout(r, Math.max(0, startAt + k * frameMs - performance.now())));
        }
        // The last frame of work, before the end card, is the clip's poster.
        frame(vt, false);
        if (o.poster) o.poster.getContext('2d').drawImage(canvas, 0, 0);
        const endAt = performance.now();
        while (performance.now() - endAt < END) { frame(vt, true); await new Promise(r => setTimeout(r, frameMs)); }
        rec.stop();
        await stopped;
        stream.getTracks().forEach(t => t.stop());
        const type = mime.split(';')[0];
        return new Blob(chunks, { type });
      } finally {
        ht.dispose();
        host.remove();
      }
    }

    // --------------------------------------------------------------- share
    function stampName(kind, ext) {
      const d = new Date();
      return 'reminal-' + kind + '-' + d.getFullYear() + String(d.getMonth() + 1).padStart(2, '0') + String(d.getDate()).padStart(2, '0') + '-' + String(d.getHours()).padStart(2, '0') + String(d.getMinutes()).padStart(2, '0') + '.' + ext;
    }
    const toBlob = c => new Promise(res => c.toBlob(res, 'image/png'));
    async function shareFiles(files, text) {
      try {
        if (navigator.canShare && navigator.canShare({ files })) {
          await navigator.share({ files, text: text || 'made with reminal · https://reminal.app' });
          return 'shared';
        }
      } catch (e) { if (e && e.name === 'AbortError') return 'cancelled'; }
      for (const f of files) saveFile(f);
      return 'saved';
    }
    function saveFile(f) {
      const url = URL.createObjectURL(f);
      const a = document.createElement('a');
      a.href = url; a.download = f.name;
      document.body.appendChild(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 30000);
    }
    // copyImage takes the picture or a promise of it. Safari wants the promise
    // (written inside the tap); a browser that refuses one gets the picture.
    async function copyImage(blob) {
      if (!navigator.clipboard || !window.ClipboardItem) return false;
      try { await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]); return true; } catch (_) {}
      try { await navigator.clipboard.write([new ClipboardItem({ 'image/png': await blob })]); return true; } catch (_) { return false; }
    }

    // ------------------------------------------------------------------ UI
    // One share sheet. Its home holds the invite to this session and a live
    // preview of each thing that can be made from it; each maker opens in the
    // same sheet, with Back to the home.
    let sheet, back, foot, state = { tab: 'home' };
    // The last text selected in the terminal, offered for a meme while it is
    // fresh: one selected this morning is not what a meme opened now is about.
    let lastSelection = '', lastSelectionAt = 0;
    const SELECTION_FRESH = 3 * 60 * 1000;
    function recentSelection() { return Date.now() - lastSelectionAt < SELECTION_FRESH ? lastSelection : ''; }
    const PRESETS = ['Shipped from the couch', 'Fixed prod from a wedding', 'Deployed from a hammock', 'Approved from bed', 'Coding from the gym', 'Merged on the train'];
    const RECEIPT_BG = [['#1f6feb', '#8957e5', 0], ['#f78166', '#ffd33d', 0], ['#0d1117', '#30363d', 1], ['#2ea043', '#a2f5bf', 0]];
    const MAKERS = [
      ['receipt', 'Receipt', 'What the agent did, on one card'],
      ['lapse', 'Timelapse', 'A run, sped up into a clip'],
      ['meme', 'Meme', 'The agent’s best line, as a meme'],
      ['phone', 'Phone shot', 'This terminal, on a phone'],
      ['wrapped', 'Wrapped', 'Your week with agents, as stories'],
    ];
    const PRIVACY = 'Made on this device. Secrets, tokens and keys, e-mail and IP addresses, home folders, user names, this machine’s name and the session’s id and PIN are hidden before anything is shared. Look before you post: a name or a client in your own text can’t always be told apart.';

    function el(tag, attrs, kids) {
      const e = document.createElement(tag);
      for (const k in (attrs || {})) {
        if (k === 'class') e.className = attrs[k];
        else if (k === 'text') e.textContent = attrs[k];
        else if (k.startsWith('on')) e.addEventListener(k.slice(2), attrs[k]);
        else e.setAttribute(k, attrs[k]);
      }
      for (const kid of (kids || [])) if (kid) e.appendChild(typeof kid === 'string' ? document.createTextNode(kid) : kid);
      return e;
    }
    function ensureSheet() {
      if (sheet) return;
      back = el('div', { class: 'mo-back', onclick: requestClose });
      sheet = el('div', { class: 'mo-sheet', role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Share', tabindex: '-1' });
      document.body.appendChild(back);
      document.body.appendChild(sheet);
      // Typing in a maker's settings, or picking one of its chips, colours or
      // boxes, is work the person would lose by closing.
      sheet.addEventListener('input', e => { if (state.tab !== 'home' && e.target.closest('.mo-controls')) dirty = true; });
      sheet.addEventListener('click', e => { if (state.tab !== 'home' && e.target.closest('.mo-controls .mo-chip, .mo-controls .mo-swatch, .mo-controls .mo-check')) dirty = true; }, true);
      // A field's change re-renders the sheet as the field loses focus, which
      // is in the middle of a tap on one of the sheet's buttons (Save, a
      // chip): rebuilt mid-tap, that button would never get its click, and
      // Save took two taps. The re-render waits for the tap, runs first, and
      // the tap goes to the new copy of the button, which sees what was typed.
      sheet.addEventListener('pointerdown', () => { pressing = true; clearTimeout(pressTimer); }, true);
      // The tap may end outside the sheet: its end is listened for everywhere.
      window.addEventListener('pointerup', () => { clearTimeout(pressTimer); pressTimer = setTimeout(endPress, 600); }, true);
      window.addEventListener('pointercancel', endPress, true);
      sheet.addEventListener('click', e => {
        clearTimeout(pressTimer);
        pressing = false;
        if (!pendingRender) return;
        pendingRender = false;
        const sig = b => [b.className, b.textContent, b.getAttribute('aria-label')].join('\n');
        // The same button among its namesakes (two chips with one label): by
        // its place among them.
        const namesakes = key => [...sheet.querySelectorAll('button')].filter(x => sig(x) === key);
        const b = e.target.closest('button');
        const key = b && sig(b), nth = b ? namesakes(key).indexOf(b) : -1;
        render();
        if (!key) return;
        const twin = namesakes(key)[nth];
        if (!twin) return;
        e.stopImmediatePropagation(); e.preventDefault();
        twin.click();
      }, true);
      // Capture, ahead of the page's own handlers: while the sheet is open it
      // takes Escape, and Tab goes round its own controls, never off it into
      // the page behind the backdrop.
      document.addEventListener('keydown', e => {
        if (!sheet.classList.contains('open')) return;
        if (e.key === 'Tab') { trapTab(e); return; }
        if (e.key !== 'Escape') return;
        e.preventDefault(); e.stopImmediatePropagation();
        if (confirmEl) { keepEditing(); return; }
        if (lapseBusy) return;
        if (state.tab !== 'home') { state.tab = 'home'; render(); } else requestClose();
      }, true);
    }
    // A maker someone has typed in or picked things in asks before the sheet
    // closes, so a stray tap on the backdrop doesn't throw that away. Copying,
    // saving or sharing the picture counts as done with it.
    let dirty = false, confirmEl = null;
    let pressing = false, pendingRender = false, pressTimer = 0;
    function endPress() {
      pressing = false;
      if (pendingRender) { pendingRender = false; render(); }
    }
    // renderSoon is render for a field's change: now, or after the tap it came in.
    function renderSoon() { if (pressing) pendingRender = true; else render(); }
    // bound makes a maker's field for state[key]: it shows what is kept there,
    // and keeps what is typed or ticked, redrawing the card (but not when
    // quiet: a clip's title shows only once the clip is made).
    function bound(tag, attrs, key, quiet) {
      const f = el(tag, attrs);
      const box = attrs.type === 'checkbox';
      if (box) f.checked = !!state[key]; else f.value = state[key] || '';
      f.addEventListener('change', () => { state[key] = box ? f.checked : f.value; if (!quiet) renderSoon(); });
      return f;
    }
    function requestClose() {
      if (!sheet || lapseBusy) return;
      if (dirty) askDiscard(); else close();
    }
    function askDiscard() {
      if (confirmEl) return;
      // The page's own small dialog (the share card's frame, title, hint and
      // button row; its plain green button and button.danger), so it looks
      // like the viewer's other questions.
      confirmFrom = document.activeElement;
      const keep = el('button', { type: 'button', text: 'Keep editing', onclick: keepEditing });
      const drop = el('button', { type: 'button', class: 'danger', text: 'Discard', onclick: discard });
      confirmEl = el('div', { class: 'mo-confirm', role: 'alertdialog', 'aria-modal': 'true', 'aria-labelledby': 'mo-confirm-t' }, [
        el('div', { class: 'share-card' }, [
          el('div', { class: 'share-title', id: 'mo-confirm-t', text: 'Discard this moment?' }),
          el('div', { class: 'share-hint', text: 'What you typed and picked here will be lost.' }),
          el('div', { class: 'share-actions' }, [drop, keep]),
        ]),
      ]);
      // A tap on the dimmed part around the question is a "keep editing".
      confirmEl.addEventListener('click', e => { if (e.target === confirmEl) keepEditing(); });
      sheet.appendChild(confirmEl);
      keep.focus({ preventScroll: true });
    }
    function dismissConfirm() { if (confirmEl) { confirmEl.remove(); confirmEl = null; } }
    // keepEditing answers the question "keep": it goes, and focus goes back to
    // where it was (or the sheet), not lost to the page.
    let confirmFrom = null;
    function keepEditing() {
      const from = confirmFrom;
      confirmFrom = null;
      dismissConfirm();
      ((from && from.isConnected && sheet.contains(from)) ? from : sheet).focus({ preventScroll: true });
    }
    function discard() {
      dismissConfirm();
      state = { tab: 'home', mode: state.mode, sid: state.sid };
      close();
    }
    const FOCUSABLE = 'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
    function trapTab(e) {
      // With the question up, Tab goes round its two buttons only.
      const root = confirmEl || sheet;
      const all = [...root.querySelectorAll(FOCUSABLE)].filter(n => n.getClientRects().length);
      const at = document.activeElement;
      if (!all.length) { e.preventDefault(); sheet.focus({ preventScroll: true }); return; }
      const first = all[0], last = all[all.length - 1], inside = root.contains(at) && at !== root;
      if (e.shiftKey && (!inside || at === first)) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && (!inside || at === last)) { e.preventDefault(); first.focus(); }
    }
    // Where focus goes back to on close: what had it when the sheet opened (the
    // Share button, say), or Share itself. Never a text field on a phone,
    // which would bring the keyboard up as the sheet goes.
    let opener = null;
    function returnFocus() {
      const o = opener;
      opener = null;
      if (!o || !o.isConnected || (COARSE && /^(INPUT|TEXTAREA)$/.test(o.tagName))) return;
      try { o.focus({ preventScroll: true }); } catch (_) {}
    }
    function open(tab, extra) {
      ensureSheet();
      // Recording a clip: the sheet stays on it, nothing rebuilds it.
      if (lapseBusy) { sheet.classList.add('open'); back.classList.add('open'); return; }
      // Another session: its own quote, clip and choices, not the last one's.
      let sid = ''; try { sid = String(sessionId || '').toUpperCase(); } catch (_) {}
      if (state.sid !== sid) { state = { tab: 'home', mode: state.mode, sid }; dirty = false; }
      state.wrappedAll = null;
      state.tab = tab || 'home';
      Object.assign(state, extra || {});
      const was = sheet.classList.contains('open');
      if (!was) {
        const a = document.activeElement;
        opener = a && a !== document.body && !sheet.contains(a) ? a : document.getElementById('share-btn');
      }
      sheet.classList.add('open'); back.classList.add('open');
      if (!was) { sheet.classList.remove('rise'); void sheet.offsetWidth; sheet.classList.add('rise'); }
      render();
      // Focus moves into the sheet, so the next Tab starts on its controls.
      if (!was) sheet.focus({ preventScroll: true });
    }
    function close() {
      if (!sheet || lapseBusy) return;
      dirty = false;
      dismissConfirm();
      parkInvite();
      sheet.classList.remove('open'); back.classList.remove('open');
      returnFocus();
    }

    // The invite's parts (QR, session, PIN, link, copy buttons) belong to the
    // page's share panel, whose handlers fill and drive them by id. The home
    // borrows them, and puts them back whenever it is not showing.
    let parked = null;
    function inviteParts() {
      if (!parked) {
        const card = document.querySelector('#share-modal .share-card');
        if (!card) return null;
        const nat = document.getElementById('share-native');
        parked = [document.getElementById('share-qr'), card.querySelector('.share-rows'), card.querySelector('.share-msgs'), nat && nat.parentElement]
          .filter(Boolean).map(node => { const at = document.createComment('share'); node.parentNode.insertBefore(at, node); return { node, at }; });
      }
      return parked;
    }
    function parkInvite() { if (parked) for (const p of parked) p.at.parentNode.insertBefore(p.node, p.at); }

    // A focused field blurs as the sheet is rebuilt and fires its change, which
    // would render again inside this render: its value is kept, the rebuild that
    // is already under way shows it.
    let rendering = false;
    function render() {
      if (rendering) return;
      rendering = true;
      try { renderSheet(); } finally { rendering = false; }
    }
    function renderSheet() {
      confirmEl = null; // replaced with the rest below
      parkInvite();
      sheet.replaceChildren();
      const home = state.tab === 'home';
      const maker = MAKERS.find(m => m[0] === state.tab);
      sheet.classList.toggle('editing', !home);
      sheet.dataset.tab = state.tab;
      const head = el('div', { class: 'mo-head' }, [
        home ? null : el('button', { class: 'mo-backbtn', type: 'button', 'aria-label': 'Back to Share', onclick: () => { if (lapseBusy) return; state.tab = 'home'; render(); } },
          [el('span', { class: 'mo-chev', text: '‹' }), 'Share']),
        el('div', { class: 'mo-title', text: home ? 'Share' : maker[1] }),
        el('button', { class: 'mo-x', type: 'button', 'aria-label': 'Close', text: '✕', onclick: requestClose }),
      ]);
      const body = el('div', { class: 'mo-body' });
      foot = el('div', { class: 'mo-foot' });
      sheet.append(head, body);
      if (home) renderHome(body);
      else ({ receipt: tabReceipt, lapse: tabLapse, meme: tabMeme, phone: tabPhone, wrapped: tabWrapped })[state.tab](body);
      if (foot.childNodes.length) sheet.appendChild(foot);
    }

    // The home: Invite or Post, one at a time. The last one picked is kept.
    const MODE_KEY = 'reminal:share:mode';
    const ICONS = {
      receipt: '<path d="M6 3h12v18l-2-1.5L14 21l-2-1.5L10 21l-2-1.5L6 21z"/><path d="M9 8h6M9 12h6M9 16h3"/>',
      lapse: '<rect x="3" y="5" width="18" height="14" rx="3"/><path d="M10 9.5v5l4.5-2.5z"/>',
      meme: '<rect x="3" y="4" width="18" height="16" rx="3"/><path d="M7 9h10M8 15.5h8"/>',
      phone: '<rect x="7" y="2.5" width="10" height="19" rx="2.5"/><path d="M10.5 5h3M11 18.5h2"/>',
      wrapped: '<path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z"/><path d="M19 16l.8 2.2L22 19l-2.2.8L19 22l-.8-2.2L16 19l2.2-.8z"/>',
    };
    function icon(k) {
      const box = el('span', { class: 'mo-ico mo-ico-' + k });
      box.innerHTML = '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' + ICONS[k] + '</svg>';
      return box;
    }
    // freshMaker forgets what maker k chose by itself last time (a meme's line,
    // the run a receipt shows), so opening it again starts from what is newest
    // now. Only when nothing was changed in it: that is kept (see dirty).
    function freshMaker(k) {
      if (k === 'meme') state.memeQuote = state.memeTop = state.memeBottom = state.memeAt = state.memeTpl = undefined;
      if (k === 'receipt') state.runIdx = 0;
    }
    function renderHome(body) {
      if (!state.mode) { try { state.mode = localStorage.getItem(MODE_KEY) || 'invite'; } catch (_) { state.mode = 'invite'; } }
      const pick = m => { state.mode = m; try { localStorage.setItem(MODE_KEY, m); } catch (_) {} render(); };
      body.appendChild(el('div', { class: 'mo-seg', role: 'tablist' }, [['invite', 'Invite'], ['post', 'Post a moment']].map(([m, l]) =>
        el('button', { type: 'button', role: 'tab', class: state.mode === m ? 'on' : '', 'aria-selected': String(state.mode === m), text: l, onclick: () => pick(m) }))));
      if (state.mode === 'invite') {
        const parts = inviteParts();
        if (!parts) return;
        // Reached from a maker that opened on its own (an offer, say): Share fills the invite first.
        const sid = document.getElementById('share-session');
        if (sid && !sid.textContent) setTimeout(() => { const b = document.getElementById('share-btn'); if (b) b.click(); });
        const [qr, ...rest] = parts.map(p => p.node);
        body.appendChild(el('div', { class: 'mo-invite' }, [el('div', { class: 'mo-qr' }, [qr])].concat(rest)));
        body.appendChild(el('div', { class: 'mo-note', text: 'They join this terminal, live, and can type in it.' }));
        return;
      }
      const list = el('div', { class: 'mo-list' });
      const ran = runChoices().length > 0;
      // Wrapped with one short run in it is no brag: it says so until there's more.
      const week = loadRuns().filter(r => Date.now() - r.start < 7 * 864e5);
      const thin = week.length < 3 && week.reduce((t, r) => t + (r.end - r.start), 0) < 30 * 60e3;
      for (const [k, label, sub] of MAKERS) {
        list.appendChild(el('button', { type: 'button', class: 'mo-row', onclick: () => { if (!dirty) freshMaker(k); state.tab = k; render(); } }, [
          icon(k),
          el('span', { class: 'mo-row-txt' }, [el('span', { class: 'mo-row-t', text: label }), el('span', { class: 'mo-row-s', text: k === 'receipt' && !ran ? 'After your agent’s next run' : k === 'wrapped' && thin ? 'Comes alive after a few runs' : sub })]),
          el('span', { class: 'mo-row-go', text: '›' }),
        ]));
      }
      body.appendChild(list);
      body.appendChild(el('div', { class: 'mo-note', text: 'Made on this device. Private details are hidden first.', title: PRIVACY }));
    }

    function preview(body, canvas) {
      const box = el('div', { class: 'mo-preview' });
      canvas.className = 'mo-canvas';
      box.appendChild(canvas);
      body.appendChild(box);
      return box;
    }
    // The maker's buttons sit in the sheet's footer, always in reach.
    // The file is made ahead, just after the preview shows: iOS lets a tap
    // share or copy only while the tap is fresh, and drawing a card at full
    // size can outlast it. keepFile, when given, is what Save keeps (one slide,
    // not seven downloads the browser would block).
    function actions(makeFile, kind, lead, keepFile) {
      let made = null;
      const getFile = () => made || (made = makeFile().catch(e => { made = null; throw e; }));
      setTimeout(() => { if (foot && foot.contains(row)) getFile().catch(() => {}); }, 250);
      const say = el('span', { class: 'mo-said' });
      const flash = t => { say.textContent = t; setTimeout(() => { if (say.textContent === t) say.textContent = ''; }, 2500); };
      const row = el('div', { class: 'mo-actions' }, [say]);
      if (lead) row.appendChild(lead);
      if (kind !== 'video' && kind !== 'slides' && navigator.clipboard && window.ClipboardItem) {
        row.appendChild(el('button', { type: 'button', class: 'mo-btn', text: 'Copy', onclick: async () => {
          // The clipboard takes the picture as a promise, inside the tap.
          const ok = await copyImage(getFile());
          if (ok) dirty = false;
          flash(ok ? 'Copied' : 'Could not copy');
        } }));
      }
      row.appendChild(el('button', { type: 'button', class: 'mo-btn', text: 'Save', onclick: async () => {
        const f = await (keepFile ? keepFile() : getFile()); if (!f) return;
        (Array.isArray(f) ? f : [f]).forEach(saveFile); dirty = false; flash('Saved');
      } }));
      row.appendChild(el('button', { type: 'button', class: 'mo-btn primary', text: 'Share', onclick: async () => {
        const f = await getFile(); if (!f) return;
        const r = await shareFiles(Array.isArray(f) ? f : [f]);
        if (r === 'shared' || r === 'saved') dirty = false;
        flash(r === 'shared' ? 'Shared' : r === 'saved' ? 'Saved to your downloads' : '');
      } }));
      foot.appendChild(row);
    }
    function field(label, input) { return el('label', { class: 'mo-field' }, [el('span', { text: label }), input]); }
    function group(label, kid) { return el('div', { class: 'mo-field' }, [el('span', { text: label }), kid]); }
    function chips(list, onPick, cls) {
      const box = el('div', { class: 'mo-chips ' + (cls || '') });
      list.forEach((c, i) => box.appendChild(el('button', { type: 'button', class: 'mo-chip' + (c.on ? ' on' : ''), text: c.label, title: c.title || c.label, onclick: () => onPick(c, i) })));
      return box;
    }
    function swatches(list, on, onPick) {
      const box = el('div', { class: 'mo-swatches' });
      list.forEach(([a, b, name], i) => box.appendChild(el('button', { type: 'button', class: 'mo-swatch' + (i === on ? ' on' : ''), title: name, 'aria-label': name,
        style: 'background: linear-gradient(135deg, ' + a + ', ' + b + ')', onclick: () => onPick(i) })));
      return box;
    }
    function runChoices() {
      const s = cur();
      const list = [];
      const live = liveRun(s);
      if (live) list.push({ label: 'Running now · ' + dur(live.end - live.start), run: live });
      for (const r of (s ? s.runs.slice().reverse() : [])) list.push({ label: clock(r.start) + ' · ' + dur(r.end - r.start) , run: r });
      return list;
    }
    function controls(body) {
      const c = el('div', { class: 'mo-controls' });
      body.appendChild(c);
      return c;
    }
    function empty(body, title, text) {
      body.appendChild(el('div', { class: 'mo-empty' }, [el('div', { class: 'mo-empty-t', text: title }), el('div', { text: text })]));
    }

    // Receipt
    function tabReceipt(body) {
      const choices = runChoices();
      if (!choices.length) {
        empty(body, 'No agent run here yet', 'Start a coding agent in this terminal. When it finishes a run, its receipt is ready: how long it worked, the files it changed, tests, and when you approved from your phone.');
        return;
      }
      if (state.runIdx == null || state.runIdx >= choices.length) state.runIdx = 0;
      if (state.rbg == null) state.rbg = 0;
      const run = choices[state.runIdx].run;
      const b = RECEIPT_BG[state.rbg];
      const c = drawReceipt(run, { note: state.note || '', showHost: !!state.showHost, bg: [b[0], b[1]], dark: !!b[2] });
      preview(body, c);
      actions(async () => new File([await toBlob(c)], stampName('receipt', 'png'), { type: 'image/png' }));
      const ctl = controls(body);
      if (choices.length > 1) ctl.appendChild(group('Run', chips(choices.map((x, i) => ({ label: x.label, on: i === state.runIdx })), (x, i) => { state.runIdx = i; render(); })));
      const note = bound('input', { type: 'text', maxlength: '80', placeholder: 'e.g. while I was at the gym' }, 'note');
      ctl.appendChild(field('Your line', note));
      ctl.appendChild(group('Colour', swatches(RECEIPT_BG.map((x, i) => [x[0], x[1], ['Violet', 'Sunset', 'Night', 'Mint'][i]]), state.rbg, i => { state.rbg = i; render(); })));
      const host = bound('input', { type: 'checkbox' }, 'showHost');
      ctl.appendChild(el('label', { class: 'mo-check' }, [host, el('span', { text: 'Show the machine’s name' })]));
    }

    // Timelapse
    function tabLapse(body) {
      const s = cur();
      const c = mk(1080, 1920);
      const ctx = c.getContext('2d');
      ctx.fillStyle = '#0d1117'; ctx.fillRect(0, 0, 1080, 1920);
      if (state.lapseLast) ctx.drawImage(state.lapseLast, 0, 0);
      else {
        ctx.fillStyle = '#8b949e'; ctx.font = '600 48px ' + SANS; ctx.textAlign = 'center';
        ctx.fillText('Tap “Make the clip”', 540, 900);
        ctx.font = '38px ' + SANS; ctx.fillText('It plays here while it records.', 540, 970);
      }
      const box = preview(body, c);
      box.classList.add('tall');
      const choices = runChoices();
      const spans = [];
      for (const ch of choices) spans.push({ label: ch.label, from: ch.run.start - 1500, to: ch.run.live ? 0 : ch.run.end + 1500 });
      if (s && s.log.length) {
        const first = (s.log.find(e => !e.old) || s.log[0]).t;
        for (const m of [5, 15, 30]) if (Date.now() - first > m * 60e3 * 0.5) spans.push({ label: 'Last ' + m + ' min', from: Date.now() - m * 60e3, to: 0 });
        spans.push({ label: 'Since you connected · ' + dur(Date.now() - first), from: first, to: 0 });
      }
      if (!spans.length) {
        box.remove();
        empty(body, 'Nothing recorded yet', 'The clip is made from what this viewer has seen since it connected. Leave it open while an agent works, then come back.');
        return;
      }
      if (state.spanIdx == null || state.spanIdx >= spans.length) state.spanIdx = 0;
      if (!state.secs) state.secs = 15;
      const status = el('span', { class: 'mo-said' });
      const go = el('button', { type: 'button', class: 'mo-btn' + (state.lapseFile ? '' : ' primary'), text: state.lapseFile ? 'Remake' : 'Make the clip' });
      if (state.lapseFile) actions(async () => state.lapseFile, 'video', go);
      else foot.appendChild(el('div', { class: 'mo-actions' }, [status, go]));
      const ctl = controls(body);
      ctl.appendChild(group('What', chips(spans.map((x, i) => ({ label: x.label, on: i === state.spanIdx })), (x, i) => { state.spanIdx = i; state.lapseFile = null; state.lapseLast = null; render(); }, 'wrapchips')));
      ctl.appendChild(group('Length', chips([10, 15, 30].map(n => ({ label: n + ' s', n, on: n === state.secs })), x => { state.secs = x.n; render(); })));
      const title = bound('input', { type: 'text', maxlength: '48', placeholder: 'My agent at work' }, 'lapseTitle', true);
      ctl.appendChild(field('Title', title));
      go.addEventListener('click', async () => {
        if (lapseBusy) return;
        lapseBusy = true;
        go.disabled = true; sheet.classList.add('busy');
        status.textContent = 'Recording… 0%';
        if (!status.isConnected) go.parentNode.insertBefore(status, go);
        try {
          const sp = spans[state.spanIdx];
          const poster = mk(1080, 1920);
          const blob = await makeTimelapse(c, { from: sp.from, to: sp.to, seconds: state.secs, title: state.lapseTitle, poster }, p => { status.textContent = 'Recording… ' + Math.round(p * 100) + '%'; });
          const ext = blob.type.includes('mp4') ? 'mp4' : 'webm';
          state.lapseFile = new File([blob], stampName('timelapse', ext), { type: blob.type });
          state.lapseLast = poster;
          lapseBusy = false; sheet.classList.remove('busy');
          render();
        } catch (e) {
          lapseBusy = false; sheet.classList.remove('busy'); go.disabled = false;
          status.textContent = String(e && e.message || e);
        }
      });
    }

    // Meme
    const TEMPLATES = [['caption', 'Caption'], ['twopanel', 'Asked / got'], ['quote', 'Quote'], ['achievement', 'Achievement']];
    // The line a meme shows with nothing of the session's own to use, and the
    // tour's example (sampleMeme).
    const SAMPLE_QUOTE = 'You’re absolutely right! I apologize for the confusion.';
    // sampleMeme is an example meme card, drawn as a real one is: for the tour.
    function sampleMeme() {
      const k = MOMENT_KINDS.find(x => x.kind === 'right');
      return drawMeme({ template: 'caption', quote: SAMPLE_QUOTE, top: k.top, bottom: k.bottom, at: Date.now() });
    }
    function memeDefaults() {
      const s = cur();
      const m = s && s.moments.length ? s.moments[s.moments.length - 1] : null;
      const sel = recentSelection();
      const quote = sel || (m ? m.text : SAMPLE_QUOTE);
      const k = MOMENT_KINDS.find(x => x.kind === (m && !sel ? m.kind : 'right'));
      return { quote, top: k.top, bottom: k.bottom, at: m ? m.at : Date.now() };
    }
    function tabMeme(body) {
      const s = cur();
      const found = s ? s.moments.slice().reverse() : [];
      if (state.memeQuote == null) {
        const d = memeDefaults();
        state.memeQuote = d.quote; state.memeTop = d.top; state.memeBottom = d.bottom; state.memeAt = d.at;
      }
      if (!state.memeTpl) state.memeTpl = 'caption';
      const tplKind = () => MOMENT_KINDS.find(x => x.re.test(state.memeQuote)) || null;
      const c = drawMeme({ template: state.memeTpl, quote: state.memeQuote, top: state.memeTop, bottom: state.memeBottom, at: state.memeAt });
      preview(body, c);
      actions(async () => new File([await toBlob(c)], stampName('meme', 'png'), { type: 'image/png' }));
      const ctl = controls(body);
      ctl.appendChild(group('Template', chips(TEMPLATES.map(([k, l]) => ({ label: l, k, on: k === state.memeTpl })), x => {
        state.memeTpl = x.k;
        const k = tplKind();
        if (x.k === 'twopanel') state.memeTop = 'Fix the failing test';
        else if (x.k === 'achievement') state.memeTop = k ? k.title : 'Mission accomplished';
        else if (x.k === 'quote') state.memeTop = '';
        else if (k) { state.memeTop = k.top; state.memeBottom = k.bottom; }
        render();
      })));
      const picks = [];
      const sel = (() => { try { return term.getSelection() || recentSelection(); } catch (_) { return recentSelection(); } })();
      if (sel && sel.trim()) picks.push({ label: 'Selected text', text: sel.trim().slice(0, 220), title: sel });
      for (const m of found.slice(0, 8)) picks.push({ label: '“' + scrub(m.text).slice(0, 42) + (m.text.length > 42 ? '…' : '') + '”', text: m.text, kind: m.kind, at: m.at, title: scrub(m.text), on: m.text === state.memeQuote });
      if (picks.length) ctl.appendChild(group('From this session', chips(picks, x => {
        state.memeQuote = x.text; state.memeAt = x.at || Date.now();
        const k = MOMENT_KINDS.find(y => y.kind === x.kind) || MOMENT_KINDS.find(y => y.re.test(x.text));
        if (k) { state.memeTop = state.memeTpl === 'achievement' ? k.title : state.memeTpl === 'twopanel' ? state.memeTop : k.top; state.memeBottom = k.bottom; }
        render();
      }, 'wrapchips')));
      else ctl.appendChild(el('div', { class: 'mo-hint', text: 'Lines like “You’re absolutely right!” or “I see the issue now” show up here as the agent says them. You can also select text in the terminal and tap Meme.' }));
      const q = bound('textarea', { rows: '2', maxlength: '240' }, 'memeQuote');
      ctl.appendChild(field('The agent said', q));
      const topLabel = state.memeTpl === 'twopanel' ? 'What I asked' : state.memeTpl === 'quote' ? 'Signed' : state.memeTpl === 'achievement' ? 'Achievement' : 'Top text';
      const t = bound('input', { type: 'text', maxlength: '90' }, 'memeTop');
      ctl.appendChild(field(topLabel, t));
      if (state.memeTpl === 'caption') {
        const bt = bound('input', { type: 'text', maxlength: '90' }, 'memeBottom');
        ctl.appendChild(field('Bottom text', bt));
      }
    }

    // Phone shot
    function tabPhone(body) {
      if (state.stamp == null) state.stamp = PRESETS[0];
      if (state.pbg == null) state.pbg = 0;
      if (state.ptime == null) state.ptime = true;
      const c = drawPhone({ stamp: state.stamp, bg: state.pbg, tall: !!state.ptall, time: state.ptime });
      const box = preview(body, c);
      if (state.ptall) box.classList.add('tall');
      actions(async () => new File([await toBlob(c)], stampName('phone', 'png'), { type: 'image/png' }));
      const ctl = controls(body);
      const st = bound('input', { type: 'text', maxlength: '60' }, 'stamp');
      ctl.appendChild(field('Caption', st));
      ctl.appendChild(chips(PRESETS.map(p => ({ label: p, on: p === state.stamp })), x => { state.stamp = x.label; render(); }));
      ctl.appendChild(group('Background', swatches(BGS.map((b, i) => [b[0], b[1], ['Peach', 'Ocean', 'Gold', 'Slate', 'Rose', 'Midnight'][i]]), state.pbg, i => { state.pbg = i; render(); })));
      ctl.appendChild(group('Size', chips([{ label: 'Post 4:5', v: false, on: !state.ptall }, { label: 'Story 9:16', v: true, on: !!state.ptall }], x => { state.ptall = x.v; render(); })));
      const tm = bound('input', { type: 'checkbox' }, 'ptime');
      ctl.appendChild(el('label', { class: 'mo-check' }, [tm, el('span', { text: 'Show the date and time' })]));
      ctl.appendChild(el('div', { class: 'mo-hint', text: 'Shows the newest lines on the terminal, as a phone shows them. Scroll up first for an earlier part.' }));
    }

    // Wrapped
    function tabWrapped(body) {
      if (!state.period) state.period = 'week';
      const slides = wrappedSlides(state.period);
      if (state.slide == null || state.slide >= slides.length) state.slide = 0;
      const c = drawSlide(slides[state.slide], state.slide, slides.length);
      const box = preview(body, c);
      box.classList.add('tall', 'tappable');
      c.addEventListener('click', ev => {
        const r = c.getBoundingClientRect();
        state.slide = ev.clientX - r.left < r.width / 3 ? Math.max(0, state.slide - 1) : (state.slide + 1) % slides.length;
        render();
      });
      // The set is made once for the period, not again on every slide tapped.
      const all = () => {
        if (state.wrappedAll && state.wrappedAll.period === state.period) return state.wrappedAll.p;
        const p = (async () => {
          const files = [];
          for (let i = 0; i < slides.length; i++) files.push(new File([await toBlob(drawSlide(slides[i], i, slides.length))], stampName('wrapped-' + (i + 1), 'png'), { type: 'image/png' }));
          return files;
        })();
        state.wrappedAll = { period: state.period, p };
        p.catch(() => { if (state.wrappedAll && state.wrappedAll.p === p) state.wrappedAll = null; });
        return p;
      };
      const one = async () => new File([await toBlob(c)], stampName('wrapped-' + (state.slide + 1), 'png'), { type: 'image/png' });
      actions(all, 'slides', null, one);
      const ctl = controls(body);
      ctl.appendChild(group('Period', chips([['week', 'This week'], ['month', 'This month'], ['all', 'All time']].map(([v, l]) => ({ label: l, v, on: v === state.period })), x => { state.period = x.v; state.slide = 0; render(); })));
      ctl.appendChild(el('div', { class: 'mo-hint', text: 'Slide ' + (state.slide + 1) + ' of ' + slides.length + '. Tap the right of the slide for the next, the left to go back. Share sends them all; Save keeps this one. Counts runs this device saw.' }));
    }

    // Offers, at the corner of the terminal: a run just finished, or a line worth a meme.
    let chipEl = null, lastOffer = 0;
    function offer(text, act, ms) {
      if (sheet && sheet.classList.contains('open')) return;
      const wrapEl = document.getElementById('terminal-wrap');
      if (!wrapEl) return;
      if (chipEl) chipEl.remove();
      chipEl = el('button', { type: 'button', class: 'mo-offer', onclick: () => { chipEl.remove(); chipEl = null; act(); } }, [el('span', { class: 'mo-spark', text: '✦' }), text]);
      wrapEl.appendChild(chipEl);
      const mine = chipEl;
      setTimeout(() => { if (chipEl === mine) { mine.classList.add('gone'); setTimeout(() => mine.remove(), 400); chipEl = null; } }, ms || 12000);
    }
    function offerReceipt(s, p) {
      try { if (!sessionId || s.id !== sessionId.toUpperCase()) return; } catch (_) { return; }
      lastOffer = Date.now();
      offer('Run done · ' + dur(p.end - p.start) + ' · get the receipt', () => open('receipt', { runIdx: 0 }), 15000);
    }
    function offerMoment(s, k, text) {
      try { if (!sessionId || s.id !== sessionId.toUpperCase()) return; } catch (_) { return; }
      // One meme offer a run, at most: the pill is a nudge, not a feed.
      if (s.run && s.run.memeOffered) return;
      if (Date.now() - lastOffer < 90000) return;
      lastOffer = Date.now();
      if (s.run) s.run.memeOffered = true;
      offer('Meme moment: “' + scrub(text).slice(0, 34) + (text.length > 34 ? '…' : '') + '”', () => open('meme', { memeQuote: text, memeTop: k.top, memeBottom: k.bottom, memeAt: Date.now(), memeTpl: 'caption' }));
    }

    function init() {
      const mb = document.getElementById('sel-meme');
      if (mb) {
        let picked = '';
        mb.addEventListener('pointerdown', () => { try { picked = term.getSelection(); } catch (_) { picked = ''; } });
        mb.addEventListener('click', () => {
          const t = (picked || recentSelection() || '').trim();
          if (t) open('meme', { memeQuote: t.slice(0, 220), memeTpl: 'caption', memeTop: (MOMENT_KINDS.find(k => k.re.test(t)) || {}).top || 'MY AGENT, LIVE:', memeBottom: (MOMENT_KINDS.find(k => k.re.test(t)) || {}).bottom || '', memeAt: Date.now() });
        });
      }
      try { term.onSelectionChange(() => { const t = term.getSelection(); if (t && t.trim()) { lastSelection = t.trim().slice(0, 220); lastSelectionAt = Date.now(); } }); } catch (_) {}
    }
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init); else init();

    const isOpen = () => !!(sheet && sheet.classList.contains('open'));
    window.reminalMoments = { output, attn, input, open, isOpen, scrub, sampleMeme };
  })();
