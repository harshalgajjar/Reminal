    window.__REMINAL_WEB = '3.5.3';
    const statusEl = document.getElementById('status');
    const sessionInput = document.getElementById('session-input');
    const pinInput = document.getElementById('pin-input');
    const connectBtn = document.getElementById('connect-btn');
    const overlay = document.getElementById('overlay');
    const overlayText = document.getElementById('overlay-text');

    const params = new URLSearchParams(location.search);
    const initialSession = params.get('s') || '';
    // Standalone pane mode: this tab IS one mirrored window and nothing else.
    // Pop-out navigates here instead of driving a canvas inside another
    // document, so the tab you are looking at owns its socket, decoder, acks
    // and timers. Nothing it needs can be throttled or frozen by the platform
    // on behalf of some other tab — which is what broke pop-out on Android,
    // where window.open yields a TAB and the opener stops executing.
    // No PIN in the URL: an owner connects PIN-free, and otherwise this tab
    // inherits a copy of the opener's sessionStorage credential. A link
    // pasted onto another device carries no secret and simply prompts.
    const paneOnly = params.get('pane') || '';
    if (paneOnly) { document.documentElement.classList.add('pane-only'); document.body.classList.add('pane-only'); }
    // A pane tab shows exactly one mirrored window and nothing else. It does
    // NOT own the terminal: its xterm is display:none, so it cannot measure
    // itself, and anything it reports about size is a guess the whole session
    // would then have to live with. Named once here rather than re-tested at
    // each call site — the layout save already skipped a pane tab for the same
    // reason ("a pane tab owns no layout") while resume/fit/resize did not,
    // which is exactly how the two came apart.
    const ownsTerminal = !paneOnly;
    // Stable for the life of this tab. Sent on every resize so the agent can
    // take min(width)×min(height) across attached viewers instead of letting
    // the latest report overwrite the others (laptop + phone keyboard).
    const viewerId = 'v-' + ((crypto.randomUUID && crypto.randomUUID()) || String(Math.random()).slice(2)).slice(0, 12);
    if (initialSession) sessionInput.value = initialSession.toUpperCase();

    // The PIN may arrive in the URL fragment (e.g. #p=123456) from the
    // QR code printed by the agent. Fragments never leave the browser, so
    // this is safer than putting the PIN in the query string. We strip the
    // fragment from the URL bar after reading it.
    const hashParams = new URLSearchParams(location.hash.replace(/^#/, ''));
    let initialPin = hashParams.get('p') || '';
    if (initialPin) {
      pinInput.value = initialPin;
      history.replaceState(null, '', location.pathname + location.search);
    }

    // ---- Opening a session in its own tab -----------------------------------
    // A URL that connects to a session on its own, used as the href behind every
    // row in the dropdown so ⌘/Ctrl-click, middle-click and "Open link in new
    // tab" all do the obvious thing. It carries the session id and NOTHING else:
    // the new tab re-derives its credential locally — the owner device key from
    // IndexedDB for a machine you own, or the remembered PIN from Recents (see
    // historyPin) — both of which are shared across tabs of this origin. So a
    // link that leaks (copied, bookmarked, pasted) leaks no secret.
    // Declared up here, above every panel that paints rows, so NEW_TAB_KEY is
    // initialised before any of them can read it.
    function sessionHref(id) {
      return location.pathname + '?s=' + encodeURIComponent(String(id).toUpperCase());
    }
    // ⌘ on Apple, Ctrl elsewhere — used only for the hover hints.
    const NEW_TAB_KEY = /Mac|iP(hone|ad|od)/.test(navigator.platform || navigator.userAgent) ? '⌘' : 'Ctrl';
    // "Open this somewhere other than here": ⌘/Ctrl (new tab), Shift (new
    // window), middle-click, Alt (download). All of them mean the click was
    // never a request to switch THIS tab's session.
    // reminalLinks finds URLs in terminal rows, including a URL broken across
    // rows. Two kinds of break: one the terminal made itself (the row is
    // flagged wrapped, so the next row is the same line), and one the
    // program made — Claude Code wraps its own text and prints a real
    // newline when a URL reaches the margin, then indents the rest. That
    // second kind leaves no flag, so it is read from the shape: the row is
    // full to the margin its neighbours share, the URL runs to its end, and
    // the next row starts with an indent and a run of URL characters.
    const reminalLinks = (function () {
      const URL_START = /https?:\/\/[^\s<>"'`]+/g;
      const URL_CHARS = /^[A-Za-z0-9\-._~:\/?#\[\]@!$&'()*+,;=%]+/;
      const TRAIL = /[.,;:!?'"]+$/;
      // A closing bracket at the very end belongs to the prose around the
      // URL unless the URL opened it: "(https://a/b)" ends before the ")".
      function trimTrail(url) {
        for (;;) {
          const before = url;
          url = url.replace(TRAIL, '');
          while (/[)\]}]$/.test(url)) {
            const close = url[url.length - 1], open = { ')': '(', ']': '[', '}': '{' }[close];
            const opens = url.split(open).length - 1, closes = url.split(close).length - 1;
            if (closes > opens) url = url.slice(0, -1); else break;
          }
          if (url === before) return url;
        }
      }
      // Rows the terminal wrapped form one logical line; the program's
      // own line breaks separate logical lines. Everything below works in
      // logical lines and maps back to rows at the end.
      function logicalAt(rows, i) {
        let top = i, bot = i;
        while (top > 0 && rows[top].wrapped) top--;
        while (bot + 1 < rows.length && rows[bot + 1].wrapped) bot++;
        return { top, bot };
      }
      function textOf(rows, L) {
        let t = '';
        for (let k = L.top; k <= L.bot; k++) t += rows[k].text;
        return t;
      }
      // The width the program wrapped at: the longest logical line in the
      // same paragraph, this one excluded. The paragraph, not the whole
      // neighbourhood: a screen that has been wider holds older lines
      // wrapped at that width, and against those nothing written at a
      // phone's width ever looks full. A line that reaches the margin
      // (within a column) was cut by it, not ended by the program.
      function marginAround(rows, L) {
        let m = 0;
        const blank = k => !rows[k].text.trim();
        let k = L.top - 1, seen = 0;
        while (k >= 0 && !blank(k) && seen < 30) { const M = logicalAt(rows, k); m = Math.max(m, textOf(rows, M).length); k = M.top - 1; seen++; }
        k = L.bot + 1; seen = 0;
        while (k < rows.length && !blank(k) && seen < 30) { const M = logicalAt(rows, k); m = Math.max(m, textOf(rows, M).length); k = M.bot + 1; seen++; }
        return m;
      }
      // marginFloor is the narrowest a row may be and still count as cut by
      // the margin. A short paragraph sets no margin of its own, and without
      // this a sentence ending in a URL would take the indented line under
      // it. Phones start at about 45 columns.
      const marginFloor = 40;
      // The column a line's text starts at, past its indent and any
      // markers: a bullet, a numbered item, and the glyph an agent puts
      // before its first line ("\u25cf - Release notes: https://…"). The rest
      // of a URL the program broke is indented to that column.
      function contentIndent(text) {
        return text.match(/^\s*(?:[^\w\s"'(\[{]{1,2}\s+)*(?:\d+[.)]\s+)?/)[0].length;
      }
      // continues: logical line N carries the rest of a URL the program
      // broke at the margin at the end of the text so far — that text ends
      // inside an open URL (nothing a URL would not end with), the line it
      // came from is full to the margin, and N starts with an indent and a
      // run of URL characters.
      // A URL cut at the margin may end on any of its characters — a dot
      // in "releases.json", or a single letter left over — so only a closing
      // bracket the URL did not open says it has ended.
      function stillOpen(url) {
        const close = url[url.length - 1], open = { ')': '(', ']': '[', '}': '{' }[close];
        if (!open) return true;
        return url.split(close).length - 1 <= url.split(open).length - 1;
      }
      function continues(rows, first, prev, sofar, N) {
        if (!N || rows[N.top].wrapped) return false;
        const m = textOf(rows, N).match(/^(\s+)(\S+)/);
        if (!m || !URL_CHARS.test(m[2]) || m[1].length !== contentIndent(rows[first.top].text)) return false;
        const tail = sofar.match(/https?:\/\/[^\s<>"'`]*$/);
        if (!tail || !stillOpen(tail[0])) return false;
        return textOf(rows, prev).length >= Math.max(marginFloor, marginAround(rows, prev) - 1);
      }
      // The pieces of a logical line: its rows, the first one past the
      // indent it was given as a continuation.
      function piecesOf(rows, L, indent) {
        const out = [];
        for (let k = L.top; k <= L.bot; k++) {
          out.push(k === L.top ? { row: k, col: indent, text: rows[k].text.slice(indent) } : { row: k, col: 0, text: rows[k].text });
        }
        return out;
      }
      // Walking down from logical line S: the pieces of the chain of
      // continuations it starts.
      function chainFrom(rows, S) {
        const pieces = piecesOf(rows, S, 0);
        let sofar = textOf(rows, S), prev = S;
        for (;;) {
          if (prev.bot + 1 >= rows.length) break;
          const N = logicalAt(rows, prev.bot + 1);
          if (!continues(rows, S, prev, sofar, N)) break;
          const indent = textOf(rows, N).match(/^\s*/)[0].length;
          pieces.push(...piecesOf(rows, N, indent));
          sofar += textOf(rows, N).slice(indent);
          prev = N;
        }
        return pieces;
      }
      // The pieces of the logical line holding row i, joined with the rows
      // a broken URL continues on: the highest line above that chains
      // down to this one, then everything that chains on below.
      function lineAt(rows, i) {
        const L = logicalAt(rows, i);
        // Candidates above: lines that look like they could start the
        // chain. Each is tried from the top; the first whose chain reaches
        // this line wins, and the line itself is the fallback.
        const starts = [L];
        let cur = L;
        while (cur.top > 0) {
          const P = logicalAt(rows, cur.top - 1);
          const t = textOf(rows, cur);
          if (!/^\s+\S/.test(t) || textOf(rows, P).length < Math.max(marginFloor, marginAround(rows, P) - 1)) break;
          starts.unshift(P);
          cur = P;
        }
        for (const S of starts) {
          const pieces = chainFrom(rows, S);
          if (pieces.some(p => p.row === i)) return pieces;
        }
        return piecesOf(rows, L, 0);
      }
      function linksInRows(rows, i) {
        if (!rows || i < 0 || i >= rows.length) return [];
        // Spaces a program painted to the right edge (an input box, a
        // status line) stay in the buffer; a URL never ends in one, and a
        // row ending in them would never read as cut at the margin.
        rows = rows.map(r => ({ text: r.text.replace(/\s+$/, ''), wrapped: r.wrapped }));
        const pieces = lineAt(rows, i);
        const joined = pieces.map(p => p.text).join('');
        const out = [];
        let m;
        URL_START.lastIndex = 0;
        while ((m = URL_START.exec(joined))) {
          const url = trimTrail(m[0]);
          if (!/^https?:\/\/[^\/]/.test(url)) continue; // "https://..." in prose is not a link
          const s = m.index, e = s + url.length - 1;
          out.push({ url: url, start: place(pieces, s), end: place(pieces, e) });
        }
        // Only links that touch the row asked about: the others belong
        // to their own rows and are reported there.
        return out.filter(k => k.start.row <= i && i <= k.end.row);
      }
      function place(pieces, off) {
        for (const p of pieces) {
          if (off < p.text.length) return { row: p.row, col: p.col + off };
          off -= p.text.length;
        }
        const last = pieces[pieces.length - 1];
        return { row: last.row, col: last.col + last.text.length - 1 };
      }
      return { linksInRows, lineAt, trimTrail };
    })();
    window.__links = (row) => { const buf = window.__term.buffer.active; const rows = []; for (let i = 0; i < buf.length; i++) { const l = buf.getLine(i); rows.push({ text: l ? l.translateToString(true) : '', wrapped: !!(l && l.isWrapped) }); } return reminalLinks.linksInRows(rows, row); };
    function wantsNewTab(ev) {
      return !!(ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey || ev.button === 1);
    }

    // ---- Recent sessions history (localStorage, cross-tab, cross-session) ----
    // Each entry: { id, pin, name, lastSeen }. Only UNNAMED (auto-recorded)
    // sessions count against the cap of 10 most-recent, so the dropdown stays
    // manageable; a session the user bothered to name is one they want to keep,
    // so it's never evicted (see trimHistory). We keep PINs here knowingly —
    // same model as `sessionStorage` for the current session; user can clear
    // via the "Forget" button on any row.
    const HISTORY_KEY = 'reminal:history';
    const HISTORY_MAX = 10;
    function loadHistory() {
      try {
        const raw = localStorage.getItem(HISTORY_KEY);
        if (!raw) return [];
        const arr = JSON.parse(raw);
        return Array.isArray(arr) ? arr : [];
      } catch (_) { return []; }
    }
    function saveHistory(list) {
      try { localStorage.setItem(HISTORY_KEY, JSON.stringify(list)); } catch (_) {}
    }
    // The PIN this browser already remembers for a session, if any. Recents
    // lives in localStorage, so every tab of this origin can read it — which is
    // what lets a tab opened from a Recents row connect on its own without the
    // PIN ever appearing in the URL.
    function historyPin(id) {
      if (!id) return '';
      const up = String(id).toUpperCase();
      const e = loadHistory().find(x => x && x.id === up);
      return (e && e.pin) || '';
    }
    // Enforce the cap WITHOUT evicting named sessions: named entries are always
    // kept (the user deliberately labelled them, so they're not throwaway), and
    // only the most-recent HISTORY_MAX unnamed entries survive. `list` is
    // newest-first, so counting unnamed entries from the front keeps the freshest.
    function trimHistory(list) {
      let unnamed = 0;
      return list.filter(e => {
        if (e.name) return true;
        unnamed++;
        return unnamed <= HISTORY_MAX;
      });
    }
    function rememberSession(id, pin) {
      // pin '' is a valid entry: owner (PIN-free) connects go through auth_ok
      // with no PIN, and they must land in Recents too — otherwise sessions on
      // other owned machines never show up here or in the session dock built
      // from this store. An empty pin never overwrites a known one.
      if (!id) return;
      const list = loadHistory();
      const existingIdx = list.findIndex(e => e.id === id);
      const existing = existingIdx >= 0 ? list[existingIdx] : null;
      const entry = {
        id: id.toUpperCase(),
        pin: pin || existing?.pin || '',
        name: existing?.name || '',
        lastSeen: Date.now(),
      };
      if (existingIdx >= 0) list.splice(existingIdx, 1);
      list.unshift(entry);
      saveHistory(trimHistory(list));
      renderRecents();
    }
    function renameSession(id, name) {
      const list = loadHistory();
      const e = list.find(x => x.id === id);
      if (!e) return;
      e.name = name;
      saveHistory(list);
      renderRecents();
      if (typeof refreshTabTitle === 'function') refreshTabTitle();
    }
    function forgetSession(id) {
      forgetCachedScreen(id);
      const w = warmSockets.get(String(id).toUpperCase()) || warmSockets.get(id);
      if (w) closeWarm(w);
      saveHistory(loadHistory().filter(e => e.id !== id));
      renderRecents();
    }
    // Stash the computer name against this session in Recent so the dropdown
    // can show "MacBook-Pro" next to the id even before you reconnect.
    function rememberHost(id, host) {
      if (!id || !host) return;
      const list = loadHistory();
      const e = list.find(x => x.id === id);
      if (e && e.host !== host) { e.host = host; saveHistory(list); renderRecents(); }
    }

    // ---- Host stats (computer name + basics) ----
    let lastHostInfo = null;
    function fmtBytes(n) {
      if (!n) return '—';
      const u = ['B', 'KB', 'MB', 'GB', 'TB'];
      let i = 0; let v = n;
      while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
      return (v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)) + ' ' + u[i];
    }
    function fmtUptime(s) {
      if (!s) return '—';
      const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
      if (d) return d + 'd ' + h + 'h';
      if (h) return h + 'h ' + m + 'm';
      return m + 'm';
    }
    // hostWaitSince is the wall-clock time we first started waiting for a reply
    // (0 = not waiting / already have data). renderHost decides "loading" vs
    // "this host is too old to answer" from ELAPSED time, not a one-shot timer —
    // so a throttled/dropped setTimeout (mobile background heuristics) can never
    // strand the panel on "Loading" forever; the poll re-render always catches up.
    let hostWaitSince = 0;
    const HOST_WAIT_MS = 4000;
    // The numeric fields of a host's report, forced to numbers on arrival. A
    // viewer joins hosts it does not control, and these are interpolated into
    // the panel's markup as numbers — so a host must not be able to send text
    // where a number is expected. Strings are escaped where they are used.
    const HOST_NUMERIC = ['cpus', 'mem_total', 'mem_used', 'cpu_pct', 'load1', 'load5', 'load15', 'uptime', 'sessions'];
    function cleanHostInfo(info) {
      if (!info || typeof info !== 'object') return {};
      for (const k of HOST_NUMERIC) {
        if (info[k] === undefined || info[k] === null) continue;
        const n = typeof info[k] === 'number' ? info[k] : NaN;
        if (Number.isFinite(n)) info[k] = n; else delete info[k];
      }
      return info;
    }
    function onHostInfo(info) {
      info = cleanHostInfo(info);
      const first = !lastHostInfo;
      lastHostInfo = info;
      // The panel was opened before the host had told us anything; now that it
      // has, ask the one question the panel exists to answer.
      if (first && updateCheckWanted && document.getElementById('host').open) requestUpdateCheck();
      // Completion for an upgrade can only arrive HERE. The host's last word
      // before it re-execs is "restart", and then it is gone — there is no
      // "done" to send, because the process that would send it has been
      // replaced. So the reconnect IS the completion signal: the host is
      // answering again, and its version says whether it moved.
      //
      // Without this the button spins forever after a perfectly successful
      // upgrade, which is the one outcome that must not look like a hang.
      //
      // Only a host_info from AFTER the reconnect may end it. The Host panel
      // polls host_info every 1.5s, and a poll answered while the host was
      // still restarting its other sessions — slow on Windows, where each one
      // spawns a successor — reported the old version and wiped the state
      // below, so a successful upgrade ended with no "Up to date" at all.
      if (upgradeState && upgradeState.stage === 'restart' && !upgradeState.error
          && (upgradeState.reconnected || (info.version && info.version !== upgradeState.version))) {
        const was = upgradeState.version;
        if (info.version && was && info.version !== was) {
          // A warning (a session that would not restart) rode in on the last
          // step before the reconnect; it is still true after it.
          upgradeState = { stage: 'done', pct: 100, version: info.version,
                           detail: 'Now on v' + info.version, warning: upgradeState.warning };
          changelog = null; // the notes we cached are about versions we now have
        } else {
          // It came back on the same version, or on a build too old to report
          // one. We cannot claim success, so stop asserting anything and let
          // the button be pressable again.
          upgradeState = null;
        }
        // The What's new sheet carries the same button; without this it kept
        // painting "Restarting…" forever after the host panel said done.
        const sheet = document.getElementById('clsheet');
        if (sheet && !sheet.hidden && !changelog) requestUpdateCheck();
        paintChangelog();
      }
      // A host upgrade done OUTSIDE this viewer — a CLI `reminal upgrade`, the
      // always-on daemon restarting itself, or another device — moves the
      // running version with no upgradeState here to clear the cached offer. The
      // reconnect refreshes host_info (so the version above is current), but the
      // cached notes were computed against the OLD version and would keep
      // offering the very version we now run. If the version we just heard no
      // longer matches what the notes were built from, drop them so the panel
      // re-fetches a fresh, correct offer instead of one that never goes away.
      if (changelog && changelog.current && info.version &&
          String(changelog.current).replace(/^v/, '') !== String(info.version).replace(/^v/, '')) {
        changelog = null;
        if (document.getElementById('host') && document.getElementById('host').open) requestUpdateCheck();
      }
      if (info.hostname) rememberHost(sessionId, info.hostname);
      // Owner (PIN-free) connects never typed the PIN. Share asks the agent
      // for it, to show it and mint a real Join link; it is kept for that, in
      // memory, for this session only — never stored, never made the
      // connection's credential: a device that is later revoked must not
      // keep a way back in.
      if (info.pin && /^\d{6,8}$/.test(info.pin)) {
        sharePinFromAgent = { sid: sessionId, pin: info.pin };
        const modal = document.getElementById('share-modal');
        if ((modal && modal.style.display === 'flex') || document.querySelector('.mo-sheet.open')) fillShare();
      }
      // The connected session's own attention state rides host_info, and the
      // agent pushes a fresh one on every change — so the pill for the session
      // you're watching flips the instant the agent does, ahead of the fleet
      // directory's slower poll. 'attn' present (even "") is authoritative for
      // this session; absent means an older host that can't report it, so leave
      // whatever the poll last knew.
      if (sessionId && ('attn' in info)) {
        setLiveAttn(sessionId, info.attn || '');
        wakeAgentBack(sessionId, info.attn || '');
        if (window.reminalMoments) try { reminalMoments.attn(sessionId, info.attn || ''); } catch (_) {}
      }
      renderHost();
    }

    // ---- Spawn + connect to a new headless session on the host ----
    let creatingSession = false;
    let hostNewError = '';
    let hostNewTimer = null;
    let hostDesktopErr = '';
    let hostDesktopHint = ''; // permission hint under "View full desktop"
    // Open a mirror pane for each display the host lists. An agent that's too
    // old (or a platform without desktop capture) lists none — say so briefly.
    async function openDesktopPanes() {
      const wins = await nextWindowList();
      if (!wins) return; // the session changed meanwhile
      // Only a hint that means capture would FAIL (macOS Screen Recording off →
      // black frames) blocks opening — surface it instead of dead panes. An
      // informational caveat (e.g. Wayland: the desktop view works but per-window
      // control doesn't) must NOT stop the desktop — it stays visible in the
      // Windows panel as a heads-up.
      if (winCaptureBlocked) {
        hostDesktopHint = winHint;
        renderHost();
        return;
      }
      hostDesktopHint = '';
      const displays = wins.filter(w => (w.id || '').startsWith('display:'));
      if (!displays.length) {
        hostDesktopErr = 'Not supported by this host';
        renderHost();
        setTimeout(() => { hostDesktopErr = ''; renderHost(); }, 3500);
        return;
      }
      const h = document.getElementById('host'); if (h) h.open = false;
      for (const d of displays) openWinView(d);
    }
    // Fill the login inputs with a session's creds and dial it, closing the
    // current one first when switching. Same path the Recent rows use.
    function connectToSession(id, pin) {
      if (!id) return; // pin may be '' — owner-handshake sessions dial PIN-free
      sessionInput.value = id;
      pinInput.value = pin;
      pinForSession = pin ? String(id).toUpperCase() : '';
      pinTyped = false;
      const rec = document.getElementById('recents'); if (rec) rec.open = false;
      const h = document.getElementById('host'); if (h) h.open = false;
      if (sessionId && sessionId !== id) userDisconnect({ switching: true });
      startConnect();
    }

    // --- Spawn timeouts: one source of truth so the two spawn paths can't drift.
    // SPAWN_HANDSHAKE_MS mirrors the host's spawnHandshakeTimeout
    // (internal/client/spawn.go) — the most time it spends forking a headless
    // agent and waiting for its ready handshake. A viewer-side spawn wait MUST
    // exceed this plus that path's own overhead, or a slow-but-successful spawn is
    // misreported as a failure and the session is orphaned.
    const SPAWN_HANDSHAKE_MS = 15000;
    // Host panel: request rides the already-open session channel — small reply margin.
    const HOST_SPAWN_TIMEOUT_MS = SPAWN_HANDSHAKE_MS + 2000;
    // Machines panel: request opens a FRESH directory channel and completes the
    // owner handshake before the spawn even starts — needs more headroom.
    const DIR_SPAWN_TIMEOUT_MS = SPAWN_HANDSHAKE_MS + 5000;

    // connectToSpawnedSession connects to a freshly-spawned session {id, pin}.
    // Owner spawns (Machines panel) connect PIN-free via the owner path; host-panel
    // spawns use the returned PIN. Both spawn paths funnel through here so a spawn
    // result is always handled identically. Returns false if there's no session.
    function connectToSpawnedSession(r, owner) {
      if (!r || !r.id) return false;
      if (owner) {
        if (r.pin) saveCred(r.id, r.pin);
        connectOwned(r.id);
      } else {
        connectToSession(r.id, r.pin);
      }
      return true;
    }
    function onNewSession(p) {
      creatingSession = false;
      if (hostNewTimer) { clearTimeout(hostNewTimer); hostNewTimer = null; }
      if (p && p.error) {
        hostNewError = 'Couldn’t start';
        renderHost();
        setTimeout(() => { hostNewError = ''; renderHost(); }, 4000);
        return;
      }
      connectToSpawnedSession(p, false); // host-panel spawn → connect with its PIN
    }
    // Health colour for a 0..100 usage: green → amber → red.
    function meterColor(p) {
      if (p >= 85) return '#f85149';
      if (p >= 65) return '#d29922';
      return '#3fb950';
    }
    function hostMeter(label, valueHTML, pct) {
      const p = Math.max(0, Math.min(100, Math.round(pct)));
      return '<div class="host-m">'
        + '<div class="top"><span class="l">' + label + '</span><span class="r">' + valueHTML + '</span></div>'
        + '<div class="bar"><span style="width:' + p + '%;background-color:' + meterColor(p) + '"></span></div>'
        + '</div>';
    }
    // ---- Own this machine (browser device identity) --------------------------
    // The browser mints its own non-extractable Ed25519 keypair (in IndexedDB)
    // and shows its PUBLIC id, formatted identically to `reminal own` on the CLI
    // (rmnl_ + base64url of the 32-byte public key), so it drops straight into
    // `sudo reminal add owner <id>` on the host. The private key never leaves
    // the browser. Ownership isn't consumed for connect yet — this is the
    // enrollment surface.
    let ownIdShown = null;   // the rmnl_ id, once generated (persists across host re-renders)
    let ownReenroll = false; // reveal the enroll command even when we think we already own it (ownership can be revoked host-side out of band)
    let ownCopied = false;
    let ownRevealed = false; // the raw id string is hidden by default (it's ugly)
    let ownErr = '';
    let iOwnThisMachine = false; // true once we've owner-connected to the current machine → hide the enroll offer
    // keyByPin: this socket's session key came by the PIN handshake, so the
    // machine did not answer this device as an owner here, whatever it did
    // before (an owner list can drop it). Reset with each socket.
    let keyByPin = false;
    let ownFp = null;        // this browser's short device fingerprint (dv_…), shown so you can match it in `reminal owners`

    // deviceFingerprint mirrors Go's: "dv_" + hex(first 4 bytes of SHA-256(pub)).
    async function deviceFingerprint(pubRaw) {
      const h = new Uint8Array(await crypto.subtle.digest('SHA-256', pubRaw));
      let hex = '';
      for (let i = 0; i < 4; i++) hex += h[i].toString(16).padStart(2, '0');
      return 'dv_' + hex;
    }
    async function refreshOwnFingerprint() {
      try { const dk = await loadDeviceKeyPair(); ownFp = dk ? await deviceFingerprint(dk.pubRaw) : null; }
      catch (_) { ownFp = null; }
      if (typeof renderHost === 'function') renderHost();
    }
    function b64url(bytes) {
      let s = '';
      for (const b of bytes) s += String.fromCharCode(b);
      return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    }
    function openOwnDB() {
      return new Promise((res, rej) => {
        const r = indexedDB.open('reminal-own', 1);
        r.onupgradeneeded = () => r.result.createObjectStore('keys');
        r.onsuccess = () => res(r.result);
        r.onerror = () => rej(r.error);
      });
    }
    function idbGet(db, key) {
      return new Promise((res, rej) => {
        const t = db.transaction('keys', 'readonly').objectStore('keys').get(key);
        t.onsuccess = () => res(t.result);
        t.onerror = () => rej(t.error);
      });
    }
    function idbPut(db, key, val) {
      return new Promise((res, rej) => {
        const t = db.transaction('keys', 'readwrite').objectStore('keys').put(val, key);
        t.onsuccess = () => res();
        t.onerror = () => rej(t.error);
      });
    }
    // A curve key (Ed25519, X25519) made by this browser. A build of WebKit
    // refuses any such key whose first byte is 0: making one fails
    // (OperationError) about once in 200, importing one every time
    // (DataError). Each try is a fresh random key: three at most.
    async function curveKeyMade(name, extractable, usages) {
      for (let i = 1; ; i++) {
        try { return await crypto.subtle.generateKey({ name: name }, extractable, usages); }
        catch (e) { if (i >= 3 || !e || e.name !== 'OperationError') throw e; }
      }
    }
    // An Ed25519 key from a 32-byte seed: { pub (32 bytes), sign(bytes) →
    // 64 bytes }. The browser's own where it takes the seed; tweetnacl's
    // where it has no Ed25519 or refuses that seed (WebKit, its first byte
    // 0): the same key, and the same signatures (Ed25519's are
    // deterministic), so whoever checks them cannot tell which made them.
    async function ed25519FromSeed(seed) {
      const pk8 = new Uint8Array(48);
      pk8.set(hexToU8('302e020100300506032b657004220420')); pk8.set(seed, 16);
      try {
        const k = await crypto.subtle.importKey('pkcs8', pk8, { name: 'Ed25519' }, true, ['sign']);
        const pub = base64ToU8(b64URLToStd((await crypto.subtle.exportKey('jwk', k)).x));
        return { pub: pub, sign: async b => new Uint8Array(await crypto.subtle.sign({ name: 'Ed25519' }, k, b)) };
      } catch (_) {
        const nacl = await loadNacl(), kp = nacl.sign.keyPair.fromSeed(seed);
        return { pub: kp.publicKey, sign: async b => nacl.sign.detached(new Uint8Array(b), kp.secretKey) };
      }
    }
    // Whether sig is pub's Ed25519 signature of msg: the browser's own check,
    // tweetnacl's where it has no Ed25519 (Safari 16: NotSupportedError). A
    // key or signature the browser refuses for its shape is refused as before.
    async function ed25519Verify(pub, sig, msg) {
      try {
        const k = await crypto.subtle.importKey('raw', pub, { name: 'Ed25519' }, false, ['verify']);
        return await crypto.subtle.verify({ name: 'Ed25519' }, k, sig, msg);
      } catch (e) {
        if (!e || e.name !== 'NotSupportedError') throw e;
        const nacl = await loadNacl();
        return nacl.sign.detached.verify(new Uint8Array(msg), new Uint8Array(sig), new Uint8Array(pub));
      }
    }
    // This device's owner key signs bytes: the browser's own key, or, in a
    // browser with no Ed25519, the one made from its sealed seed
    // (loadDeviceKeyPair).
    async function deviceSign(dk, bytes) {
      if (dk.sign) return dk.sign(bytes);
      return new Uint8Array(await crypto.subtle.sign({ name: 'Ed25519' }, dk.privateKey, bytes));
    }
    // What a person reads where a browser cannot make the owner key at all
    // (no Ed25519, and tweetnacl not loaded): in place of the browser's own
    // "The operation is not supported."
    const DEVICE_KEY_UNSUPPORTED = 'This browser can’t make the keys reminal needs. Update Safari (to 17 or later), or use another browser, then try again.';
    // An owner key where the browser makes no Ed25519 key itself: a random
    // seed, its key tweetnacl's (ed25519FromSeed: the same key, signatures and
    // device id a browser's own would give), the seed kept sealed under a key
    // the browser keeps and never gives out (AES-GCM, not extractable, like the
    // exchange key's), and read back to be sure it was kept.
    async function deviceSeedKeyMade(db) {
      let k, seed;
      try {
        seed = crypto.getRandomValues(new Uint8Array(32));
        k = await ed25519FromSeed(seed);
      } catch (_) { throw new Error(DEVICE_KEY_UNSUPPORTED); }
      const seal = await crypto.subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']);
      const iv = crypto.getRandomValues(new Uint8Array(12));
      const sealed = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv: iv }, seal, seed));
      seed.fill(0);
      await idbPut(db, 'device', { seed: { seal: seal, iv: iv, sealed: sealed }, pubRaw: new Uint8Array(k.pub) });
      const back = await idbGet(db, 'device');
      if (!back || !back.seed || !back.pubRaw || u8ToBase64(new Uint8Array(back.pubRaw)) !== u8ToBase64(k.pub)) throw new Error('This browser did not keep the owner key it was given: allow this site to store data, then try again.');
      return new Uint8Array(k.pub);
    }
    async function ownDeviceId() {
      const db = await openOwnDB();
      const rec = await idbGet(db, 'device');
      let pub;
      if (rec && rec.publicKey) {
        pub = new Uint8Array(await crypto.subtle.exportKey('raw', rec.publicKey));
      } else if (rec && rec.seed && rec.pubRaw) {
        pub = new Uint8Array(rec.pubRaw); // made from a seed (a browser with no Ed25519)
      } else {
        // extractable=false → the PRIVATE key can never leave the browser.
        // Made only when none is kept: a kept one is never made again.
        let kp = null;
        try { kp = await curveKeyMade('Ed25519', false, ['sign', 'verify']); }
        catch (e) { if (!e || e.name !== 'NotSupportedError') throw e; }
        if (kp) {
          await idbPut(db, 'device', { privateKey: kp.privateKey, publicKey: kp.publicKey });
          pub = new Uint8Array(await crypto.subtle.exportKey('raw', kp.publicKey));
        } else {
          pub = await deviceSeedKeyMade(db); // no Ed25519 here (Safari 16)
        }
        forgetCachedScreen(); // a new owner key: what the old one opened is not this one's
        closeAllWarm();
      }
      return 'rmnl_' + b64url(pub);
    }

    // ── upgrade & changelog ───────────────────────────────────────────
    // upgradeState is the live progress of a host upgrade, or null when idle.
    // It survives the Host panel's periodic re-render the same way
    // creatingSession does, so watching an upgrade does not depend on the
    // panel not repainting under you.
    let upgradeState = null;
    let changelog = null;      // {current, releases[], error} once fetched
    let changelogPending = false;
    let changelogTimer = null;
    const CHANGELOG_WAIT_MS = 8000; // a host too old to answer never will
    let updateCheckWanted = false;  // panel opened before host_info arrived; ask on arrival

    // ONE question, ONE answer. Opening the panel fetches the release notes,
    // and that reply is also what says whether a newer version exists: the
    // newest release in it that is not the one running. There is no separate
    // "is there an update" request and no button — a second path with its own
    // cache is how the panel came to say "up to date" beside a "What's new · 2"
    // that plainly listed a newer version.
    function requestUpdateCheck() {
      if (!lastHostInfo || !lastHostInfo.version) { updateCheckWanted = true; return; }
      if (typeof sendWin !== 'function') return;
      updateCheckWanted = false;
      if (changelogPending) return;
      changelogPending = true;
      clearTimeout(changelogTimer);
      changelogTimer = setTimeout(() => { changelogPending = false; renderHost(); paintChangelog(); }, CHANGELOG_WAIT_MS);
      sendWin('changelog', {});
      renderHost();
    }

    // The version to offer, from the same reply the notes are drawn from. The
    // host's cached hint (host_info.update) is only used while the reply has
    // not arrived — it is the daily answer, and can be a release behind.
    function hostUpdate(i) {
      if (changelog && changelog.releases) {
        const newer = changelog.releases.filter(r => !r.current);
        return newer.length ? newer[0].version : '';
      }
      return i.update || '';
    }

    // The notes link is offered whenever the host can answer it, including when
    // up to date — a dead end there would make "what changed?" unanswerable for
    // anyone already current.
    function whatsNewHTML(i) {
      if (!i.version) return '';
      // The count is releases you do not have yet, so it agrees with the
      // status beside it by construction: a count means "update available".
      const behind = changelog && changelog.releases ? changelog.releases.filter(r => !r.current).length : 0;
      return '<i class="host-br"></i><button class="host-whatsnew">What\u2019s new' + (behind > 0 ? ' \u00b7 ' + behind : '') + '</button>' +
        '<button class="host-whatsnew host-tour">Take the tour</button>';
    }

    // Version, the control that changes it, and the notes for it — one block,
    // last in the panel.
    function hostVersionBlockHTML(i) {
      if (!i.version) return '';
      return '<div class="host-vblock">' + hostVersionHTML(i) + upgradeButtonHTML(i) + '</div>';
    }

    function hostVersionHTML(i) {
      if (!i.version) return ''; // pre-3.5.8 host: it cannot act on any of this
      // One version number, once. This row says what the machine IS; the
      // button below says what it would become. Naming the target here too —
      // and a third time in the button's subtitle — put the same number on
      // screen three times in a panel eight lines tall. The state still reads
      // at a glance, in words rather than a repeated number.
      let h = '<div class="host-ver">reminal <code>v' + escHTML(i.version) + '</code>';
      const u = upgradeState;
      // "up to date" is only ever said on the strength of a reply: the daily
      // hint alone can be a release behind, and asserting it as fact is the
      // wrong answer this row used to give.
      const answered = changelog && changelog.releases && !changelog.error;
      if (u && u.stage === 'done' && !u.error) h += '<span class="new">\u00b7 updated</span>';
      // No "update available" text: the button below says so, and on a phone
      // the words plus the notes pill overflowed the row into a horizontal
      // scroll. An offer is a button, not a label.
      // A machine whose reminal is put in place by what installed it (an
      // image, a package): it neither checks nor upgrades itself, so it says
      // who does — never "could not check" for a check it does not make.
      else if (i.updated_by)                   h += '<span>\u00b7 updated by ' + escHTML(i.updated_by) + '</span>';
      else if (hostUpdate(i))                  { /* the upgrade button is the statement */ }
      else if (changelogPending)               h += '<span>\u00b7 checking\u2026</span>';
      else if (answered)                       h += '<span>\u00b7 up to date</span>';
      else if (changelog && changelog.error)   h += '<span>\u00b7 could not check</span>';
      return h + whatsNewHTML(i) + '</div>';
    }

    // One renderer for every upgrade state, so the button and its subtitle
    // cannot disagree about what is happening.
    function upgradeButtonHTML(i) {
      if (!i.version) return '';
      const u = upgradeState;
      // A failure carries the stage it failed AT, so "still working" has to
      // exclude it — otherwise the retry button is permanently disabled and
      // the only way out of a failed upgrade is reloading the page.
      const busy = u && !u.error && ['download', 'verify', 'install', 'restart'].includes(u.stage);
      const failed = u && u.error;
      const done = u && u.stage === 'done' && !u.error;
      // Only when there is something to do. Opening the panel already asked the
      // host for a fresh answer (requestUpdateCheck), so "nothing to offer"
      // means the host is current — the row above says so — not that nobody
      // has looked.
      const update = hostUpdate(i);
      if (!update && !u) return '';
      // Owners only. This is the cosmetic half of the rule — the host verifies
      // a signed owner proof on the request and refuses without one — but a
      // button that cannot work should not be shown.
      //
      // `!u` matters: once something IS in flight or has failed, it must stay
      // visible. Returning unconditionally here swallowed the error from a
      // browser that could not produce a proof, so the press did nothing and
      // said nothing.
      if (!iOwnThisMachine && !u) return '';
      // pct lands unquoted inside a style attribute below, and everything in
      // `u` came off the wire. A number is the only shape that cannot break
      // out of it, so coerce rather than trust — Number() on a hostile string
      // gives NaN, which the clamp turns into 0.
      let label, sub;
      const pct = u ? Math.max(0, Math.min(100, Math.round(Number(u.pct)) || 0)) : 0;
      // The button carries a SHORT label in every state and the line under it
      // carries the detail. It shares a row with "What's new", so a sentence
      // in the button — a status, an error — wrapped to four lines and the
      // whole row ballooned with it.
      // A "restart"-stage failure comes AFTER the binary was replaced, so the
      // reassurance that nothing changed would be false there — and a retry
      // would only answer "already on the latest version".
      const installed = failed && u.stage === 'restart';
      if (installed)   { label = 'Retry upgrade';
                         sub = escHTML(u.error) + ' \u2014 the new version is installed; <code>reminal restart --all</code> on the host moves what is left.'; }
      else if (failed) { label = 'Retry upgrade';
                         sub = escHTML(u.error) + ' \u2014 nothing was replaced, every session is untouched.'; }
      else if (done)   { label = '\u2713 Up to date';
                         sub = [u.detail, u.warning].filter(Boolean).map(escHTML).join(' \u00b7 '); }
      else if (busy)   { label = 'Upgrading\u2026'; sub = escHTML(u.detail || ''); }
      else             { label = 'Upgrade to v' + escHTML(update);
                         sub = hostRestartClause() + ' Shells are kept.'; }
      let h = '<button class="host-upgrade' + (done ? ' done' : failed ? ' fail' : '') + '"'
            + (busy || done ? ' disabled' : '') + '>'
            + (busy ? '<span class="up-spin"></span>' : '') + label + '</button>';
      if (u && !failed) h += '<div class="up-rail' + (done ? ' done' : '') + '"><i style="width:' + pct + '%"></i></div>';
      if (u && failed)  h += '<div class="up-rail fail"><i style="width:' + pct + '%"></i></div>';
      if (sub) h += '<div class="up-sub">' + sub + '</div>';
      return h;
    }

    // How many sessions a restart would move, from the Machines panel's own
    // cache of this host when we have it. Falls back to neutral wording rather
    // than guessing a number.
    // A whole clause, not a noun: "every session" spliced into "<n> restart in
    // place" produced "every session restart in place", which is not a sentence.
    function hostRestartClause() {
      const n = Number((lastHostInfo && lastHostInfo.sessions) || 0);
      return n > 0 ? '<b>' + n + ' session' + (n === 1 ? '' : 's') + '</b> restart in place.'
                   : '<b>Every session</b> restarts in place.';
    }

    function escHTML(v) {
      return String(v == null ? '' : v).replace(/[&<>"]/g, c =>
        ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
    }

    // Renders one changelog file. Deliberately not a markdown engine: the
    // format in changelog/README.md is a heading, bullets and plain-line
    // subheadings precisely so that this stays about thirty lines and nobody
    // reaches for a table that would break on a phone.
    function renderNotes(md) {
      const out = [];
      let list = null;
      const flush = () => { if (list) { out.push(list + '</ul>'); list = null; } };
      for (const raw of String(md || '').split('\n')) {
        const line = raw.replace(/\s+$/, '');
        if (!line.trim()) { flush(); continue; }
        if (/^#{1,3} /.test(line)) continue;          // the version, already in the header
        if (/^[-*] /.test(line)) {
          if (!list) list = '<ul class="cl-list">';
          list += '<li>' + inlineNotes(line.slice(2)) + '</li>';
          continue;
        }
        if (list && /^\s{2,}\S/.test(raw)) {          // a wrapped bullet, folded back in
          list = list.replace(/<\/li>$/, ' ' + inlineNotes(line.trim()) + '</li>');
          continue;
        }
        flush();
        // A short label ("Fixed", "New") heads a section; a sentence is a
        // paragraph. Notes written as prose came out as section headings —
        // uppercase, small and grey, line after line.
        const label = line.length <= 32 && !/[.!?:,]$/.test(line);
        out.push(label ? '<div class="cl-sub">' + inlineNotes(line) + '</div>' : '<p class="cl-p">' + inlineNotes(line) + '</p>');
      }
      flush();
      return out.join('') || '<div class="cl-sub">No notes for this release.</div>';
    }
    // Escape first, then allow exactly one inline form: `code`. Notes come off
    // the network, so nothing else may become markup.
    function inlineNotes(t) {
      return escHTML(t).replace(/`([^`]+)`/g, '<code>$1</code>');
    }

    function openChangelog() {
      const back = document.getElementById('clback'), sheet = document.getElementById('clsheet');
      if (!back || !sheet) return;
      back.hidden = false; sheet.hidden = false;
      back.classList.add('open'); sheet.classList.add('open');
      // Mark the fetch as in flight BEFORE the first paint. Painting first
      // rendered the not-yet-fetched empty list as "You're on the latest" —
      // an answer, and the wrong one — for as long as the round trip took,
      // then silently corrected itself.
      if (!changelog) requestUpdateCheck();
      paintChangelog();
    }
    function closeChangelog() {
      const back = document.getElementById('clback'), sheet = document.getElementById('clsheet');
      if (!back || !sheet) return;
      back.classList.remove('open'); sheet.classList.remove('open');
      back.hidden = true; sheet.hidden = true;
    }

    function paintChangelog() {
      const sheet = document.getElementById('clsheet');
      if (!sheet || sheet.hidden) return;
      const cur = (lastHostInfo && lastHostInfo.version) || (changelog && changelog.current) || '';
      const rels = (changelog && changelog.releases) || [];
      // The running version now rides along in the list so its notes can
      // be read. It is not somewhere to upgrade to, so it never counts as
      // behind, and never decides whether the upgrade button is drawn.
      const behind = rels.filter(r => !r.current).length;
      const haveCurrent = rels.some(r => r.current);
      let sub;
      if (changelogPending) sub = 'Fetching release notes\u2026';
      else if (changelog && changelog.error) sub = 'Could not reach the release notes';
      else if (behind) sub = 'You\u2019re on v' + escHTML(cur) + ' \u00b7 ' + behind
             + ' release' + (behind > 1 ? 's' : '') + ' behind';
      else sub = 'You\u2019re on the latest, v' + escHTML(cur);

      let h = '<div class="cl-head"><div><div class="cl-title">What\u2019s new</div>'
            + '<div class="cl-behind">' + sub + '</div></div>'
            + '<button class="cl-x" id="clx" aria-label="Close">\u00d7</button></div>';

      if (changelogPending) {
        h += '<div class="cl-empty">Fetching from the release feed\u2026</div>';
      } else if (changelog && changelog.error) {
        h += '<div class="cl-empty">' + escHTML(changelog.error)
           + '<br><br>Upgrading still works \u2014 this only affects reading about it first.</div>';
      } else if (!rels.length) {
        h += '<div class="cl-empty">No release notes published for v' + escHTML(cur) + ' or anything newer.</div>';
      } else {
        h += '<div class="cl-body">';
        // Newest first, then everything between here and there: a host three
        // releases behind has no other way to learn what landed in the middle.
        rels.forEach((r, idx) => {
          // Only the release you are on is tagged. Every other entry is
          // something one upgrade gives you, so ranking them — "next" for the
          // newest, "skipped" for the rest — described a staged rollout that
          // does not happen and read as "you did not get these".
          const tag = r.current ? '<span class="cl-tag here">you are here</span>' : '';
          h += '<div class="cl-rel"><div class="cl-relhead">'
             + '<span class="cl-ver">v' + escHTML(r.version) + '</span>' + tag
             + '<span class="cl-when">' + escHTML(relWhen(r.published)) + '</span></div>'
             + renderNotes(r.notes) + '</div>';
        });
        // Only needed when the running version has no entry of its own to
        // tag: an unreleased build, or one whose release was pulled.
        if (cur && !haveCurrent) h += '<div class="cl-here">you are here \u00b7 v' + escHTML(cur) + '</div>';
        h += '</div>';
      }
      // Nothing newer means nothing to press: the sheet is open to be read,
      // not to start an upgrade that would be a no-op. But an upgrade that is
      // running, finished or failed keeps its button — "done" clears the notes
      // it was started from, and dropping the foot with them hid the outcome.
      if (behind || upgradeState) h += '<div class="cl-foot">' + (upgradeButtonHTML(lastHostInfo || {}) || '') + '</div>';
      sheet.innerHTML = h;
      const x = sheet.querySelector('#clx');
      if (x) x.onclick = closeChangelog;
      const up = sheet.querySelector('.host-upgrade');
      if (up) up.onclick = () => {
        if (up.disabled) return;
        upgradeState = { stage: 'download', pct: 3, detail: 'Starting\u2026' };
        renderHost(); paintChangelog();
        sendOwnerAction('upgrade', 'upgrade');
      };
    }

    // ---- Alerts ---------------------------------------------------------
    // A machine you own can push to this device when its CPU leaves a range,
    // its battery runs low, or its charger is plugged in or out. The machine
    // does the watching (its daemon runs whether or not anyone is looking) and
    // seals each alert to this browser's own push keys before it leaves, so the
    // relay forwards what it cannot read. This page subscribes once, keeps a set
    // of DEFAULT rules plus optional per-machine custom rules, and hands each
    // machine its own over the owner-only directory channel. A machine that is
    // offline when something changes catches up the next time it is seen.
    const DEFAULT_ALERT_RULES = { cpu: true, cpu_pct: 90, cpu_low: 0, cpu_mins: 5, battery: true, bat_pct: 20, bat_time: false, bat_mins: 60, charger: false };
    // Every threshold in steps of 10, end to end. CPU's two ends only have to
    // stay in order (alertRulesHTML narrows each list by the other's value).
    const tens = (lo, hi) => Array.from({ length: (hi - lo) / 10 + 1 }, (_, i) => lo + i * 10);
    const ALERT_CHOICES = {
      cpu_pct: tens(10, 100),
      cpu_low: tens(0, 90),
      cpu_mins: [1, 2, 5, 10, 15, 30, 60],
      bat_pct: tens(10, 90),
      bat_mins: [30, 60, 90, 120, 150, 180, 210, 240, 270, 300],
    };
    // 30 → "30 min", 60 → "1 h", 90 → "1.5 h".
    const alertHours = (v) => v < 60 ? v + ' min' : (v / 60) + ' h';
    const alerts = { open: false, view: 'loading', detail: '', sub: null, rows: {}, busy: false, msg: '', msgKind: '' };
    let alertPrefsCache = null;

    function aIcon(name, size) {
      const s = size || 16;
      return '<svg viewBox="0 0 24 24" width="' + s + '" height="' + s + '" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + (MICONS[name] || '') + '</svg>';
    }
    function pushSupported() {
      return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window && window.isSecureContext;
    }
    function isIOSDevice() {
      return /iP(hone|ad|od)/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
    }
    function isStandaloneApp() {
      return (window.matchMedia && matchMedia('(display-mode: standalone)').matches) || navigator.standalone === true;
    }
    // How to turn notifications on from wherever this page is running. An
    // installed app has no address bar, so the browser-tab steps (the lock
    // icon) are impossible there; each platform keeps the switch somewhere
    // else.
    function alertAllowSteps() {
      const host = escHTML(location.host);
      const tab = '<li>Tap the site icon at the left of the address bar, then <b>Permissions</b></li>'
        + '<li>Tap <b>Reset</b>, then turn <b>Notifications</b> on</li>';
      if (isStandaloneApp() && isIOSDevice()) {
        return '<ol><li>Open the <b>Settings</b> app</li>'
          + '<li>Go to <b>Notifications</b>, then <b>reminal</b></li>'
          + '<li>Turn on <b>Allow Notifications</b></li></ol>';
      }
      // An installed app has no address bar: the reset happens in a browser
      // tab on the same site, and the app picks it up.
      if (isStandaloneApp()) return '<ol><li>Open <b>' + host + '</b> in your browser</li>' + tab + '<li>Come back here</li></ol>';
      return '<ol>' + tab + '</ol>';
    }
    // Alerts arrived in 3.14.0. A reply with no version at all comes from a
    // machine older still; a non-numeric version is a development build,
    // which is assumed current.
    function alertHostTooOld(r) {
      const v = r && r.stats && r.stats.version;
      if (!v) return true;
      const m = String(v).replace(/^v/, '').match(/^(\d+)\.(\d+)\.(\d+)/);
      if (!m) return false;
      const [maj, min] = [+m[1], +m[2]];
      return maj < 3 || (maj === 3 && min < 14);
    }
    function b64urlToU8(s) {
      const pad = '='.repeat((4 - s.length % 4) % 4);
      return base64ToU8((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
    }
    function normAlertRules(r) { return Object.assign({}, DEFAULT_ALERT_RULES, r || {}); }

    async function loadAlertPrefs() {
      let p = null;
      try { const db = await openOwnDB(); p = await idbGet(db, 'alert-prefs'); } catch (_) {}
      p = p || {};
      alertPrefsCache = { enabled: !!p.enabled, rules: normAlertRules(p.rules) };
      return alertPrefsCache;
    }
    async function saveAlertPrefs(p) {
      alertPrefsCache = p;
      try { const db = await openOwnDB(); await idbPut(db, 'alert-prefs', p); } catch (_) {}
    }

    // Reuse the registration once there is one. register() re-downloads
    // sw.js to check for an update, and this is called on every permission
    // check — a Worker request each time for a file that has not changed.
    async function pushRegistration() {
      const have = await navigator.serviceWorker.getRegistration('/');
      if (have && have.active) return have;
      await navigator.serviceWorker.register('/sw.js', { scope: '/' });
      return navigator.serviceWorker.ready;
    }
    // This browser's push subscription, or null. create=true subscribes; the
    // caller has already been granted permission.
    async function pushSubscription(create) {
      const reg = await pushRegistration();
      let s = await reg.pushManager.getSubscription();
      if (!s && create) {
        const r = await fetch('/push/key', { cache: 'no-store' });
        const j = await r.json().catch(() => ({}));
        if (!r.ok || !j.key) throw new Error(j.error || 'This relay cannot send alerts');
        s = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64urlToU8(j.key) });
      }
      if (!s) return null;
      const j = s.toJSON();
      return { endpoint: j.endpoint, p256dh: j.keys.p256dh, auth: j.keys.auth };
    }
    // What an owner device signs for a push request (pushAction on the
    // machine): its op and endpoint, and for "token" the token too.
    function pushActionOf(p) {
      return 'push:' + p.op + ':' + p.sub.endpoint + (p.op === 'token' ? ':' + p.token : '');
    }
    // The request a machine is sent for one push call.
    function pushRequestOf(p, proof, reqId) {
      return { push: { op: p.op, sub: p.sub, rules: p.rules || undefined, token: p.op === 'token' ? p.token : undefined, proof, req_id: reqId } };
    }
    async function pushCall(b64, op, sub, rules, token) {
      const r = await queryDirectory(base64ToU8(b64URLToStd(b64)), { push: { op, sub, rules, token }, timeout: op === 'test' ? 30000 : 10000 });
      if (!r || !r.push) throw new Error('needs-upgrade');
      if (r.push.error) throw Object.assign(new Error(r.push.error), { answered: true });
      return r.push;
    }

    // Alerts are bound to the machine they are turned on for. The relay sends
    // this phone a token for (this subscription, that machine) in a
    // notification, "Alerts are on"; the service worker keeps it for the page
    // (sw.js), and the page hands it to that machine (op "token"). A machine
    // that has none for this phone (it was turned on before) says so
    // (tokened false): it sends this phone nothing until it has one, so the
    // page offers Renew alerts on this phone — in the Machines panel as soon
    // as such a machine answers, and in the sheet.
    const ALERT_TOKENS = 'reminal-alert-tokens';
    // The machines this phone's alerts need renewing on.
    const alertRenew = new Set();
    const alertRenewAsked = new Set();
    async function askAlertToken(b64, sub) {
      const r = await fetch('/push/token', { method: 'POST', cache: 'no-store', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ endpoint: sub.endpoint, p256dh: sub.p256dh, auth: sub.auth, machine: b64URLToStd(b64) }) });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(j.error || 'The relay could not renew alerts');
    }
    // The tokens the service worker kept, each handed to its machine.
    let alertTokensBusy = null;
    function deliverAlertTokens() {
      if (alertTokensBusy) return alertTokensBusy;
      alertTokensBusy = (async () => {
        let c;
        try { c = await caches.open(ALERT_TOKENS); } catch (_) { return; }
        const { store } = await sortedAlertMachines();
        const sub = alerts.sub || await pushSubscription(false).catch(() => null);
        if (!sub) return;
        for (const req of await c.keys()) {
          const machine = decodeURIComponent(new URL(req.url).pathname.split('/').pop() || '');
          const b64 = Object.keys(store).find(k => b64URLToStd(k) === machine);
          const token = await (await c.match(req)).text();
          if (!b64) { await c.delete(req); continue; }
          try { await pushCall(b64, 'token', sub, undefined, token); await c.delete(req); alertRenew.delete(b64); paintAlertRenew(); }
          // Refused by the machine (alerts off there, not its owner): it will
          // not take it later either. Not reached: tried again the next time
          // the page or the sheet opens.
          catch (e) { if (e && e.answered) await c.delete(req); }
        }
        if (alerts.open) await checkAlertTokens();
      })().finally(() => { alertTokensBusy = null; });
      return alertTokensBusy;
    }
    if ('serviceWorker' in navigator) {
      navigator.serviceWorker.addEventListener('message', e => { if (e.data && e.data.type === 'alert-token') deliverAlertTokens(); });
    }
    // Which machines this phone's alerts are on without a token, by asking
    // each that is on (only while the sheet is open).
    async function checkAlertTokens() {
      const prefs = alertPrefsCache || await loadAlertPrefs();
      const { store, keys } = await sortedAlertMachines();
      if (!alerts.sub) { alerts.renew = []; return; }
      await Promise.all(keys.filter(k => alertMachineOn(prefs, store[k])).map(async k => {
        try { const r = await pushCall(k, 'get', alerts.sub); if (r.subscribed && r.tokens && !r.tokened) alertRenew.add(k); else alertRenew.delete(k); } catch (_) {}
      }));
      alerts.renew = [...alertRenew];
      paintAlertRenew();
      if (alerts.open) paintAlerts();
    }
    // A machine that just answered, asked once a page load whether this
    // phone's alerts there need renewing: so the first time the page opens
    // after alerts were bound, the person is asked, sheet open or not.
    async function checkAlertRenewFor(b64, rec) {
      if (alertRenewAsked.has(b64) || !pushSupported()) return;
      const prefs = alertPrefsCache || await loadAlertPrefs();
      if (!alertMachineOn(prefs, rec)) return;
      // Asked once it can be: no subscription yet (the service worker not
      // ready) is not an answer.
      const sub = alerts.sub || await pushSubscription(false).catch(() => null);
      if (!sub || alertRenewAsked.has(b64)) return;
      alertRenewAsked.add(b64);
      alerts.sub = sub;
      try {
        const r = await pushCall(b64, 'get', sub);
        if (r.subscribed && r.tokens && !r.tokened) alertRenew.add(b64); else alertRenew.delete(b64);
      } catch (_) { alertRenewAsked.delete(b64); return; }
      alerts.renew = [...alertRenew];
      paintAlertRenew();
      if (alerts.open) paintAlerts();
    }
    // The Machines panel's line: one tap renews them all.
    async function paintAlertRenew() {
      const el = document.getElementById('alerts-renew');
      if (!el) return;
      el.hidden = !alertRenew.size;
      if (!alertRenew.size) return;
      const store = await loadOwnedMachines();
      el.querySelector('.alr-text').textContent = 'Phone alerts from ' + [...alertRenew].map(k => machineLabel(store[k] || {}, k)).join(', ') + ' need renewing on this device.';
    }
    async function renewAlerts() {
      const sub = alerts.sub || await pushSubscription(false).catch(() => null);
      if (!sub) return 'This device has no alerts subscription: turn alerts on again in Alerts.';
      let failed = '';
      for (const k of alertRenew) {
        try { await askAlertToken(k, sub); } catch (e) { failed = (e && e.message) || 'that did not work'; }
      }
      return failed;
    }
    {
      const el = document.getElementById('alerts-renew');
      if (el) el.querySelector('button').addEventListener('click', async () => {
        const b = el.querySelector('button'); b.disabled = true;
        const failed = await renewAlerts();
        b.disabled = false;
        el.querySelector('.alr-text').textContent = failed || 'Renewing — tap nothing more: a notification “Alerts are on” arrives on this device for each machine.';
      });
    }
    async function alertsRenew() {
      if (alerts.busy || !alerts.sub) return;
      alerts.busy = true; paintAlerts();
      const failed = await renewAlerts();
      alerts.busy = false;
      alertsSay(failed ? 'err' : 'ok', failed || 'Renewing — a notification “Alerts are on” arrives on this device for each machine.');
    }

    // The rules a machine runs: its own when customised, the defaults otherwise.
    function alertRulesFor(prefs, rec) { return rec && rec.alertRules ? normAlertRules(rec.alertRules) : prefs.rules; }
    // What a machine SHOULD be told, as a comparable key: its rules when it
    // should alert, 'off' when it should not.
    function alertWantKey(prefs, rec) {
      return prefs.enabled && !rec.alertsOff ? JSON.stringify(alertRulesFor(prefs, rec)) : 'off';
    }
    function alertMachineOn(prefs, rec) {
      return !!prefs && prefs.enabled && !rec.alertsOff && rec.alertsSynced === alertWantKey(prefs, rec);
    }
    async function sortedAlertMachines() {
      const store = await loadOwnedMachines();
      const keys = Object.keys(store).filter(k => !store[k].pendingRevoke)
        .sort((a, b) => (store[b].lastSeen || 0) - (store[a].lastSeen || 0));
      return { store, keys };
    }

    // Bring one machine in line with what it should be told. Returns the row
    // state it ended in.
    // opts.retryOld: ask a machine that last said "needs an update" again.
    // Only opening the sheet does that; everything in the background leaves
    // such a machine alone — each ask is a relay round trip, and an old
    // machine gives the same answer every time.
    async function syncAlertMachine(b64, prefs, sub, opts) {
      const store = await loadOwnedMachines();
      const rec = store[b64];
      if (!rec) return 'off';
      if (rec.alertsNeedsUpdate && !(opts && opts.retryOld)) return setAlertRow(b64, 'upgrade');
      const key = alertWantKey(prefs, rec);
      const want = key !== 'off';
      // Never told anything and should stay quiet: nothing to undo.
      if (!want && (rec.alertsSynced === undefined || rec.alertsSynced === 'off')) return setAlertRow(b64, 'off');
      if (rec.alertsSynced === key) return setAlertRow(b64, want ? 'on' : 'off');
      if (!sub) return setAlertRow(b64, 'off');
      setAlertRow(b64, 'saving');
      try {
        if (want) {
          const r = await pushCall(b64, 'set', sub, alertRulesFor(prefs, rec));
          // Bound to this machine from the start (a machine too old for it
          // says nothing of tokens).
          if (r.tokens && !r.tokened) askAlertToken(b64, sub).catch(() => {});
        } else await pushCall(b64, 'remove', sub);
        await withOwnedMachines(m => { if (!m[b64]) return false; m[b64].alertsSynced = key; delete m[b64].alertsNeedsUpdate; });
        return setAlertRow(b64, want ? 'on' : 'off');
      } catch (e) {
        const m = (e && e.message) || '';
        if (m === 'needs-upgrade') {
          await withOwnedMachines(mm => { if (!mm[b64]) return false; mm[b64].alertsNeedsUpdate = true; });
          return setAlertRow(b64, 'upgrade');
        }
        if (m === 'timeout' || m === 'unreachable') return setAlertRow(b64, want ? 'pending-on' : 'pending-off');
        return setAlertRow(b64, 'error', m);
      }
    }
    function setAlertRow(b64, state, detail) {
      alerts.rows[b64] = { state, detail: detail || '' };
      if (alerts.open) paintAlerts();
      return state;
    }
    async function syncAllAlerts(opts) {
      const prefs = alertPrefsCache || await loadAlertPrefs();
      const { keys } = await sortedAlertMachines();
      const results = await Promise.all(keys.map(k => syncAlertMachine(k, prefs, alerts.sub, opts)));
      refreshAlertBadges();
      return keys.filter((k, i) => results[i] === 'on');
    }

    // Called when the Machines panel sees a machine answer: finish any change
    // it missed while offline. Quiet — no sheet needed.
    // Every check below is local and free; only a machine that passes them
    // all costs a relay round trip, and at most once per ALERT_RETRY_MS.
    const alertCatchUp = new Set();
    const alertLastTry = new Map();
    const ALERT_RETRY_MS = 5 * 60 * 1000;
    async function maybeCatchUpAlerts(b64, rec, resp) {
      const prefs = alertPrefsCache;
      if (!prefs || !pushSupported() || alertCatchUp.has(b64)) return;
      const key = alertWantKey(prefs, rec);
      if (rec.alertsSynced === key || (key === 'off' && (rec.alertsSynced === undefined || rec.alertsSynced === 'off'))) return;
      if (rec.alertsNeedsUpdate) {
        // The refresh that called us already carries the machine's version,
        // so an upgrade is noticed for free — and until then, nothing is sent.
        if (!resp || alertHostTooOld(resp)) return;
        await withOwnedMachines(m => { if (m[b64]) delete m[b64].alertsNeedsUpdate; });
      }
      const last = alertLastTry.get(b64) || 0;
      if (Date.now() - last < ALERT_RETRY_MS) return;
      if (key !== 'off' && !alertPermGranted(await readAlertPermission())) return;
      alertLastTry.set(b64, Date.now());
      alertCatchUp.add(b64);
      try {
        const sub = alerts.sub || await pushSubscription(false);
        if (sub) { alerts.sub = sub; await syncAlertMachine(b64, prefs, sub); refreshAlertBadges(); }
      } catch (_) {} finally { alertCatchUp.delete(b64); }
    }

    // The bell beside the Machines search: lit while any machine is on.
    async function refreshAlertBadges() {
      const prefs = alertPrefsCache || await loadAlertPrefs();
      const { store, keys } = await sortedAlertMachines();
      const on = keys.filter(k => alertMachineOn(prefs, store[k])).length;
      const btn = document.getElementById('alerts-bar-btn');
      if (btn) {
        const lit = !!prefs.enabled && on > 0;
        btn.classList.toggle('on', lit);
        btn.title = lit ? 'Alerts · on for ' + on + (on === 1 ? ' machine' : ' machines') : 'Alerts';
        btn.setAttribute('aria-label', btn.title);
      }
    }

    // ---- permission ----
    let stopPermWatch = null;
    // Every source of the answer, because inside an installed app they can
    // disagree: Android hands the decision to the app's own notification
    // switch, and Notification.permission has been seen to say "denied"
    // there while the push service itself allows it. Any one of them saying
    // "granted" is enough to try; the subscribe call is the real test.
    async function readAlertPermission() {
      const p = { notification: 'Notification' in window ? Notification.permission : 'unsupported', query: '?', push: '?',
                  where: isStandaloneApp() ? 'installed app' : 'browser tab' };
      try { p.query = (await navigator.permissions.query({ name: 'notifications' })).state; } catch (_) {}
      try { const reg = await pushRegistration(); p.push = await reg.pushManager.permissionState({ userVisibleOnly: true }); } catch (_) {}
      return p;
    }
    function alertPermGranted(p) { return p.notification === 'granted' || p.query === 'granted' || p.push === 'granted'; }
    function alertPermDenied(p) { return p.notification === 'denied' || p.query === 'denied' || p.push === 'denied'; }

    function watchPermission(onGranted) {
      if (stopPermWatch) stopPermWatch();
      let st = null, busy = false;
      const check = async () => {
        if (busy) return;
        busy = true;
        const p = await readAlertPermission();
        busy = false;
        if (!alertPermGranted(p) || !stopPermWatch) return;
        stopPermWatch();
        onGranted();
      };
      const iv = setInterval(check, 1000);
      document.addEventListener('visibilitychange', check);
      window.addEventListener('focus', check);
      if (navigator.permissions && navigator.permissions.query) {
        navigator.permissions.query({ name: 'notifications' }).then(s => { st = s; s.onchange = check; }).catch(() => {});
      }
      stopPermWatch = () => {
        clearInterval(iv);
        document.removeEventListener('visibilitychange', check);
        window.removeEventListener('focus', check);
        if (st) st.onchange = null;
        stopPermWatch = null;
      };
    }

    async function alertsTurnOn() {
      if (alerts.busy) return;
      // Asked synchronously inside the tap: browsers only prompt in direct
      // response to a user gesture.
      if (Notification.permission === 'default') {
        let asked = null;
        try { asked = Notification.requestPermission(); } catch (_) {}
        alerts.view = 'asking'; alerts.msg = ''; paintAlerts();
        try { await asked; } catch (_) {}
      }
      const p = await readAlertPermission();
      alerts.perm = p;
      if (alertPermGranted(p)) return alertsFinishTurnOn();
      // Chrome often answers with its quiet prompt (a crossed-out bell in the
      // address bar) and reports "no answer" at once; a blocked site answers
      // "denied". Either way, wait for the change and carry on by ourselves.
      alerts.view = alertPermDenied(p) ? 'blocked' : 'quiet';
      paintAlerts();
      watchPermission(() => alertsFinishTurnOn());
    }

    // "Try again": ask once more inside this tap, then go straight to
    // subscribing — the push service is the final word, and when it refuses,
    // its own reason is shown instead of our guess.
    async function alertsTryAgain() {
      if (alerts.busy) return;
      try { Notification.requestPermission(); } catch (_) {}
      if (stopPermWatch) stopPermWatch();
      await alertsFinishTurnOn(true);
    }

    async function alertsFinishTurnOn(retry) {
      alerts.busy = true; alerts.view = 'working'; alerts.msg = ''; paintAlerts();
      try {
        alerts.sub = await pushSubscription(true);
        const prefs = Object.assign({}, alertPrefsCache || await loadAlertPrefs(), { enabled: true });
        await saveAlertPrefs(prefs);
        alerts.view = 'on'; alerts.busy = false; paintAlerts();
        const onKeys = await syncAllAlerts();
        if (onKeys.length) {
          // A real alert straight away, so the first one is not a surprise
          // hours later — and proof the whole path works.
          try { await pushCall(onKeys[0], 'test', alerts.sub); alertsSay('ok', 'You’re set — a test alert is on its way.'); }
          catch (e) { alertsSay('err', 'Alerts are on, but the test did not go through: ' + alertErrText(e)); }
        } else {
          alertsSay('', 'Alerts are on. Your machines pick them up as soon as they are online.');
        }
      } catch (e) {
        alerts.busy = false;
        if (retry) {
          alerts.perm = await readAlertPermission();
          alerts.view = alertPermDenied(alerts.perm) ? 'blocked' : 'quiet';
          watchPermission(() => alertsFinishTurnOn());
          alertsSay('err', 'Your browser said: ' + ((e && (e.name ? e.name + ': ' : '') + e.message) || String(e)));
        } else {
          alerts.view = 'off';
          alertsSay('err', alertErrText(e));
        }
      }
      alerts.busy = false; paintAlerts();
    }
    function alertErrText(e) {
      const m = (e && e.message) || String(e);
      if (m === 'timeout' || m === 'unreachable') return 'Could not reach the machine.';
      if (m === 'needs-upgrade') return 'That machine needs a reminal update first.';
      return m;
    }
    function alertsSay(kind, text) { alerts.msgKind = kind; alerts.msg = text; paintAlerts(); }

    // ---- sheet ----
    async function openAlerts(focusB64) {
      const back = document.getElementById('alback'), sheet = document.getElementById('alsheet');
      if (!back || !sheet) return;
      Object.assign(alerts, { open: true, view: 'loading', detail: '', rows: {}, busy: false, msg: '', msgKind: '' });
      back.hidden = false; sheet.hidden = false;
      back.classList.add('open'); sheet.classList.add('open');
      paintAlerts();
      const prefs = await loadAlertPrefs();
      if (!pushSupported()) { alerts.view = 'unsupported'; return paintAlerts(); }
      try { alerts.sub = await pushSubscription(false); } catch (_) { alerts.sub = null; }
      alerts.perm = await readAlertPermission();
      if (prefs.enabled && alerts.sub && alertPermGranted(alerts.perm)) {
        alerts.view = 'on'; alerts.detail = focusB64 || '';
        paintAlerts();
        await syncAllAlerts({ retryOld: true });
        await deliverAlertTokens();
        await checkAlertTokens();
      } else {
        alerts.view = 'off';
        paintAlerts();
      }
    }
    function closeAlerts() {
      const back = document.getElementById('alback'), sheet = document.getElementById('alsheet');
      if (!back || !sheet) return;
      alerts.open = false;
      if (stopPermWatch) stopPermWatch();
      back.classList.remove('open'); sheet.classList.remove('open');
      back.hidden = true; sheet.hidden = true;
      refreshAlertBadges();
    }

    function alertChip(k, v, fmt, disabled, allowed) {
      const base = allowed || ALERT_CHOICES[k];
      const opts = base.includes(v) ? base : base.concat([v]).sort((a, b) => a - b);
      return '<select class="al-chip" data-k="' + k + '" aria-label="' + k.replace('_', ' ') + '"' + (disabled ? ' disabled' : '') + '>'
        + opts.map(o => '<option value="' + o + '"' + (o === v ? ' selected' : '') + '>' + fmt(o) + '</option>').join('')
        + '</select>';
    }
    function alertSwitch(attrs, on, label, disabled) {
      return '<button class="al-switch" role="switch" aria-checked="' + (on ? 'true' : 'false') + '" aria-label="' + escHTML(label) + '" '
        + attrs + (disabled ? ' disabled' : '') + '></button>';
    }
    // The three rule rows, for the defaults or for one machine. Read-only when
    // a machine is following the defaults.
    // time: 'note' on the defaults (the rule only reaches machines that
    // estimate time left), true on a machine that does, false to leave it out.
    function alertRulesHTML(r, ro, time) {
      const lows = ALERT_CHOICES.cpu_low.filter(v => v < r.cpu_pct);
      const highs = ALERT_CHOICES.cpu_pct.filter(v => v > r.cpu_low);
      return '<div class="al-list' + (ro ? ' ro' : '') + '">'
        + '<div class="al-row"><span class="al-tile' + (r.cpu ? ' on' : '') + '">' + aIcon('cpu') + '</span><div class="al-text al-sentence">'
        + '<span><b>CPU</b> leaves</span>' + alertChip('cpu_low', r.cpu_low, v => v + '%', ro, lows) + '<span>–</span>'
        + alertChip('cpu_pct', r.cpu_pct, v => v + '%', ro, highs) + '<span>for</span>' + alertChip('cpu_mins', r.cpu_mins, v => v + ' min', ro)
        + '</div>' + alertSwitch('data-rule="cpu"', r.cpu, 'CPU alerts', ro) + '</div>'
        + '<div class="al-row"><span class="al-tile' + (r.battery ? ' on' : '') + '">' + aIcon('battery') + '</span><div class="al-text al-sentence">'
        + '<span><b>Battery</b> below</span>' + alertChip('bat_pct', r.bat_pct, v => v + '%', ro)
        + '</div>' + alertSwitch('data-rule="battery"', r.battery, 'Battery alerts', ro) + '</div>'
        + (time ? '<div class="al-row"><span class="al-tile' + (r.bat_time ? ' on' : '') + '">' + aIcon('clock') + '</span><div class="al-text al-sentence">'
          + '<span><b>Battery</b> left under</span>' + alertChip('bat_mins', r.bat_mins, alertHours, ro)
          + (time === 'note' ? '<span class="al-note">On machines that estimate time left</span>' : '')
          + '</div>' + alertSwitch('data-rule="bat_time"', r.bat_time, 'Battery time alerts', ro) + '</div>' : '')
        + '<div class="al-row"><span class="al-tile' + (r.charger ? ' on' : '') + '">' + aIcon('plug') + '</span><div class="al-text">'
        + '<b>Charger</b> plugged in or out</div>' + alertSwitch('data-rule="charger"', r.charger, 'Charger alerts', ro) + '</div></div>';
    }
    const ALERT_ROW_TEXT = {
      on: ['on', 'On'], off: ['', 'Off'], saving: ['', 'Saving…'], tested: ['on', 'Test sent'],
      'pending-on': ['warn', 'Offline — turns on when it’s back'],
      'pending-off': ['warn', 'Offline — turns off when it’s back'],
      upgrade: ['warn', 'Needs a reminal update'], error: ['err', 'Could not update'],
    };
    function alertRowStatus(prefs, rec, k) {
      const row = alerts.rows[k] || { state: alertMachineOn(prefs, rec) ? 'on' : (rec.alertsOff ? 'off' : 'saving') };
      const [cls, label] = ALERT_ROW_TEXT[row.state] || ['', ''];
      let text = row.state === 'error' && row.detail ? row.detail : label;
      if ((row.state === 'on' || row.state === 'tested') && rec.alertRules) text += ' · custom rules';
      return { cls, text, state: row.state };
    }
    function alertMsgHTML(extra) {
      if (!alerts.msg) return '';
      const k = alerts.msgKind;
      return '<div class="al-msg ' + k + '"' + (extra || '') + '>' + (k === 'ok' ? aIcon('check', 14) : k === 'err' ? aIcon('info', 14) : '')
        + '<span>' + escHTML(alerts.msg) + '</span></div>';
    }

    async function paintAlerts() {
      const sheet = document.getElementById('alsheet');
      if (!sheet || sheet.hidden) return;
      const a = alerts;
      const prefs = alertPrefsCache || { enabled: false, rules: normAlertRules() };
      const { store, keys } = await sortedAlertMachines();
      if (a.detail && !store[a.detail]) a.detail = '';
      const close = '<button class="al-x" id="alx" aria-label="Close">' + aIcon('x', 16) + '</button>';

      // One machine's settings.
      if (a.view === 'on' && a.detail) {
        const k = a.detail, rec = store[k];
        const st = alertRowStatus(prefs, rec, k);
        const custom = !!rec.alertRules;
        const on = !rec.alertsOff;
        let h = '<div class="al-head"><button class="al-x" id="al-back" aria-label="Back">' + aIcon('left', 16) + '</button>'
          + '<div class="al-head-text"><div class="al-title al-ell">' + escHTML(machineLabel(rec, k)) + '</div>'
          + '<div class="al-subtitle ' + st.cls + '">' + escHTML(st.text) + '</div></div>' + close + '</div>'
          + '<div class="al-list" style="padding-top:8px"><div class="al-row"><span class="al-tile' + (on ? ' on' : '') + '">' + aIcon('bell') + '</span>'
          + '<div class="al-text"><b>Alerts for this machine</b></div>' + alertSwitch('data-machine="' + escHTML(k) + '"', on, 'Alerts for this machine') + '</div></div>';
        if (on) {
          h += '<div class="al-sec">Rules</div>'
            + '<div class="al-seg" role="radiogroup" aria-label="Rules">'
            + '<button role="radio" aria-checked="' + !custom + '" data-mode="default">Default</button>'
            + '<button role="radio" aria-checked="' + custom + '" data-mode="custom">Custom</button></div>'
            + '<div class="al-hint">' + (custom ? 'Only this machine uses these.' : 'Following your default rules. Choose Custom to change them for this machine only.') + '</div>'
            + alertRulesHTML(alertRulesFor(prefs, rec), !custom, !!rec.battEst);
        }
        h += alertMsgHTML();
        h += '<div class="al-foot"><button class="al-btn" id="al-test-one"' + (a.busy || st.state !== 'on' ? ' disabled' : '') + '>'
          + aIcon('send', 15) + '<span>Send a test</span></button></div>';
        sheet.innerHTML = h;
        wireAlertsSheet(sheet, k);
        return;
      }

      const onCount = keys.filter(k => { const s = alertRowStatus(prefs, store[k], k).state; return s === 'on' || s === 'tested'; }).length;
      let sub = 'Off', subOn = false;
      if (a.view === 'on') {
        const waitingN = keys.filter(k => a.rows[k] && a.rows[k].state === 'pending-on').length;
        const total = keys.length + (keys.length === 1 ? ' machine' : ' machines');
        sub = onCount ? 'On for ' + onCount + ' of ' + total : waitingN ? 'On · waiting for ' + (waitingN === 1 ? 'your machine' : waitingN + ' machines') : 'On';
        subOn = onCount > 0;
      }
      let h = '<div class="al-head"><div class="al-head-text"><div class="al-title">Alerts</div>'
            + '<div class="al-subtitle' + (subOn ? ' on' : '') + '">' + escHTML(sub) + '</div></div>' + close + '</div>';

      const feats = '<div class="al-feats al-list">'
        + '<div class="al-row"><span class="al-tile">' + aIcon('cpu') + '</span><div class="al-text">CPU stays out of its normal range</div></div>'
        + '<div class="al-row"><span class="al-tile">' + aIcon('battery') + '</span><div class="al-text">Battery runs low</div></div>'
        + '<div class="al-row"><span class="al-tile">' + aIcon('plug') + '</span><div class="al-text">Charger is plugged in or out</div></div></div>';
      const hero = '<div class="al-hero"><div class="al-hero-icon">' + aIcon('bell', 22) + '</div>'
        + '<div class="al-hero-title">Alerts from your machines</div>'
        + '<div class="al-hero-sub">A notification on this device when one of your machines needs attention — even when reminal isn’t open.</div></div>';
      const waiting = (t) => '<div class="al-wait"><span class="al-spin"></span>' + t + '</div>';

      if (a.view === 'loading') {
        h += '<div class="al-empty"><div class="al-wait" style="justify-content:center;margin:0"><span class="al-spin"></span>Checking…</div></div>';
      } else if (a.view === 'unsupported') {
        h += hero;
        h += isIOSDevice()
          ? '<div class="al-callout"><span class="al-callout-icon">' + aIcon('info') + '</span><div class="al-callout-body">'
            + '<div class="al-callout-title">Add reminal to your Home Screen first</div>'
            + 'On iPhone and iPad, alerts work in the Home Screen app. Tap <span class="al-glyph">' + aIcon('share', 14) + '</span> Share, choose '
            + '<b>Add to Home Screen</b>, then open reminal from there.</div></div>'
          : '<div class="al-callout err"><span class="al-callout-icon">' + aIcon('info') + '</span><div class="al-callout-body">'
            + 'This browser can’t receive notifications. Chrome, Edge, Firefox and Safari can.</div></div>';
        h += '<div style="height:16px"></div>';
      } else if (a.view === 'off' || a.view === 'asking' || a.view === 'working') {
        const busy = a.view !== 'off';
        h += hero + feats
          + '<div class="al-foot"><button class="al-btn primary" id="al-on"' + (busy ? ' disabled' : '') + '>'
          + (busy ? '<span class="al-spin"></span><span>' + (a.view === 'asking' ? 'Waiting for permission…' : 'Turning on…') + '</span>'
                  : aIcon('bell', 15) + '<span>Turn on for all my machines</span>')
          + '</button></div>';
        h += alertMsgHTML(' style="padding-bottom:14px"');
      } else if (a.view === 'quiet' || a.view === 'blocked') {
        h += hero;
        if (a.view === 'quiet') {
          h += '<div class="al-callout"><span class="al-callout-icon">' + aIcon('belloff') + '</span><div class="al-callout-body">'
            + '<div class="al-callout-title">Your browser is waiting for an answer</div>'
            + (isStandaloneApp()
                ? alertAllowSteps()
                : 'Instead of a popup, it put <span class="al-glyph">' + aIcon('belloff', 14) + '</span> in the address bar. Tap it and choose <b>Allow</b>.')
            + waiting('This continues by itself once you allow it') + '</div></div>';
        } else {
          h += '<div class="al-callout err"><span class="al-callout-icon">' + aIcon('lock') + '</span><div class="al-callout-body">'
            + '<div class="al-callout-title">Notifications are blocked ' + (isStandaloneApp() ? 'for reminal' : 'for this site') + '</div>'
            + alertAllowSteps()
            + waiting('This continues by itself once you allow it') + '</div></div>';
        }
        h += alertMsgHTML();
        h += '<div class="al-foot"><button class="al-btn primary" id="al-retry"' + (a.busy ? ' disabled' : '') + '>' + aIcon('bell', 15) + '<span>Try again</span></button>'
          + '<button class="al-btn" id="al-cancel"><span>Not now</span></button></div>';
      } else {
        h += '<div class="al-sec">Default rules</div>' + alertRulesHTML(prefs.rules, false, 'note');
        h += '<div class="al-sec">Machines</div><div class="al-list">';
        if (!keys.length) h += '<div class="al-empty">No machines yet — owner-connect to one and it shows up here.</div>';
        for (const k of keys) {
          const rec = store[k];
          const st = alertRowStatus(prefs, rec, k);
          h += '<div class="al-row al-mrow" data-open="' + escHTML(k) + '" role="button" tabindex="0">'
            + '<span class="al-dot ' + (st.cls === 'on' ? 'on' : st.cls === 'warn' ? 'warn' : '') + '"></span>'
            + '<div class="al-text"><div class="al-name">' + escHTML(machineLabel(rec, k)) + '</div>'
            + '<div class="al-state ' + st.cls + '">' + escHTML(st.text) + '</div></div>'
            + alertSwitch('data-machine="' + escHTML(k) + '"', !rec.alertsOff, 'Alerts for ' + machineLabel(rec, k))
            + '<span class="al-chev">' + aIcon('right', 16) + '</span></div>';
        }
        h += '</div>';
        if ((a.renew || []).length) {
          h += '<div class="al-renew"><div class="al-text">Alerts on this device need renewing for ' + escHTML((a.renew || []).map(k => machineLabel(store[k] || {}, k)).join(', '))
            + ': until then they get no alerts from ' + ((a.renew || []).length === 1 ? 'it' : 'them') + '.</div>'
            + '<button class="al-btn" id="al-renew"' + (a.busy ? ' disabled' : '') + '>' + aIcon('bell', 15) + '<span>Renew alerts on this phone</span></button></div>';
        }
        h += alertMsgHTML();
        h += '<div class="al-foot"><button class="al-btn" id="al-test"' + (a.busy ? ' disabled' : '') + '>' + aIcon('send', 15) + '<span>Send a test</span></button>'
          + '<button class="al-btn quiet" id="al-off"' + (a.busy ? ' disabled' : '') + '>' + aIcon('belloff', 15) + '<span>Turn all off</span></button></div>';
      }
      sheet.innerHTML = h;
      wireAlertsSheet(sheet, '');
    }

    let alertsSaveTimer = 0;
    function wireAlertsSheet(sheet, machine) {
      const bind = (id, fn) => { const el = sheet.querySelector('#' + id); if (el) el.onclick = fn; };
      bind('alx', closeAlerts);
      bind('al-back', () => { alerts.detail = ''; alerts.msg = ''; paintAlerts(); });
      bind('al-on', alertsTurnOn);
      bind('al-retry', alertsTryAgain);
      bind('al-cancel', () => { if (stopPermWatch) stopPermWatch(); alerts.view = 'off'; paintAlerts(); });
      bind('al-test', () => alertsSendTest(null));
      bind('al-test-one', () => alertsSendTest(machine));
      bind('al-off', alertsTurnOffAll);
      bind('al-renew', alertsRenew);

      // Rules save themselves after a short pause, so stepping a chip twice is
      // one update per machine, not two. In a machine's view they edit that
      // machine's own rules; on the main view, the defaults.
      const changed = async (mutate) => {
        if (machine) {
          await withOwnedMachines(m => {
            if (!m[machine]) return false;
            const r = normAlertRules(m[machine].alertRules || alertPrefsCache.rules);
            mutate(r);
            m[machine].alertRules = r;
          });
        } else {
          const prefs = Object.assign({}, alertPrefsCache, { rules: normAlertRules(alertPrefsCache.rules) });
          mutate(prefs.rules);
          await saveAlertPrefs(prefs);
        }
        alerts.msg = '';
        paintAlerts();
        clearTimeout(alertsSaveTimer);
        alertsSaveTimer = setTimeout(syncAllAlerts, 500);
      };
      for (const sel of sheet.querySelectorAll('.al-chip')) {
        sel.onchange = () => changed(r => { r[sel.dataset.k] = parseInt(sel.value, 10); });
      }
      for (const sw of sheet.querySelectorAll('.al-switch[data-rule]')) {
        sw.onclick = () => changed(r => { r[sw.dataset.rule] = !r[sw.dataset.rule]; });
      }
      for (const btn of sheet.querySelectorAll('.al-seg button')) {
        btn.onclick = async () => {
          const custom = btn.dataset.mode === 'custom';
          await withOwnedMachines(m => {
            if (!m[machine]) return false;
            // Custom starts from what the machine runs today, so the switch
            // itself changes nothing until a rule is edited.
            if (custom && !m[machine].alertRules) m[machine].alertRules = normAlertRules(alertPrefsCache.rules);
            else if (!custom) delete m[machine].alertRules;
          });
          paintAlerts();
          syncAlertMachine(machine, alertPrefsCache, alerts.sub).then(refreshAlertBadges);
        };
      }
      for (const sw of sheet.querySelectorAll('.al-switch[data-machine]')) {
        sw.onclick = async (ev) => {
          ev.stopPropagation(); // the row behind it opens the machine
          const k = sw.dataset.machine;
          await withOwnedMachines(m => { if (!m[k]) return false; m[k].alertsOff = !m[k].alertsOff; });
          paintAlerts();
          await syncAlertMachine(k, alertPrefsCache, alerts.sub);
          refreshAlertBadges();
        };
      }
      for (const row of sheet.querySelectorAll('.al-mrow')) {
        const open = () => { alerts.detail = row.dataset.open; alerts.msg = ''; paintAlerts(); };
        row.onclick = open;
        row.onkeydown = (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(); } };
      }
    }

    // One machine (its settings view) or every machine that is on.
    async function alertsSendTest(only) {
      const { keys } = await sortedAlertMachines();
      const on = (only ? [only] : keys).filter(k => alerts.rows[k] && alerts.rows[k].state === 'on');
      if (!on.length) return alertsSay('err', 'No machine is ready to send one — turn one on above, or wait until it is online.');
      alerts.busy = true; alertsSay('', '');
      const res = await Promise.allSettled(on.map(k => pushCall(k, 'test', alerts.sub)));
      res.forEach((r, i) => setAlertRow(on[i], r.status === 'fulfilled' ? 'tested' : 'error', r.status === 'fulfilled' ? '' : alertErrText(r.reason)));
      const ok = res.filter(r => r.status === 'fulfilled').length;
      alerts.busy = false;
      alertsSay(ok ? 'ok' : 'err', ok ? (ok === 1 ? 'Test sent — it should arrive in a few seconds.' : ok + ' tests sent — one from each machine.') : 'The test did not go through.');
      setTimeout(() => { for (const k of on) if (alerts.rows[k] && alerts.rows[k].state === 'tested') setAlertRow(k, 'on'); }, 5000);
    }

    async function alertsTurnOffAll() {
      alerts.busy = true;
      const prefs = Object.assign({}, alertPrefsCache, { enabled: false });
      await saveAlertPrefs(prefs);
      paintAlerts();
      await syncAllAlerts();
      alerts.busy = false;
      alerts.view = 'off';
      alertsSay('', 'Alerts are off. Machines that were offline turn off when they are back.');
    }

    (function wireAlerts() {
      const back = document.getElementById('alback');
      if (back) back.addEventListener('click', closeAlerts);
      addEventListener('keydown', (e) => {
        const sheet = document.getElementById('alsheet');
        if (e.key === 'Escape' && sheet && !sheet.hidden) { e.stopPropagation(); closeAlerts(); }
      });
      const btn = document.getElementById('alerts-bar-btn');
      if (btn) btn.addEventListener('click', (e) => { e.preventDefault(); e.stopPropagation(); openAlerts(''); });
      loadAlertPrefs().then(refreshAlertBadges);
    })();

    (function wireChangelogDismiss() {
      const back = document.getElementById('clback');
      if (back) back.addEventListener('click', closeChangelog);
      addEventListener('keydown', (e) => {
        const sheet = document.getElementById('clsheet');
        if (e.key === 'Escape' && sheet && !sheet.hidden) { e.stopPropagation(); closeChangelog(); }
      });
    })();

    function relWhen(iso) {
      if (!iso) return '';
      const d = new Date(iso);
      if (isNaN(d)) return '';
      const days = Math.floor((Date.now() - d) / 86400000);
      if (days <= 0) return 'today';
      if (days === 1) return 'yesterday';
      if (days < 30) return days + ' days ago';
      return d.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
    }

    // Sign a privileged request with this browser's enrolled device key. The
    // agent verifies the signature and checks the key is an owner of that
    // machine, so the authorisation travels WITH the request rather than being
    // implied by having connected — every viewer on a session shares one
    // encryption key, so "I could send this message" proves nothing.
    //
    // Returns null when this browser holds no device key, which is exactly the
    // case where the action is not offered.
    // sid defaults to the connected session; a machine-channel request passes
    // the directory id, which is what that channel's host verifies against.
    async function signOwnerAction(action, sid) {
      const dk = await loadDeviceKeyPair();
      if (!dk) return null;
      const nonce = crypto.getRandomValues(new Uint8Array(24));
      const unix = Math.floor(Date.now() / 1000);
      const ts = new Uint8Array(8);
      // Big-endian uint64, matching OwnerActionTranscript. Only the low 32
      // bits vary for any plausible clock, but the full width is written so
      // the two transcripts are byte-identical.
      new DataView(ts.buffer).setBigUint64(0, BigInt(unix), false);
      const tr = await ownerTranscript('reminal-owner-action-v1:', sid || sessionId,
        [new TextEncoder().encode(action), nonce, ts]);
      const sig = await deviceSign(dk, tr);
      return { device_pub: u8ToBase64(dk.pubRaw), device_sig: u8ToBase64(sig),
               nonce: u8ToBase64(nonce), unix };
    }

    // Sends a privileged request, or reports why it cannot. The button is
    // hidden for non-owners, but this path must still refuse rather than send
    // something the host will reject with a confusing error.
    async function sendOwnerAction(type, action) {
      const proof = await signOwnerAction(action);
      if (!proof) {
        upgradeState = { stage: 'download', pct: 0,
          error: 'This browser is not enrolled as an owner of this machine' };
        renderHost(); paintChangelog();
        return false;
      }
      sendWin(type, proof);
      return true;
    }

    function onChangelog(payload) {
      clearTimeout(changelogTimer);
      changelogPending = false;
      changelog = payload || {};
      renderHost();
      paintChangelog();
    }

    function onUpgrade(payload) {
      upgradeState = payload || null;
      renderHost();
      paintChangelog();
      // A finished upgrade invalidates what we know: the host is on a new
      // version, so the cached notes and the pending-update flag are both
      // stale. The reconnect re-reads host_info; drop the notes so the next
      // open re-fetches rather than showing releases we already have.
      if (payload && payload.stage === 'done' && !payload.error) changelog = null;
    }

    function renderHost() {
      const root = document.getElementById('host-list');
      if (!root) return;
      // Numeric host fields are used only when they are finite numbers.
      const i = lastHostInfo && Object.assign({}, lastHostInfo);
      if (i) for (const k of ['mem_used', 'mem_total', 'cpu_pct', 'load1', 'load5', 'load15', 'cpus', 'uptime']) {
        if (i[k] !== undefined && !(typeof i[k] === 'number' && isFinite(i[k]))) delete i[k];
      }
      if (!i) {
        // We only reach the Host panel while connected (the button is hidden
        // otherwise), so distinguish "still loading" from "the host is too old
        // to report stats" rather than telling the user to connect.
        let msg;
        if (!document.body.classList.contains('connected')) msg = 'Connect to a session to see host stats.';
        else if (hostWaitSince && (Date.now() - hostWaitSince) < HOST_WAIT_MS) msg = 'Loading host stats…';
        else msg = 'This host is on an older reminal — update it (v1.9.1+) to show stats.';
        root.innerHTML = '<div class="recents-empty">' + msg + '</div>';
        return;
      }
      // Every one of these comes off the wire from the host, and a viewer
      // joins hosts it does not control — a PIN is enough to connect, so the
      // machine on the other end is not trusted to hand us markup. Escaped at
      // the point of interpolation, the same reason the CLI runs remote
      // hostnames through cleanTerm before printing them.
      const name = escHTML((i.hostname || 'this computer').replace(/\.local$/, ''));
      const sub = [i.os, i.arch, i.cpu_model, i.cpus ? i.cpus + ' cores' : '']
        .filter(Boolean).map(escHTML).join(' · ');
      let html = '<div class="host-wrap">'
        + '<div class="host-name">' + name + '</div>'
        + '<div class="host-sub">' + sub + '</div>';
      if (i.mem_total) {
        const p = i.mem_used ? i.mem_used / i.mem_total * 100 : 0;
        html += hostMeter('Memory',
          '<b>' + fmtBytes(i.mem_used) + '</b> / ' + fmtBytes(i.mem_total) + ' <span class="dim">· ' + Math.round(p) + '%</span>', p);
      }
      if (i.cpu_pct !== undefined && i.cpu_pct !== null) {
        // Real CPU utilization (the "% busy" Activity Monitor / top show).
        const p = Math.max(0, Math.min(100, i.cpu_pct));
        html += hostMeter('CPU', '<b>' + Math.round(p) + '%</b>', p);
      } else if (i.load1 !== undefined && i.cpus) {
        // Older host (or a platform without a sampler): no utilization to show,
        // so present load honestly as a saturation bar — load ≠ CPU %.
        const p = i.load1 / i.cpus * 100;
        html += hostMeter('Load',
          '<b>' + i.load1.toFixed(2) + '</b> <span class="dim">· ' + Math.round(i.cpus) + ' cores</span>', p);
      }
      // Load and uptime finish the machine's vitals, so they sit with the
      // meters. They used to land between the version and the button that
      // upgrades it, splitting the one pair in this panel that belongs together.
      const foot = [];
      if (i.load1 !== undefined && i.load5 !== undefined && i.load15 !== undefined) foot.push('load ' + i.load1.toFixed(2) + ' ' + i.load5.toFixed(2) + ' ' + i.load15.toFixed(2));
      if (i.uptime) foot.push('up ' + fmtUptime(i.uptime));
      if (foot.length) html += '<div class="host-foot">' + foot.join('  ·  ') + '</div>';
      // Spawn a fresh headless shell on this same host and connect to it. Only
      // offered once we have host stats (proves the host runs a reminal new
      // enough to handle the request). Label reflects the in-flight/error state,
      // which survives the panel's periodic re-render via creatingSession.
      const label = hostNewError ? escHTML(hostNewError) : (creatingSession ? 'Starting…' : '+ New session on this host');
      html += '<button class="host-new"' + (creatingSession ? ' disabled' : '') + '>' + label + '</button>';
      // Mirror the whole desktop (every display) as panes — same streaming and
      // input pipeline as single windows, keyed by "display:<id>" entries the
      // agent appends to the window list.
      html += '<button class="host-desktop">' + (hostDesktopErr ? escHTML(hostDesktopErr) : 'View full desktop') + '</button>';
      if (hostDesktopHint) html += '<div class="windows-hint">' + permHintHTML(hostDesktopHint) + '</div>';
      // Always show this browser's device fingerprint so it can be matched to a
      // row in `reminal owners` (or the web owners list) without a label.
      if (ownFp) html += '<div class="own-fp">This browser · <code>' + ownFp + '</code></div>';
      // Own this machine: hidden entirely once this device already owns it (a
      // PIN-free/owner connect proves ownership); otherwise offer to enroll.
      if (iOwnThisMachine && !ownReenroll) {
        // Ownership lives on the host (/etc/reminal) and can be revoked there out
        // of band, so always offer a way back in rather than dead-ending on a
        // possibly-stale "you own this" (the browser only knows it once did).
        html += '<div class="own-owned">✓ You own this machine <span class="own-reenroll">re-enroll?</span></div>';
      } else if (ownErr) {
        html += '<div class="own-box"><div class="own-label">' + escHTML(ownErr) + '</div></div>';
      } else if (ownIdShown) {
        const cmd = 'sudo reminal add owner ' + ownIdShown;
        html += '<div class="own-box">'
          + '<div class="own-label">Run this on ' + name + ' to make this device an owner:</div>'
          + '<div class="own-actions"><button class="own-copy own-copy-main">'
          + (ownCopied ? 'Copied ✓' : 'Copy enroll command') + '</button></div>';
        if (ownRevealed) {
          html += '<div class="own-id"><code>' + cmd + '</code></div>';
        }
        html += '<div class="own-toggle"><span class="own-reveal">'
          + (ownRevealed ? 'hide command' : 'show command') + '</span>';
        // Once they've seen or copied the command, a Done button re-checks and
        // re-renders so they don't have to reload the page after running it.
        if (ownRevealed || ownCopied) {
          html += ' <button class="own-done">Done — I ran it</button>';
        }
        html += '</div></div>';
      } else {
        html += '<button class="host-desktop host-own">Own this machine</button>';
      }
      // Last: which reminal this machine runs, and the controls for changing it.
      html += hostVersionBlockHTML(i);
      html += '</div>';
      root.innerHTML = html;
    }
    function fmtAgo(ts) {
      const s = Math.floor((Date.now() - ts) / 1000);
      if (s < 60) return s + 's ago';
      if (s < 3600) return Math.floor(s / 60) + 'm ago';
      if (s < 86400) return Math.floor(s / 3600) + 'h ago';
      return Math.floor(s / 86400) + 'd ago';
    }
    // ---- Mobile session dock (see #sessdock CSS) -------------------------
    // One-gesture switching on the keybar's ≡ toggle: drag it up, the recents
    // dock appears, slide onto a session, release to connect. A plain TAP is
    // untouched — it still hides/shows the key bar (its click handler ignores
    // the click a drag release synthesizes, via sessdockLastDragEnd). Fed from
    // the FULL history — unlike the dropdown it does not exclude sessions
    // shown under Machines cards, because its whole job is fast switching to
    // any known session.
    const sessdockEl = document.getElementById('sessdock');
    const sessdockHandle = document.getElementById('keybar-toggle');
    // Same element as the keybar consts further down — those are in TDZ here.
    const sessdockKeybar = document.getElementById('keybar');

    // Where the keybar's top edge is — the dock's floor. Hidden keybar
    // (desktop-wide) means the floor is the viewport bottom.
    function keybarTop() {
      return sessdockKeybar.offsetParent
        ? sessdockKeybar.getBoundingClientRect().top
        : window.innerHeight;
    }

    // The dock sits just above the keybar, whose top moves with wrapping,
    // collapse, and the soft keyboard — so position comes from the live rect,
    // re-run via ResizeObserver + viewport events, not a hardcoded offset.
    // Reads before writes: the style assignments invalidate layout.
    function positionSessdock() {
      if (!sessdockEl.classList.contains('open')) return;
      const kbTop = keybarTop();
      const r = sessdockEl.getBoundingClientRect();
      const hr = sessdockHandle.getBoundingClientRect();
      // Never rise past the header: the dock lives strictly in the band
      // between header bottom and the keybar.
      const top = Math.max(headerBottom(), kbTop - 12 - r.height);
      // Anchor horizontally to the toggle itself — it's only right-pinned
      // while the keybar overflows; with the bar collapsed it sits at the
      // LEFT, and a right-fixed dock would appear across the screen from the
      // thumb that's dragging. Clamped inside the viewport either way.
      const right = Math.max(8, Math.min(window.innerWidth - hr.right,
                                         window.innerWidth - 8 - r.width));
      sessdockEl.style.top = top + 'px';
      sessdockEl.style.right = right + 'px';
    }
    if (window.ResizeObserver) new ResizeObserver(positionSessdock).observe(sessdockKeybar);
    if (window.visualViewport) visualViewport.addEventListener('resize', positionSessdock);
    window.addEventListener('resize', positionSessdock);

    // At most this many items, ever — the dock is a thumb-flick switcher, not
    // the full history (that's the dropdown). Fewer still if the screen is
    // short: openSessdock drops trailing items until the dock fits between
    // the header and the keybar, so items never run behind either.
    const SESSDOCK_MAX = 6;

    // machineLabel is an owned machine's display name — shared with the
    // Machines panel so the fallback can't drift between the two.
    function machineLabel(rec, b64) {
      return rec.name || 'machine ' + b64.slice(0, 6) + '…';
    }

    // The dock's candidate pool: every LIVE session on every machine this
    // device owns (from the Machines panel's cached fills — all reminal
    // instances, not just ones this browser has visited), merged with the
    // Recents history (PIN sessions on machines we don't own). Deduped by id,
    // freshest first. Refreshed asynchronously (the owned-machines store is
    // IndexedDB) into a snapshot so opening the dock stays synchronous under
    // the finger; single-flight, since refreshes fire from every Recents
    // render and can otherwise overlap during reconnect storms.
    let sessdockPool = [];
    let sessdockPoolBusy = false;
    let sessdockPoolAgain = false;
    async function refreshSessdockPool() {
      if (sessdockPoolBusy) {
        sessdockPoolAgain = true;
        return;
      }
      sessdockPoolBusy = true;
      try {
        do {
          sessdockPoolAgain = false;
          const now = Date.now();
          const pool = [];
          const seen = new Set();
          // ORDER IS CONNECT RECENCY, nothing cleverer: the sessions this
          // browser connected to, most recent first (their history lastSeen).
          // Host-side idle looked smart and was wrong — an always-active
          // agent outranked the session the user just left. Activity only
          // breaks ties among never-connected sessions, which sort after
          // everything the user has actually visited.
          const history = loadHistory();
          const hist = new Map(history.map(e => [e.id, e]));
          try {
            const store = await loadOwnedMachines();
            for (const b64 of Object.keys(store)) {
              const rec = store[b64];
              if (rec.pendingRevoke || !Array.isArray(rec.sessions)) continue;
              for (const s of rec.sessions) {
                if (s.kind === 'port' || killedSessionIds.has(s.id) || seen.has(s.id)) continue;
                seen.add(s.id);
                pool.push({
                  id: s.id, owned: true, label: sessionLabelWeb(s), machine: machineLabel(rec, b64),
                  mkey: b64, // which owned machine this lives on — the warm poller tiers by it
                  last: hist.get(s.id)?.lastSeen || 0,
                  act: now - (s.idle_secs || 0) * 1000,
                  attn: s.attn || '',
                });
              }
            }
          } catch (_) {} // no owned-machine data — the dock still has Recents
          for (const e of history) {
            // killedSessionIds: the owned-machine loop above filters kills, but
            // a killed session's HISTORY entry would sail through here and put
            // a dead session back in the dock.
            if (seen.has(e.id) || killedSessionIds.has(e.id)) continue;
            seen.add(e.id);
            pool.push({ id: e.id, pin: e.pin, label: e.name, last: e.lastSeen || 0, act: 0 });
          }
          // Current session first outright (it is the most recent connect by
          // definition, whatever the clocks say), then connect recency, then
          // host activity for the never-connected tail.
          pool.sort((a, b) => (b.id === sessionId) - (a.id === sessionId)
            || b.last - a.last || b.act - a.act);
          sessdockPool = pool;
        } while (sessdockPoolAgain);
      } finally {
        sessdockPoolBusy = false;
      }
    }

    function buildSessdockItems() {
      sessdockEl.innerHTML = '';
      for (const e of sessdockPool.slice(0, SESSDOCK_MAX)) {
        const item = document.createElement('div');
        const isCurrent = !!sessionId && e.id === sessionId;
        item.className = 'sessdock-item' + (isCurrent ? ' is-current' : '');
        item._entry = e; // the release handler wants the entry, not a DOM round-trip
        const nameRow = document.createElement('div');
        nameRow.className = 'name-row';
        const name = document.createElement('div');
        name.className = 'name' + (e.label ? '' : ' placeholder');
        name.textContent = e.label || '(unnamed session)';
        nameRow.appendChild(name);
        const chip = attnChipEl(attnOf(e.id, e.attn));
        if (chip) nameRow.appendChild(chip);
        const meta = document.createElement('div');
        meta.className = 'meta';
        // Formatted at render time — the pool snapshot can be minutes old.
        meta.textContent = e.id + ' · '
          + (isCurrent ? 'current' : e.machine || fmtAgo(e.last));
        item.appendChild(nameRow);
        item.appendChild(meta);
        sessdockEl.appendChild(item);
      }
    }

    function openSessdock() {
      if (sessdockPool.length === 0) return; // nothing to offer
      buildSessdockItems();
      sessdockEl.classList.add('open');
      // Height budget: drop trailing (oldest) items until the whole dock fits
      // above the keybar without touching the header. Keep at least one.
      // Arithmetic from one measurement — a measure-inside-remove loop would
      // force a reflow per iteration, mid-gesture.
      const avail = keybarTop() - 12 - headerBottom();
      const h = sessdockEl.getBoundingClientRect().height;
      if (h > avail && sessdockEl.children.length > 1) {
        const per = sessdockEl.firstChild.offsetHeight + 2; // + the flex gap
        const drop = Math.min(sessdockEl.children.length - 1, Math.ceil((h - avail) / per));
        for (let i = 0; i < drop; i++) sessdockEl.removeChild(sessdockEl.lastChild);
      }
      positionSessdock(); // needs the final rendered height
      // The dock is static for the rest of the gesture — cache each item's
      // box once, so the pointermove hit-test is plain arithmetic instead of
      // an elementFromPoint layout flush per move event.
      sessdockHits = [...sessdockEl.children].map(el => {
        const r = el.getBoundingClientRect();
        return { el, top: r.top, bottom: r.bottom, left: r.left, right: r.right };
      });
    }
    function closeSessdock() {
      sessdockEl.classList.remove('open');
      sessdockHits = [];
      sessdockActiveItem = null;
    }

    // Same dials the panel rows use: an owned machine's session connects
    // PIN-free via the owner handshake; a history entry with its remembered
    // PIN (possibly '' — those dial PIN-free the same way).
    // sessdockDialing says whether the session open here is live, or on its
    // way to it: its socket still connecting, or open with its handshake in
    // flight (not refused) — not yet authed, or authed and its session key
    // still being exchanged. A tap on it in the dock then dials nothing: a
    // second dial would only cut the first short.
    function sessdockDialing() {
      if (inputLive()) return true;
      if (!ws) return false;
      return ws.readyState === WebSocket.CONNECTING || (ws.readyState === WebSocket.OPEN && !fatal && (!authed || !cryptoKey));
    }
    function sessdockConnect(e) {
      // Here and live, or on its way (sessdockDialing): nothing to do. Here
      // but not live (its link dropped, its agent gone, a relay that said
      // no): opened again.
      if (sessionId && e.id === sessionId && sessdockDialing()) return;
      if (wakeIfAsleep(e.id)) return; // asleep: woken first
      if (e.owned) connectOwned(e.id);
      else connectToSession(e.id, e.pin);
    }

    // The gesture. Pointer capture keeps every move on the toggle, and the
    // cached item boxes say which session the finger is over — it never has
    // to be ON the dock, just pointing at it. No preventDefault on
    // pointerdown: a plain tap must still produce the click the hide/show
    // handler listens for; touch-action:none (CSS) already owns the drag.
    let sessdockGesture = null; // { startY, tried } while the pointer is down
    let sessdockLastDragEnd = -1; // when a drag last released — see the ≡ click handler
    let sessdockHits = []; // cached item boxes while open
    let sessdockActiveItem = null; // the item under the finger

    function sessdockItemAt(x, y) {
      const hit = sessdockHits.find(h => y >= h.top && y < h.bottom && x >= h.left && x <= h.right);
      return hit ? hit.el : null;
    }
    sessdockHandle.addEventListener('pointerdown', (e) => {
      sessdockHandle.setPointerCapture(e.pointerId);
      sessdockGesture = { startY: e.clientY, tried: false };
      // Refresh in the dead travel before the 8px threshold — by the time the
      // dock opens, the pool is usually current. Also poll every warm machine
      // connection right now so the chips show the freshest state; replies land
      // via surgical chip updates while the dock stays open (never a mid-drag
      // rebuild).
      refreshSessdockPool();
      pollFleetNow();
    });
    sessdockHandle.addEventListener('pointermove', (e) => {
      const g = sessdockGesture;
      if (!g) return;
      if (!g.tried && g.startY - e.clientY > 8) {
        g.tried = true;
        openSessdock();
      }
      if (!sessdockEl.classList.contains('open')) return;
      const over = sessdockItemAt(e.clientX, e.clientY);
      if (over === sessdockActiveItem) return;
      if (sessdockActiveItem) sessdockActiveItem.classList.remove('active');
      sessdockActiveItem = over;
      if (over) over.classList.add('active');
    });
    sessdockHandle.addEventListener('pointerup', (e) => {
      const g = sessdockGesture;
      sessdockGesture = null;
      if (!g || !g.tried) return; // plain tap — the toggle's own click handler acts
      sessdockLastDragEnd = e.timeStamp; // its synthesized click is not a tap
      const target = sessdockItemAt(e.clientX, e.clientY);
      closeSessdock();
      if (target) sessdockConnect(target._entry); // released on a session → go
    });
    sessdockHandle.addEventListener('pointercancel', () => {
      sessdockGesture = null;
      closeSessdock();
    });

    // ---- Snippet dock (see #snipdock CSS) --------------------------------
    // The session dock's shape, applied to stored text: drag the ✂ button
    // up, slide onto a snippet, release to PASTE it into the terminal — no
    // trailing newline, pasting must never run anything by itself. A plain
    // TAP opens the same dock in manage mode: rows paste on tap and carry
    // edit/remove, plus an add row. Snippets live in localStorage — they're
    // this device's muscle memory ('ls', a long claude invocation), not
    // session state, so they follow the phone, not the host.
    const SNIPPETS_KEY = 'reminal-snippets';
    const snipdockEl = document.getElementById('snipdock');
    const snipHandle = document.getElementById('snip-btn');

    // Entries are { text, name? } — name is an optional display label for
    // when the command itself is too long to recognize at a glance. The
    // first shipped format was a bare string array; normalize on load so
    // those keep working, and anything unrecognizable is dropped rather
    // than rendered as "[object Object]".
    function loadSnippets() {
      try {
        const v = JSON.parse(localStorage.getItem(SNIPPETS_KEY) || '[]');
        if (!Array.isArray(v)) return [];
        return v.map(s => {
          if (typeof s === 'string') return s.length > 0 ? { text: s } : null;
          if (s && typeof s.text === 'string' && s.text.length > 0) {
            return typeof s.name === 'string' && s.name.trim() !== ''
              ? { text: s.text, name: s.name.trim() } : { text: s.text };
          }
          return null;
        }).filter(Boolean);
      } catch (_) { return []; }
    }
    function saveSnippets(list) {
      try { localStorage.setItem(SNIPPETS_KEY, JSON.stringify(list)); } catch (_) {}
    }

    // Same band as the session dock: between header bottom and keybar top,
    // anchored to its own handle.
    function positionSnipdock() {
      if (!snipdockEl.classList.contains('open')) return;
      const kbTop = keybarTop();
      const r = snipdockEl.getBoundingClientRect();
      const hr = snipHandle.getBoundingClientRect();
      const top = Math.max(headerBottom(), kbTop - 12 - r.height);
      const right = Math.max(8, Math.min(window.innerWidth - hr.right,
                                         window.innerWidth - 8 - r.width));
      snipdockEl.style.top = top + 'px';
      snipdockEl.style.right = right + 'px';
    }
    if (window.ResizeObserver) new ResizeObserver(positionSnipdock).observe(sessdockKeybar);
    if (window.visualViewport) visualViewport.addEventListener('resize', positionSnipdock);
    window.addEventListener('resize', positionSnipdock);

    function pasteSnippet(text) {
      const toWindow = typeToTarget(text);
      (toWindow ? wkey : term).focus();
    }

    // askName is the optional second half of add/edit: a display label for a
    // command too long to recognize at a glance. Cancel keeps what was there;
    // empty clears it back to showing the command itself.
    function askName(current) {
      const name = prompt('Name (optional — shown instead of the snippet):', current || '');
      if (name === null) return current || '';
      return name.trim();
    }

    // Manage-mode edits rebuild in place so the dock never closes mid-session
    // of tidying. Edited-to-empty means remove; text is stored VERBATIM
    // otherwise — a trailing space ('cd ') is a deliberate snippet shape.
    function buildSnipdockItems(manage) {
      snipdockEl.innerHTML = '';
      const snips = loadSnippets();
      snips.forEach((snip, i) => {
        const item = document.createElement('div');
        item.className = 'sessdock-item snip-row';
        item._snip = snip;
        // Named: the label on top, the command as the small mono line under
        // it (the session dock's name/meta shape). Unnamed: the command IS
        // the label, in mono.
        const label = document.createElement('div');
        label.className = 'snip-label';
        const name = document.createElement('div');
        name.className = 'name' + (snip.name ? '' : ' cmd');
        name.textContent = snip.name || snip.text;
        label.appendChild(name);
        if (snip.name) {
          const meta = document.createElement('div');
          meta.className = 'meta';
          meta.textContent = snip.text;
          label.appendChild(meta);
        }
        item.appendChild(label);
        if (manage) {
          const edit = document.createElement('button');
          edit.className = 'row-btn';
          edit.title = 'Edit';
          edit.textContent = '✎';
          edit.addEventListener('click', (ev) => {
            ev.stopPropagation();
            const next = prompt('Edit snippet:', snip.text);
            if (next === null) return;
            const list = loadSnippets();
            if (next === '') {
              list.splice(i, 1);
            } else {
              const name = askName(snip.name);
              list[i] = name ? { text: next, name } : { text: next };
            }
            saveSnippets(list);
            buildSnipdockItems(true);
            positionSnipdock();
          });
          const del = document.createElement('button');
          del.className = 'row-btn';
          del.title = 'Remove';
          del.textContent = '✕';
          del.addEventListener('click', (ev) => {
            ev.stopPropagation();
            // Same guard as Recents' forget: ✕ sits a thumb-width from the
            // paste row, and there's no undo. Identify it by its label —
            // truncated, a 200-char one-liner isn't a readable question.
            const what = snip.name || (snip.text.length > 60 ? snip.text.slice(0, 57) + '…' : snip.text);
            if (!confirm('Remove snippet “' + what + '”?')) return;
            const list = loadSnippets();
            list.splice(i, 1);
            saveSnippets(list);
            buildSnipdockItems(true);
            positionSnipdock();
          });
          item.appendChild(edit);
          item.appendChild(del);
          item.addEventListener('click', () => {
            pasteSnippet(snip.text);
            closeSnipdock();
          });
        }
        snipdockEl.appendChild(item);
      });
      if (manage) {
        const add = document.createElement('div');
        add.className = 'sessdock-item snip-add';
        const name = document.createElement('div');
        name.className = 'name';
        name.textContent = '＋ Add snippet';
        add.appendChild(name);
        add.addEventListener('click', (ev) => {
          ev.stopPropagation();
          const text = prompt('New snippet (pasted into the terminal, never run by itself):', '');
          if (!text) return;
          const name = askName('');
          const list = loadSnippets();
          list.push(name ? { text, name } : { text });
          saveSnippets(list);
          buildSnipdockItems(true);
          positionSnipdock();
        });
        snipdockEl.appendChild(add);
      } else if (snips.length === 0) {
        // The drag found nothing stored: say so where the list would be, so
        // the gesture teaches its own setup path instead of doing nothing.
        const hint = document.createElement('div');
        hint.className = 'sessdock-item';
        const name = document.createElement('div');
        name.className = 'name placeholder';
        name.textContent = 'No snippets yet — tap ✂ to add';
        hint.appendChild(name);
        snipdockEl.appendChild(hint);
      }
    }

    let snipdockHits = [];
    let snipdockActiveItem = null;
    let snipdockManage = false;

    function openSnipdock(manage) {
      snipdockManage = manage;
      buildSnipdockItems(manage);
      if (snipdockEl.children.length === 0) return;
      snipdockEl.classList.add('open');
      // Same height budget as the session dock: drop trailing rows until it
      // fits between the header and the keybar.
      const avail = keybarTop() - 12 - headerBottom();
      const h = snipdockEl.getBoundingClientRect().height;
      if (h > avail && snipdockEl.children.length > 1) {
        const per = snipdockEl.firstChild.offsetHeight + 2;
        const drop = Math.min(snipdockEl.children.length - 1, Math.ceil((h - avail) / per));
        for (let i = 0; i < drop; i++) snipdockEl.removeChild(snipdockEl.lastChild);
      }
      positionSnipdock();
      snipdockHits = [...snipdockEl.children].map(el => {
        const r = el.getBoundingClientRect();
        return { el, top: r.top, bottom: r.bottom, left: r.left, right: r.right };
      });
    }
    function closeSnipdock() {
      snipdockEl.classList.remove('open');
      snipdockHits = [];
      snipdockActiveItem = null;
      snipdockManage = false;
    }

    // The drag gesture — a mirror of the session dock's, on its own state.
    let snipGesture = null;
    let snipLastDragEnd = -1;
    function snipItemAt(x, y) {
      const hit = snipdockHits.find(h => y >= h.top && y < h.bottom && x >= h.left && x <= h.right);
      return hit ? hit.el : null;
    }
    snipHandle.addEventListener('pointerdown', (e) => {
      if (snipdockManage) return; // manage panel open — taps belong to its rows
      snipHandle.setPointerCapture(e.pointerId);
      snipGesture = { startY: e.clientY, tried: false };
    });
    snipHandle.addEventListener('pointermove', (e) => {
      const g = snipGesture;
      if (!g) return;
      if (!g.tried && g.startY - e.clientY > 8) {
        g.tried = true;
        openSnipdock(false);
      }
      if (!snipdockEl.classList.contains('open')) return;
      const over = snipItemAt(e.clientX, e.clientY);
      if (over === snipdockActiveItem) return;
      if (snipdockActiveItem) snipdockActiveItem.classList.remove('active');
      snipdockActiveItem = over;
      if (over) over.classList.add('active');
    });
    snipHandle.addEventListener('pointerup', (e) => {
      const g = snipGesture;
      snipGesture = null;
      if (!g || !g.tried) return; // plain tap — the click handler opens manage mode
      snipLastDragEnd = e.timeStamp; // its synthesized click is not a tap
      const target = snipItemAt(e.clientX, e.clientY);
      closeSnipdock();
      if (target && target._snip) pasteSnippet(target._snip.text);
    });
    snipHandle.addEventListener('pointercancel', () => {
      snipGesture = null;
      closeSnipdock();
    });
    snipHandle.addEventListener('click', (e) => {
      if (e.timeStamp - snipLastDragEnd < 400) return; // drag release, not a tap
      if (snipdockEl.classList.contains('open')) {
        closeSnipdock(); // second tap toggles it away
        return;
      }
      openSnipdock(true);
    });
    // Manage mode is a panel, not a gesture: it stays open until a paste, a
    // second ✂ tap, or a tap anywhere outside it.
    document.addEventListener('pointerdown', (e) => {
      if (!snipdockManage) return;
      if (e.target.closest('.wpane')) return; // window gestures are an isolated input surface
      if (snipdockEl.contains(e.target) || snipHandle.contains(e.target)) return;
      closeSnipdock();
    }, true);

    function renderRecents() {
      refreshSessdockPool(); // piggyback: every store change re-renders Recents
      const root = document.getElementById('recents-list');
      if (!root) return;
      const hasMachines = !!document.querySelector('#machines-list .machine-box');
      // Drop sessions already shown under an owned-machine card — they'd be
      // duplicates (e.g. the current session appears in its machine's card).
      // Killed sessions stay out too: the kill flow forgets them from history,
      // but another tab's stale copy or a racing render must not revive one.
      const list = loadHistory().filter(e => !ownedSessionIds.has(e.id) && !killedSessionIds.has(e.id));
      // Live attention per id, from the sessdock pool (refreshed just above).
      // Recents rows are non-owned sessions, so this usually only lights up once
      // a machine we own is also in history; the dock is where it shows richest.
      const attnById = new Map(sessdockPool.filter(p => p.attn).map(p => [p.id, p.attn]));
      if (list.length === 0) {
        // Only show the recents hint when there are no Machines cards above it,
        // otherwise it reads as a redundant second empty-state.
        root.innerHTML = hasMachines
          ? '' : '<div class="recents-empty">No recent sessions yet — connect once and it\'ll show up here.</div>';
        return;
      }
      root.innerHTML = '';
      // When Machines cards are shown above, label this list so the boundary
      // between "machines you own" and "sessions you've connected to" is clear.
      if (hasMachines) {
        const label = document.createElement('div');
        label.className = 'section-label';
        label.textContent = 'Recent sessions';
        root.appendChild(label);
      }
      for (const e of list) {
        const row = document.createElement('div');
        const isCurrent = !!sessionId && e.id === sessionId;
        row.className = 'recents-row' + (isCurrent ? ' is-current' : '');
        row.title = (isCurrent ? 'This is the session you\'re viewing' : 'Click to connect')
          + ' · ' + NEW_TAB_KEY + '-click opens it in a new tab';
        // A real link, so the browser's own open-elsewhere gestures work. The
        // PIN stays out of the URL — the opened tab reads it back out of
        // Recents itself (see historyPin).
        const label = document.createElement('a');
        label.href = sessionHref(e.id);
        label.className = 'label';
        const nameRow = document.createElement('div');
        nameRow.className = 'name-row';
        const nameEl = document.createElement('div');
        nameEl.className = 'name' + (e.name ? '' : ' placeholder');
        nameEl.textContent = e.name || '(unnamed session)';
        nameRow.appendChild(nameEl);
        const chip = attnChipEl(attnOf(e.id, attnById.get(e.id)));
        if (chip) nameRow.appendChild(chip);
        const metaEl = document.createElement('div');
        metaEl.className = 'meta';
        metaEl.textContent = (e.host ? e.host + ' · ' : '') + e.id + ' · ' + fmtAgo(e.lastSeen);
        label.appendChild(nameRow);
        label.appendChild(metaEl);
        const renameBtn = document.createElement('button');
        renameBtn.className = 'row-btn';
        renameBtn.title = 'Rename';
        renameBtn.textContent = '✎';
        renameBtn.addEventListener('click', (ev) => {
          ev.stopPropagation();
          const next = prompt('Name for ' + e.id + ':', e.name || '');
          if (next !== null) renameSession(e.id, next.trim());
        });
        const forgetBtn = document.createElement('button');
        forgetBtn.className = 'row-btn';
        forgetBtn.title = 'Forget this session';
        forgetBtn.textContent = '✕';
        forgetBtn.addEventListener('click', (ev) => {
          ev.stopPropagation();
          if (confirm('Forget ' + (e.name || e.id) + '?')) forgetSession(e.id);
        });
        row.appendChild(label);
        row.appendChild(renameBtn);
        row.appendChild(forgetBtn);
        row.addEventListener('click', (ev) => {
          // A modified click belongs to the browser — it's already opening the
          // row's link elsewhere, and connecting here too would tear down this
          // tab's session as a side effect of asking for a second one.
          if (wantsNewTab(ev)) return;
          ev.preventDefault(); // don't navigate this tab away; connect in place
          // Fill inputs and start a connection to this remembered session.
          sessionInput.value = e.id;
          pinInput.value = e.pin;
          pinForSession = e.pin ? String(e.id).toUpperCase() : '';
          pinTyped = false;
          document.getElementById('recents').open = false;
          if (sessionId && sessionId !== e.id) {
            // Switching to a different session — close the live one first
            // so its socket releases cleanly before we dial a new one.
            userDisconnect({ switching: true });
          }
          startConnect();
        });
        root.appendChild(row);
      }
    }

    // Restore the PIN from sessionStorage if we previously authed this same
    // session in this tab. Refresh now Just Works without re-prompting.
    // sessionStorage (vs localStorage) means the credential dies with the
    // tab, matching the user's intent — closing the tab is "I'm done."
    const CRED_KEY = 'reminal:cred';
    function loadStoredCred(sessionId) {
      if (!sessionId) return '';
      try {
        const raw = sessionStorage.getItem(CRED_KEY);
        if (!raw) return '';
        const c = JSON.parse(raw);
        if (c && c.session === sessionId.toUpperCase() && typeof c.pin === 'string') {
          return c.pin;
        }
      } catch (_) {}
      return '';
    }
    function saveCred(sessionId, pin) {
      try {
        sessionStorage.setItem(CRED_KEY, JSON.stringify({ session: sessionId.toUpperCase(), pin: pin }));
      } catch (_) {}
    }
    function clearCred() {
      try { sessionStorage.removeItem(CRED_KEY); } catch (_) {}
    }
    if (!initialPin) {
      const stored = loadStoredCred(initialSession);
      if (stored) {
        pinInput.value = stored;
        initialPin = stored;
      }
    }
    // Still nothing, but we've connected to this session before? Use the PIN
    // from Recents. sessionStorage above is per-tab and a new tab never inherits
    // it, so without this a ⌘-clicked Recents row would land on a PIN prompt for
    // a credential this browser is already holding. A stale PIN here just fails
    // the handshake and prompts, exactly as clicking the row does.
    if (!initialPin && initialSession) {
      const remembered = historyPin(initialSession);
      if (remembered) {
        pinInput.value = remembered;
        initialPin = remembered;
      }
    }

    // Persisted user-preferred font size (default 14). Tablets/phones with
    // different DPIs render the same px size very differently; let the
    // user dial it via A− / A+ on the keybar.
    const FONT_KEY = 'reminal:fontSize';
    const FONT_MIN = 9;
    const FONT_MAX = 28;
    let initialFontSize = 14;
    try {
      const stored = parseInt(localStorage.getItem(FONT_KEY) || '', 10);
      if (Number.isFinite(stored) && stored >= FONT_MIN && stored <= FONT_MAX) {
        initialFontSize = stored;
      }
    } catch (_) {}
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      fontSize: initialFontSize,
      scrollback: 10000,
      theme: {
        background: '#0d1117', foreground: '#e6edf3', cursor: '#58a6ff',
        selectionBackground: '#264f78',
      },
    });
    const searchAddon = new SearchAddon.SearchAddon();
    term.loadAddon(searchAddon);
    // Any http(s)://… in terminal output becomes a tap target that opens in
    // a new tab — on a phone you cannot copy a URL into the address bar.
    // Links are found across rows, not one row at a time: an agent that
    // word-wraps its own text breaks a long URL with a real newline and
    // indents the rest, and the first row alone opened a 404 (see
    // reminalLinks).
    term.registerLinkProvider({
      provideLinks(y, cb) {
        const buf = term.buffer.active;
        const rows = [];
        const at = y - 1;
        const from = Math.max(0, at - 40), to = Math.min(buf.length - 1, at + 40);
        for (let i = from; i <= to; i++) {
          const l = buf.getLine(i);
          rows.push({ text: l ? l.translateToString(true) : '', wrapped: !!(l && l.isWrapped) });
        }
        const found = reminalLinks.linksInRows(rows, at - from);
        cb(found.length ? found.map(k => ({
          text: k.url,
          range: { start: { x: k.start.col + 1, y: k.start.row + from + 1 }, end: { x: k.end.col + 1, y: k.end.row + from + 1 } },
          // reminalLinks only finds http(s); checked again here so nothing else
          // can ever be opened from the terminal.
          activate: (ev, text) => { if (/^https?:\/\//i.test(text)) window.open(text, '_blank', 'noopener,noreferrer'); },
        })) : undefined);
      },
    });
    // Links a program in the terminal names itself (OSC 8): web addresses
    // only, opened apart from this page — a javascript: one would run as it.
    term.options.linkHandler = {
      allowNonHttpProtocols: false,
      activate: (ev, uri) => { if (/^https?:\/\//i.test(uri)) window.open(uri, '_blank', 'noopener,noreferrer'); },
    };
    term.open(document.getElementById('terminal'));
    window.__term = term; // debug/test handle (harmless; used by automated viewer tests)
    // Chrome Android pans the layout to keep xterm's hidden textarea in
    // view. That is the "everything jumps up, hole under the prompt"
    // keyboard bug: the wrap already shrank, then the browser scrolled
    // the page anyway. Keep focus; do not pan.
    (function () {
      const orig = term.focus.bind(term);
      term.focus = function () {
        const ta = term.textarea;
        if (ta && ta.focus) {
          try { ta.focus({ preventScroll: true }); return; } catch (_) {}
        }
        orig();
      };
    })();
    // Per-pane video health, for automated tests and field diagnosis: AUs
    // decoded, sequence gaps seen (each one means the host's stream and this
    // decoder diverged), and decoder errors. Cheap counters, no behaviour.
    window.__winStats = () => [...panes.values()].map((p) => ({
      id: p.w.id, cap: p.capSrc || null, aus: p.h264AUs || 0,
      gaps: p.h264Gaps || 0, errs: p.h264Errs || 0, batches: p.h264Batches || 0,
      paintMs: (() => { const g = (p.paintGaps || []).slice().sort((a, b) => a - b);
        return g.length ? Math.round(g[Math.floor(g.length / 2)]) : 0; })(),
      queued: (p.renderQ || []).length,
      fps: (p.frameTimes || []).length, seq: p.lastSeq || 0,
    }));

    // Pointer-ownership guard, same rule as scroll routing below: an app only
    // gets REAL mouse events (clicks, drags) when it is a full-screen
    // alternate-buffer app. Inline TUIs on the normal screen (Claude Code)
    // also request mouse reporting — and once xterm activates it, a plain
    // drag is forwarded to the app instead of selecting, so "select + Cmd+C"
    // silently copies nothing. Suppress the protocol while the normal buffer
    // is active (the app simply sees a terminal without mouse support) and
    // restore it the moment an alt-screen app takes over.
    (function mainScreenPointerGuard() {
      let suppressed = null; // protocol the app requested while on the main screen
      const apply = () => {
        let svc = null;
        try { svc = term._core.coreMouseService; } catch (_) { return; }
        if (!svc) return;
        const alt = term.buffer.active.type === 'alternate';
        if (!alt && svc.activeProtocol !== 'NONE') {
          suppressed = svc.activeProtocol;
          svc.activeProtocol = 'NONE';
        } else if (alt && suppressed) {
          svc.activeProtocol = suppressed;
          suppressed = null;
        }
      };
      term.buffer.onBufferChange(apply);
      if (term.onWriteParsed) term.onWriteParsed(apply); else setInterval(apply, 250);
    })();

    // Wheel routing, same rule as touch and the pointer guard above: an app
    // only receives scroll when it is a full-screen (alternate-buffer) app
    // with mouse reporting on. The pointer guard already pins activeProtocol
    // to NONE on the normal screen, so xterm handles the wheel natively there;
    // this capture-phase interceptor is the last line of defense for the
    // sub-frame window between an app enabling mouse mode and the guard
    // running (and documents the rule where a reader will look for it).
    (function routeWheel() {
      const host = document.getElementById('terminal');
      if (!host) return;
      let acc = 0;
      host.addEventListener('wheel', (e) => {
        let mainScreenMouseApp = false;
        try {
          mainScreenMouseApp = term.buffer.active.type !== 'alternate' &&
                               term._core.coreMouseService.activeProtocol !== 'NONE';
        } catch (_) {}
        if (!mainScreenMouseApp) return;            // let xterm handle it normally
        let h = 16;
        try { const c = term._core._renderService.dimensions.css.cell.height; if (c > 0) h = c; } catch (_) {}
        acc += (e.deltaMode === 1 ? e.deltaY * h : e.deltaY);
        const lines = (acc / h) | 0;
        if (lines) { term.scrollLines(lines); acc -= lines * h; }
        e.preventDefault();
        e.stopImmediatePropagation();
      }, { capture: true, passive: false });
    })();

    // Defang the hidden helper textarea that xterm.js routes keystrokes
    // through. Without this, Android Chrome's IME (Gboard etc.) treats
    // each shell command as an English word — autocorrect rewrites it
    // on space and the typed text gets replaced with gibberish.
    //
    // The HTML attributes alone aren't enough: Gboard infamously
    // ignores autocorrect="off" in many cases. We need three layers:
    //
    //   1. The attributes (worst-case opt-out).
    //   2. inputmode="url" — the URL keyboard has no autocorrect
    //      built-in. Layout differs slightly from QWERTY-with-text
    //      (extra `/` `:` symbols on the main row, no smart period)
    //      but it never composes or autocorrects. This is the same
    //      trick Termius / Blink / a-Shell use for the same problem.
    //   3. beforeinput interceptor — suppress the specific event
    //      types Gboard fires when it tries to REPLACE typed text
    //      (insertReplacementText / insertFromComposition). xterm's
    //      own input handler still receives the genuine keystrokes
    //      that came BEFORE the replacement attempt.
    const helperTA = document.querySelector('.xterm-helper-textarea');
    // APPLE_MOBILE: iOS reports as iPhone/iPad/iPod, or as "MacIntel" with a
    // touch screen (iPadOS masquerading as desktop Safari). Top-level (not
    // inside the helperTA block) because sendInput and the keybar also branch
    // on it for the iOS field-mirror.
    const APPLE_MOBILE = /iP(hone|ad|od)/.test(navigator.userAgent) ||
      (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
    // imeField: iOS field-mirror — the text we've mirrored to the shell for the
    // current IME field (see the beforeinput handler). Top-level so sendInput
    // can invalidate it when out-of-band input (keybar, paste) edits the line.
    let imeField = '';
    // Android: where the IME double-fires (Samsung Keyboard commits a tap
    // through composition and then sends the same character again as
    // insertText). The filters for that are for it alone: a keyboard
    // anywhere else sends each key once, and a key repeated — held down, or
    // typed fast — is meant.
    const ANDROID = /Android/i.test(navigator.userAgent);
    if (helperTA) {
      const isAppleMobile = APPLE_MOBILE;
      helperTA.setAttribute('autocorrect', 'off');
      helperTA.setAttribute('autocomplete', 'off');
      helperTA.setAttribute('autocapitalize', 'none');
      helperTA.setAttribute('spellcheck', 'false');
      // inputmode="url" is the only reliable way to disable Android Gboard's
      // autocorrect (it ignores autocorrect="off"). But on iOS the URL keyboard
      // ALSO removes the dictation MIC button — and iOS Safari *does* honour
      // autocorrect="off", so it never needed the URL trick. So: a text
      // inputmode on iOS (mic returns, attributes keep autocorrect off), and
      // "url" on Android where it's load-bearing.
      helperTA.setAttribute('inputmode', isAppleMobile ? 'text' : 'url');
      helperTA.setAttribute('enterkeyhint', 'done');
      helperTA.style.setProperty('position', 'fixed', 'important');
      helperTA.style.setProperty('left', '0', 'important');
      helperTA.style.setProperty('top', '0', 'important');
      helperTA.style.setProperty('width', '1px', 'important');
      helperTA.style.setProperty('height', '1px', 'important');
      helperTA.addEventListener('focus', applyViewportHeight);
      helperTA.addEventListener('input', applyViewportHeight, true);
      // Intercept autocorrect/replacement events at beforeinput so the
      // typed text never gets clobbered. Capture phase + preventDefault
      // stops the replacement from reaching xterm's listener.
      // Samsung Keyboard runs an "autocorrect the previous word" loop
      // after every non-letter (space, `.`, `/`, `-`, …) that fires a
      // fresh composition emitting insertCompositionText events that
      // re-insert the "corrected" word — but the IME's cursor model is
      // desynced from the textarea, so each insert mutates the middle of
      // the buffer instead of appending. Result: doubled punctuation
      // and mangled words. Trace captured with ?debug=1 confirmed.
      //
      // Defence: only block insertCompositionText when we detect the
      // current composition is autocorrect-spawned, not user-typed.
      // Two cheap signals discriminate the two:
      //   (A) Composition started <50ms after a non-composing input —
      //       below human reaction time, so it can't be the user
      //       starting a new word. This catches the "fires right after
      //       space" autocorrect pattern.
      //   (B) The first compositionupdate after compositionstart
      //       contains MORE than one character — real human typing
      //       grows composition by one char per keydown ("d" → "da" →
      //       "dan"). Autocorrect arrives pre-formed ("--dang" → …).
      // If either fires, this composition is flagged as suspicious and
      // its insertCompositionText events are dropped. Real backspace,
      // real letter input, and real composition all pass through
      // untouched.
      let lastCommittedData = null;        // last char/word committed via composition OR insertText
      let lastCommittedAt = -Infinity;
      // How it was committed: 'composition' (compositionend), 'punct' (a
      // Samsung smart-punctuation tap, re-sent below), or 'key' (an
      // insertText of its own). Only a composition's or a smart-punctuation's
      // commit is followed by the IME's duplicate; one insertText after
      // another is a key repeated, never a double-fire.
      let lastCommitSource = '';
      let inSuspiciousComposition = false;
      let compositionFirstUpdateSeen = false;
      let compositionStartedCold = false;  // composition began with no recent typing → user speech/paste, not autocorrect
      let iosComposing = false;            // iOS: a CJK composition is live — the mirror and keydown interference stand down
      const DUP_WINDOW_LETTER_MS = 60;     // letters: tight, so "letter" still types fast
      const DUP_WINDOW_PUNCT_MS = 80;      // punctuation: slightly wider; tighter than this lets Samsung dup-fires through, wider feels sluggish for deliberate ".." typing
      const PUNCT_RX = /^[.,!?;:'"\/\\(){}\[\]<>@#$%^&*\-_+=|`~]$/;
      const NEW_COMPOSITION_AUTOCORRECT_WINDOW_MS = 50;
      // Voice dictation arrives as a composition whose data is a whole word or
      // phrase — the same SHAPE as autocorrect, so shape alone can't tell them
      // apart. Timing can: autocorrect fires WHILE you type (it rewrites the
      // word you just committed, so it starts within a few hundred ms of the
      // last commit), whereas dictation starts COLD — a deliberate mic tap with
      // no keystroke in the last moment. A composition that begins more than
      // this long after the last committed char is treated as user-initiated (a
      // new word or dictation) and is never dropped. Tunable per real-device
      // traces (?debug=1).
      const AUTOCORRECT_ACTIVE_TYPING_MS = 1500;

      const isHelperTarget = (e) => e.target === helperTA;

      // Phantom-deletion defence. Samsung Keyboard hits us from two
      // angles around backspace:
      //   • phantom keydown Backspace bursts with no matching
      //     beforeinput/input pair, and
      //   • phantom `input deleteContentBackward` events with NO
      //     keydown and NO beforeinput at all (the IME mutates the
      //     textarea behind our back during its autocorrect cascade —
      //     see events 250–259 in the captured trace).
      // Both routes end up at sendInput as \x7f bytes that xterm emits
      // from processing the deletion. So instead of trying to gate the
      // input events here (Android Chrome treats many of them as
      // non-cancelable from IME sources anyway), we let xterm do its
      // thing and gate at the OUTPUT layer: count real Backspace
      // keydowns and only let \x7f through sendInput when there's a
      // count to consume. See realBackspacesPending in the sendInput
      // block above. Counter decays after 100ms so a single phantom
      // keydown doesn't leak budget into a subsequent phantom deletion
      // burst.
      // Minimum interval between Backspace-keydown counter credits.
      // Samsung's phantom Backspace bursts arrive at sub-millisecond
      // intervals (40 keydowns in <40ms). Real Backspace input —
      // single tap OR held autorepeat — is ≥30ms apart. 20ms gives
      // headroom for autorepeat while still excluding bursts.
      let lastBackspaceCreditAt = -Infinity;
      const BS_MIN_INTERVAL_MS = 20;

      document.addEventListener('keydown', (e) => {
        if (!isHelperTarget(e)) return;
        if (isAppleMobile) {
          // While a CJK composition is live, Enter CONFIRMS the composition and
          // Backspace edits it — neither is ours to touch. Clearing the field
          // here would destroy the half-composed word; stand down and let the
          // IME + xterm's CompositionHelper run it.
          if (iosComposing) return;
          // Enter submits the line — the mirrored field is gone; xterm's own
          // keydown handling sends the '\r'.
          if (e.key === 'Enter') { imeField = ''; helperTA.value = ''; return; }
          if (e.key === 'Backspace') {
            // Keep xterm's keydown handler out of it: xterm would cancel the
            // keydown (so the browser never edits the field and beforeinput
            // never fires) and emit its own \x7f — invisible to the mirror.
            e.stopPropagation();
            if (helperTA.value === '') {
              // Nothing in the field for the browser to delete, but a real
              // backspace must still reach the shell (a TUI, a line that
              // predates this page, a hardware keyboard whose typed chars
              // never enter the field). Same timing gate as elsewhere, so
              // iOS's phantom pre-dictation Backspace bursts (sub-20ms runs,
              // always with an empty field) leak at most one — and that one
              // lands on an empty readline, where \x7f is a no-op.
              const bnow = performance.now();
              if (bnow - lastBackspaceCreditAt >= BS_MIN_INTERVAL_MS) {
                lastBackspaceCreditAt = bnow;
                realBackspacesPending++;
                sendInput('\x7f', { ime: true });
              }
              e.preventDefault(); // no edit to perform; also stops a spurious empty delete event
            }
            // Field non-empty: let the browser's default edit run — the
            // beforeinput deleteContentBackward below mirrors it as \x7f.
            return;
          }
          return;
        }
        if (e.key === 'Backspace') {
          // NB: helperTA is almost always empty (xterm clears it after
          // each committed char), so we can't gate on textarea content
          // — that breaks the user's ability to backspace anything
          // they previously typed into Claude Code's input buffer.
          // Use timing instead: anything closer than BS_MIN_INTERVAL_MS
          // to the previous Backspace credit is part of a phantom burst.
          const now = performance.now();
          if (now - lastBackspaceCreditAt < BS_MIN_INTERVAL_MS) return;
          lastBackspaceCreditAt = now;
          realBackspacesPending++;
          setTimeout(() => {
            if (realBackspacesPending > 0) realBackspacesPending--;
          }, 100);
        }
      }, true);

      // Losing focus abandons the current IME field — drop the mirror so a later
      // dictation can't diff against stale text and backspace into the shell.
      helperTA.addEventListener('blur', () => { imeField = ''; });

      // iOS CJK: a composition runs through xterm's CompositionHelper (the
      // mirror stands down — see the beforeinput handler). iosComposing keeps
      // the keydown handler's Enter/Backspace interference off while it's live;
      // on commit, re-adopt whatever xterm left in the field as the mirror's
      // model, a tick later so xterm's synchronous finalize/clear has already
      // run. Without the resync, the next mirrored edit would diff against ''
      // and re-send the composed text.
      if (isAppleMobile) {
        helperTA.addEventListener('compositionstart', () => { iosComposing = true; });
        helperTA.addEventListener('compositionend', () => {
          iosComposing = false;
          setTimeout(() => { imeField = helperTA.value; }, 0);
        });
      }

      // Document-level capture so we run BEFORE xterm's helperTA
      // listeners (xterm registered first during term.open()).
      document.addEventListener('beforeinput', (e) => {
        if (!isHelperTarget(e)) return;

        // ── iOS: mirror the hidden field ──────────────────────────────────
        // On iOS every edit — plain typing, voice dictation, and deletes —
        // arrives as a beforeinput that rewrites the field. Dictation is why this
        // exists (captured with ?debug=1&raw=1): it streams its running
        // transcription, first REPLACING the field with the whole growing phrase
        // ("Hello" → "Hello, how are?" → "Hello, how are you?"), then re-emitting
        // it word by word ("hello," → "hello, how" → …). Forwarding each e.data
        // verbatim makes the shell accumulate every partial. So we don't forward
        // events at all: compute what the field BECOMES, diff it against what
        // we've already shown (imeField), and send only backspaces-for-the-
        // changed-tail plus the new tail — then preventDefault so xterm never
        // sends, keeping the field as our own model. Android keeps the switch
        // below (its autocorrect needs different, composition-based handling).
        if (isAppleMobile) {
          const t = e.inputType;
          // A live CJK composition owns the field: its events mutate the
          // textarea natively and xterm's CompositionHelper both tracks it and
          // emits the composed text on compositionend (mirroring the commit too
          // would send it twice). Stand down for the whole composition — the
          // compositionend listener below resyncs the mirror afterwards. This
          // MUST precede the invariant check: mid-composition the field
          // legitimately diverges from the mirror, and "correcting" it would
          // wipe the user's half-composed word.
          if (e.isComposing || t === 'insertCompositionText' || t === 'insertFromComposition') {
            imeField = '';
            return;
          }
          let cur = helperTA.value;
          let a = helperTA.selectionStart, b = helperTA.selectionEnd;
          // Invariant check — the self-correcting heart of the mirror. Between
          // events, the field must contain exactly what we last mirrored
          // (imeField): every edit path we know about keeps them equal. If they
          // diverge, something wrote the textarea behind our back — xterm's
          // right-click handler stuffs the SELECTION into it for native copy,
          // its Linux middle-click path does the same, and future xterm
          // versions may add more. Diffing against phantom text would re-send
          // it into the shell (or backspace real characters), so instead:
          // forget it all and start this edit from a clean field. The shell
          // keeps everything already sent; the current keystroke still lands.
          if (cur !== imeField) {
            imeField = '';
            helperTA.value = '';
            cur = '';
            a = 0;
            b = 0;
          }
          let next = null;
          if (t === 'insertText' || t === 'insertReplacementText' ||
              t === 'insertFromPaste' || t === 'insertFromDictation') {
            next = cur.slice(0, a) + (e.data || '') + cur.slice(b);
          } else if (t === 'deleteContentBackward' || t === 'deleteContent') {
            // A collapsed-caret delete removes one CHARACTER, which in UTF-16
            // can be a surrogate pair (emoji) — slicing one code unit would
            // split the pair.
            next = (a === b) ? Array.from(cur.slice(0, a)).slice(0, -1).join('') + cur.slice(b)
                             : cur.slice(0, a) + cur.slice(b);
          } else if (t === 'deleteContentForward') {
            next = (a === b) ? cur.slice(0, a) + Array.from(cur.slice(b)).slice(1).join('')
                             : cur.slice(0, a) + cur.slice(b);
          }
          if (t === 'insertLineBreak' || t === 'insertParagraph') {
            // Return delivered as an EDIT (some iOS keyboards/flows emit this
            // instead of — or in addition to — a keydown Enter). Submit the
            // line deterministically: never let a literal newline into the
            // field, send the CR ourselves, and start the next line fresh.
            e.preventDefault();
            e.stopPropagation();
            imeField = '';
            helperTA.value = '';
            sendInput('\r', { ime: true });
            return;
          }
          if (next !== null) {
            e.preventDefault();
            e.stopPropagation();
            // Diff in CODEPOINTS, not UTF-16 code units: the shell deletes one
            // character per \x7f, so counting code units would send TWO
            // backspaces for one revised emoji and eat an extra real character.
            const prevCP = Array.from(imeField);
            const nextCP = Array.from(next);
            let common = 0;
            const min = Math.min(prevCP.length, nextCP.length);
            while (common < min && prevCP[common] === nextCP[common]) common++;
            const recon = '\x7f'.repeat(prevCP.length - common) + nextCP.slice(common).join('');
            if (recon) {
              // Already sent by xterm's keydown path for this same keystroke?
              // Then this edit is its echo, not a second character. The field
              // and the mirror still take it below, so the shell and the model
              // agree and a later backspace deletes the right thing.
              const echoOfKeydown = recon === lastNonMirrorSend &&
                (performance.now() - lastNonMirrorSendAt) < MIRROR_DUP_WINDOW_MS;
              if (echoOfKeydown) {
                lastNonMirrorSend = null;
              } else {
                // A lone \x7f is exactly what sendInput's phantom-backspace gate
                // drops — this one is a real, mirrored edit, so fund it.
                if (recon === '\x7f') realBackspacesPending++;
                sendInput(recon, { ime: true });
              }
            }
            imeField = next;
            helperTA.value = next;
            try { helperTA.setSelectionRange(next.length, next.length); } catch (_) {}
            lastCommittedData = e.data;
            lastCommittedAt = performance.now();
            return;
          }
          // Unhandled edit (composition, word-delete, undo/redo, …): abandon the
          // mirror so we never diff against a stale model, and let xterm handle
          // it. Field and mirror re-converge on the next mirrored edit or the
          // compositionend resync.
          imeField = '';
          return;
        }

        switch (e.inputType) {
          case 'insertReplacementText':
          case 'insertFromComposition':
          case 'historyUndo':
          case 'historyRedo':
            e.preventDefault();
            e.stopPropagation();
            return;
          case 'insertCompositionText':
            if (inSuspiciousComposition) {
              e.preventDefault();
              e.stopPropagation();
            }
            return;
          case 'insertText':
            if (!e.isComposing) {
              const now = performance.now();
              // Samsung "smart punctuation": a single tap on `.`, `,`,
              // `?`, etc. delivers MULTI-CHAR data — typically the
              // punctuation followed by an auto-space (e.g. d=". ").
              // Strip everything after the first char and re-inject
              // just the punctuation via sendInput. Longer multi-char
              // insertText is paste (e.g. clipboard) — leave alone.
              if (ANDROID && e.data && e.data.length > 1 && e.data.length <= 3 && PUNCT_RX.test(e.data[0])) {
                e.preventDefault();
                e.stopPropagation();
                sendInput(e.data[0]);
                lastCommittedData = e.data[0];
                lastCommittedAt = now;
                lastCommitSource = 'punct';
                return;
              }
              // Dup-fire: a single key tap commits via composition
              // (compositionend d="?") and THEN immediately fires one OR
              // MORE duplicate insertText d="?". Block any insertText
              // within the dup-window of an earlier commit with the same
              // data. Don't refresh lastCommittedAt on a block — Samsung
              // sometimes fires multiple dups in sequence. Window is
              // wider for punctuation than letters: punctuation dup-fires
              // arrive ~80ms later; letters need a tight window so
              // "letter" still types fast.
              // On Android only, and only after a commit that is followed
              // by a duplicate (a composition's, a smart punctuation's):
              // after an insertText of its own the same key again is the
              // key typed again — "44", "((", a key held down — and is
              // never dropped (a desktop keyboard lost them all).
              const dupWindow = PUNCT_RX.test(e.data) ? DUP_WINDOW_PUNCT_MS : DUP_WINDOW_LETTER_MS;
              if (ANDROID && (lastCommitSource === 'composition' || lastCommitSource === 'punct') &&
                  e.data === lastCommittedData && (now - lastCommittedAt) < dupWindow) {
                e.preventDefault();
                e.stopPropagation();
                return;
              }
              lastCommittedData = e.data;
              lastCommittedAt = now;
              lastCommitSource = 'key';
            }
            return;
        }
      }, true);

      document.addEventListener('compositionstart', (e) => {
        if (!isHelperTarget(e)) return;
        compositionFirstUpdateSeen = false;
        const dt = performance.now() - lastCommittedAt;
        inSuspiciousComposition = dt < NEW_COMPOSITION_AUTOCORRECT_WINDOW_MS;
        // No recent commit → not part of an autocorrect cascade. This is the
        // user starting fresh: a new word, a paste, or a voice dictation. Never
        // suppress it, whatever shape its data takes.
        compositionStartedCold = dt >= AUTOCORRECT_ACTIVE_TYPING_MS;
      }, true);

      // Pattern that real English almost never has — 3+ consecutive
      // identical characters. Samsung's "extend the composition with
      // hallucinated characters" autocorrect produces strings like
      // "hmmhhmmmm" / "yessss" / "noooo"; this catches them. Trade-off:
      // intentional emphasis ("hmmm", "okayyy") gets flagged too, but
      // those aren't useful shell input anyway.
      const AUTOCORRECT_REPEAT_RX = /(.)\1{2,}/;

      document.addEventListener('compositionupdate', (e) => {
        if (!isHelperTarget(e)) return;
        // A cold-started composition is the user speaking or pasting, not the
        // IME rewriting recent keystrokes — leave it entirely alone so dictated
        // words reach the shell instead of being dropped on commit.
        if (compositionStartedCold) return;
        if (!compositionFirstUpdateSeen) {
          compositionFirstUpdateSeen = true;
          // Real typing: first update is 1 char ("d"). Autocorrect:
          // first update arrives multi-char ("--dang", "you're").
          if ((e.data || '').length > 1) inSuspiciousComposition = true;
        }
        // Repeated-char pattern check on every update — catches Samsung
        // growing a normal-looking composition with hallucinated chars
        // (single-char growth bypasses the first-update heuristic).
        if (AUTOCORRECT_REPEAT_RX.test(e.data || '')) {
          inSuspiciousComposition = true;
        }
      }, true);

      document.addEventListener('compositionend', (e) => {
        if (!isHelperTarget(e)) return;
        if (inSuspiciousComposition && e.data) {
          // beforeinput insertCompositionText events are non-cancelable
          // per spec, so we can't block at the event layer. xterm
          // processes compositionend synchronously and calls
          // term.onData. Two suppression mechanisms paired:
          //   (a) suppressNextDataChunk — exact-string match, dropped
          //       on first sendInput call (cheap and precise).
          //   (b) suppressMultiCharUntil — time-windowed multi-char
          //       drop for SUSPICIOUS_DROP_MS, as a backstop. xterm
          //       sometimes commits via "\b\b\bnewword" or multi-call
          //       splits that the exact-match misses.
          suppressNextDataChunk = e.data;
          clearTimeout(suppressTimer);
          suppressTimer = setTimeout(() => { suppressNextDataChunk = null; }, 200);
          suppressMultiCharUntil = performance.now() + SUSPICIOUS_DROP_MS;
        }
        inSuspiciousComposition = false;
        compositionFirstUpdateSeen = false;
        if (e.data) {
          lastCommittedData = e.data;
          lastCommittedAt = performance.now();
          lastCommitSource = 'composition';
        }
      }, true);

      // Debug overlay (?debug=1): mirrors every keystroke / composition
      // event into a fixed corner panel so we can see exactly which
      // inputType / data Samsung Keyboard (and other IMEs) emit when
      // they mangle typed shell commands. Off by default.
      if (new URLSearchParams(location.search).get('debug') === '1') {
        const ov = document.createElement('div');
        ov.style.cssText = [
          'position:fixed','top:120px','right:6px',
          'max-width:300px','max-height:42vh','overflow:auto',
          'background:rgba(0,0,0,0.88)','color:#0f0',
          'font:11px ui-monospace,SFMono-Regular,Menlo,monospace',
          'padding:6px 8px','border:1px solid #0f0','border-radius:4px',
          'z-index:99999','white-space:pre','pointer-events:auto',
        ].join(';');
        ov.title = 'tap to clear';
        document.body.appendChild(ov);
        const buf = []; let n = 0;
        const fmt = (label, e) => {
          const parts = [String(++n).padStart(3, ' '), label];
          if (e.inputType) parts.push('it=' + e.inputType);
          if (e.key !== undefined) parts.push('k=' + JSON.stringify(e.key));
          if (e.keyCode !== undefined && e.keyCode !== 0) parts.push('kc=' + e.keyCode);
          if ('isComposing' in e) parts.push('ic=' + (e.isComposing ? '1' : '0'));
          if (e.data !== undefined && e.data !== null) parts.push('d=' + JSON.stringify(e.data));
          parts.push('v=' + JSON.stringify(helperTA.value));
          return parts.join(' ');
        };
        const log = (label, e) => {
          buf.push(fmt(label, e));
          if (buf.length > 40) buf.shift();
          ov.textContent = buf.join('\n');
          ov.scrollTop = ov.scrollHeight;
        };
        ov.addEventListener('click', () => { buf.length = 0; n = 0; ov.textContent = ''; });
        ['keydown','beforeinput','input','compositionstart','compositionupdate','compositionend']
          .forEach(t => helperTA.addEventListener(t, (e) => log(t, e), true));
      }
    }

    // Connection state machine
    let ws = null;
    let cryptoKey = null;
    let authed = false;       // received auth_ok
    let agentLive = false;    // agent is currently connected on the relay
    let lastSeq = 0;          // highest seq we've received from agent
    // While nonzero (a deadline), this viewer is owed a fresh snapshot paint —
    // set on any fresh attach / refresh, cleared when a wipe-signature frame
    // lands. Gates the stale-tag dedup bypass in the data handler.
    let awaitingSnapshotUntil = 0;
    let sessionId = '';
    let pinValue = '';
    let manualClose = false;  // user clicked Disconnect; do not auto-reconnect
    let fatal = false;        // unrecoverable (bad PIN, session expired); do not auto-reconnect
    let fatalWords = '';      // what the relay said when it ended or refused the session: kept over the terminal
    const uploadWaiters = new Set(); // uploads waiting for the link to be back (uploadLinkWhenBack); here, before anything can settle them
    let reconnectTimer = null;
    let backoffMs = 1000;
    // The first close after a session that was up redials at once: most are
    // a blip (a relay deploy, a radio handoff) that a second dial goes
    // straight through. Any close after that waits the backoff (1 s, 2 s, …),
    // and only a session up again earns another at-once redial.
    let redialNow = false;
    let lastPingAt = 0;
    const MAX_BACKOFF_MS = 30000;
    // Dial watchdog: a mobile radio handoff (5G↔Wi-Fi) can leave a WebSocket
    // black-holed in CONNECTING — no onopen, no onclose, no onerror — so the
    // "Reconnecting…" overlay would sit forever with nothing to rescue it (the
    // `online` event doesn't fire when the network stays nominally up). If we
    // don't reach auth_ok within this window, force the socket closed so the
    // normal onclose → scheduleReconnect path retries. Cleared the moment we
    // authenticate; a legitimately-slow-but-progressing connect is rare within
    // 12s, and a needless retry is cheaper than a hang.
    let dialTimer = null;
    const DIAL_TIMEOUT_MS = 12000;
    let connectedAt = 0;
    // helloShown gates the one-time "Connected · AES-256-GCM end-to-end" line
    // written into the terminal; bytesSent/Received feed the summary line on
    // disconnect. Mirrors the terminal viewer's helloOnce + counters.
    let helloShown = false;
    // Set whenever we drop the live agent (WS close OR agent_offline
    // notification). Cleared the next time startSessionFlow prints
    // the "Reconnected" line — so each disconnect/reconnect cycle
    // surfaces exactly one in-terminal confirmation, no matter how
    // many WS retries it took to get back.
    let reconnectMessagePending = false;
    let bytesSent = 0;
    let bytesReceived = 0;
    let lastDialMs = 0; // measured from new WebSocket() to ws.onopen
    // On-screen debug log (add ?debug or #debug to the URL). Lets us watch the
    // handshake on a phone with no Web Inspector — every relay message, the kex
    // send/recv, ex_id matching, and timeouts scroll into a panel at the bottom.
    const DEBUG = new URLSearchParams(location.search).get('debug') === 'true';
    let dbgEl = null;
    function dbg(m) {
      if (!DEBUG) return;
      try { console.log('[dbg]', m); } catch (_) {}
      if (!dbgEl) {
        dbgEl = document.createElement('div');
        dbgEl.style.cssText = 'position:fixed;left:0;right:0;bottom:0;max-height:45vh;overflow:auto;z-index:99999;background:rgba(0,0,0,0.88);color:#7ee787;font:11px/1.45 ui-monospace,monospace;padding:6px 8px 10px;white-space:pre-wrap;border-top:1px solid #30363d;';
        (document.body || document.documentElement).appendChild(dbgEl);
      }
      dbgEl.textContent += new Date().toISOString().slice(11, 23) + '  ' + m + '\n';
      dbgEl.scrollTop = dbgEl.scrollHeight;
    }
    // Upload IDs THIS viewer originated — used to filter the agent's
    // upload_ack broadcast so only the originator auto-pastes the path.
    const pendingUploadIDs = new Set();
    // An upload waiting on its ack, by upload_id: given the saved path (the
    // upload toast's "Saving…" turns to where it landed).
    const uploadAckWaiters = new Map();

    function humanBytes(n) {
      if (n < 1000) return n + ' B';
      if (n < 1000000) return (n / 1000).toFixed(2) + ' KB';
      if (n < 1000000000) return (n / 1000000).toFixed(2) + ' MB';
      return (n / 1000000000).toFixed(2) + ' GB';
    }

    // humanDuration formats seconds as 47s / 8m32s / 1h12m. Matches the
    // Go time.Duration string output the terminal viewer uses.
    function humanDuration(secs) {
      if (secs < 60) return secs + 's';
      const m = Math.floor(secs / 60);
      const s = secs % 60;
      if (m < 60) return m + 'm' + (s > 0 ? s + 's' : '');
      const h = Math.floor(m / 60);
      const mm = m % 60;
      return h + 'h' + (mm > 0 ? mm + 'm' : '');
    }

    // ---- v2 key exchange (EKE-style PIN-authenticated X25519) ----
    //
    // v1 derived the session AES key directly from (PIN, sessionID) via
    // HKDF. The sessionID is the relay's routing key, so the secret part
    // was just the 6-digit PIN — about 20 bits, offline-brute-forceable
    // by anyone who recorded a ciphertext frame. (See GitHub issue #1.)
    //
    // It is now a CPace handshake (cpaceStart / cpaceFinish below, and
    // internal/crypto/cpace.go): the PIN only chooses the generator both sides
    // do Diffie-Hellman over, the host wraps a random 256-bit session key under
    // the key both derive, and nothing derived from the PIN goes over the wire.
    //
    // Per-handshake state. All four are reset by openSocket() on every
    // reconnect; completeKex clears the ephemeral private material as
    // soon as the wrap is decrypted.
    // All outstanding pake_init attempts, keyed by their hex ex_id →
    // { cp, exIDBytes }. We can have more than one in flight: the
    // viewer fires an optimistic pake_init on auth_ok and another on
    // connected/agent_online. Whichever the agent answers FIRST completes the
    // handshake — so a dropped/coalesced reply to one attempt no longer strands
    // the connection (the earlier iOS "handshake timed out" bug, where we kept
    // only the latest ex_id and discarded a valid reply to a prior one).
    const kexAttempts = new Map();
    let kexExID = '';            // most-recent ex_id, for debug logging only
    // Guard against a single sendKexInit call re-entering at its first await.
    // (Distinct auth_ok / connected triggers intentionally each add their own
    // attempt to kexAttempts — that's the resilience; this flag only stops one
    // trigger from double-firing.) Set synchronously at the top of sendKexInit.
    let kexInFlight = false;
    let pakeTimeouts = 0;       // consecutive unanswered handshakes on this page
    let pakeBusyTries = 0;      // pake_busy answers since the last completed handshake


    // u8ToBase64 / base64ToU8 mirror the encrypt/decrypt helpers'
    // chunking so we don't blow the call stack on large keys (here
    // the inputs are tiny — 32-48 bytes — but keeping one idiom).
    function u8ToBase64(u) {
      let binary = '';
      const stride = 0x1000;
      for (let i = 0; i < u.length; i += stride) {
        binary += String.fromCharCode.apply(null, u.subarray(i, i + stride));
      }
      return btoa(binary);
    }
    function base64ToU8(b64) {
      return Uint8Array.from(atob(b64), c => c.charCodeAt(0));
    }
    function hexToU8(h) {
      const u = new Uint8Array(h.length / 2);
      for (let i = 0; i < u.length; i++) {
        u[i] = parseInt(h.slice(i * 2, i * 2 + 2), 16);
      }
      return u;
    }

    // KEX_TIMEOUT_MS bounds how long we wait for the agent's pake_resp
    // before assuming the message got dropped (relay blip, agent stall)
    // and retrying. Without this the viewer would hang forever on a
    // black screen with no error if the response went missing.
    const KEX_TIMEOUT_MS = 10000;
    // X25519 with a pure-JS fallback. crypto.subtle only gained X25519 on
    // Safari/iOS 18.4 (June 2025) and other browsers recently; on anything older
    // it throws NotSupportedError and the handshake can't run. When native X25519
    // is missing we do the key agreement with tweetnacl (nacl.scalarMult IS
    // X25519), loaded lazily and integrity-pinned (SRI) so 18.4+ browsers never
    // fetch it and the fallback can't be tampered with. HKDF + AES-GCM keep using
    // WebCrypto everywhere (supported far back).
    const NACL_URL = 'https://cdn.jsdelivr.net/npm/tweetnacl@1.0.3/nacl.min.js';
    const NACL_SRI = 'sha384-LMUiUHpaYNGZFzWFRjsADnCSqae1Mk5llcUOHOLDhCxkyF2cdsWAueTZAzV+swW/';
    let _naclPromise = null;
    // ---- CPace (PIN handshake) ------------------------------------------------
    // Mirrors internal/crypto/cpace.go byte for byte: the PIN only chooses the
    // generator both sides do Diffie-Hellman over, and nothing derived from it
    // goes over the wire. ristretto255 comes from a pinned @noble/curves build
    // served from this origin with SRI (scripts/vendor-ristretto/build.sh).
    const RISTRETTO_URL = '/vendor/ristretto255.js';
    const RISTRETTO_SRI = 'sha384-WLPjvwtTUQIPQoBW7B5gFQWHWWlAgJCIm1zMvS7MOJAZpawKsB0KUTl020csajQu';
    const CPACE_DSI = 'CPaceRistretto255';
    const PAKE_CHANNEL = 'reminal-session-v3';
    // What a handshake that offers sealed frames binds in: the CPace sid's
    // last part, and a field of each owner transcript (crypto.FramesLabel).
    const FRAMES_LABEL = 'frames-v1';
    // Sealed frames are offered twice in a handshake: bound into what is
    // authenticated (FRAMES_LABEL) and unbound, as an agent from before the
    // binding answers. An answer to the unbound form cannot be told from one
    // a relay brought about by stripping the bound offer, so this browser
    // remembers the sessions ("s:") and machines ("m:") it has seen seal, and
    // refuses an unsealed answer from them. SEALED_FRAMES_REQUIRED refuses one
    // from any agent: the one line to set once agents from before sealed
    // frames are gone.
    const SEALED_FRAMES_REQUIRED = false;
    const SEALED_PEERS_KEY = 'reminal-sealed-peers-v1';
    function sealedPeers() {
      try { const m = JSON.parse(localStorage.getItem(SEALED_PEERS_KEY) || '{}'); return m && typeof m === 'object' ? m : {}; } catch (_) { return {}; }
    }
    function sealedSeen(peer) { return !!(peer && sealedPeers()[peer]); }
    // At most the newest 2000 kept: a cap on what every handshake reads, never
    // an expiry (one would reopen the downgrade).
    function noteSealed(peer) {
      if (!peer) return;
      try {
        let m = sealedPeers();
        m[peer] = Math.floor(Date.now() / 1000);
        const ks = Object.keys(m);
        if (ks.length > 2000) {
          ks.sort((a, b) => m[b] - m[a] || (a < b ? -1 : 1));
          const kept = {};
          for (const k of ks.slice(0, 2000)) kept[k] = m[k];
          kept[peer] = m[peer];
          m = kept;
        }
        localStorage.setItem(SEALED_PEERS_KEY, JSON.stringify(m));
      } catch (_) {}
    }
    let _ristrettoPromise = null;
    function loadRistretto() {
      if (_ristrettoPromise) return _ristrettoPromise;
      _ristrettoPromise = new Promise((resolve, reject) => {
        if (window.ReminalRistretto) return resolve(window.ReminalRistretto);
        const s = document.createElement('script');
        s.src = RISTRETTO_URL; s.integrity = RISTRETTO_SRI; s.crossOrigin = 'anonymous';
        s.onload = () => window.ReminalRistretto ? resolve(window.ReminalRistretto) : reject(new Error('handshake library did not load'));
        s.onerror = () => { _ristrettoPromise = null; reject(new Error('handshake library failed to load')); };
        document.head.appendChild(s);
      });
      return _ristrettoPromise;
    }
    const utf8 = (str) => new TextEncoder().encode(str);
    function concatBytes(...parts) {
      let n = 0; for (const p of parts) n += p.length;
      const out = new Uint8Array(n); let o = 0;
      for (const p of parts) { out.set(p, o); o += p.length; }
      return out;
    }
    // Each field length-prefixed with 8 big-endian bytes, as in Go's lv().
    function lvBytes(...fields) {
      const parts = [];
      for (const f of fields) {
        const len = new Uint8Array(8);
        new DataView(len.buffer).setBigUint64(0, BigInt(f.length), false);
        parts.push(len, f);
      }
      return concatBytes(...parts);
    }
    async function sha512(bytes) {
      return new Uint8Array(await crypto.subtle.digest('SHA-512', bytes));
    }
    function leToBigInt(b) {
      let x = 0n;
      for (let i = b.length - 1; i >= 0; i--) x = (x << 8n) | BigInt(b[i]);
      return x;
    }
    async function cpaceStart(secret, channel, sid, initiator) {
      if (!secret || !secret.length) throw new Error('handshake: no PIN');
      const R = await loadRistretto();
      const G = R.fromUniform(await sha512(lvBytes(utf8(CPACE_DSI), secret, channel, sid)));
      let y = 0n;
      while (y === 0n) y = leToBigInt(crypto.getRandomValues(new Uint8Array(64))) % R.order;
      const mine = R.encode(R.multiply(G, y));
      return { y, mine, channel, sid, initiator };
    }
    // Refuses an element that does not decode, or that is (or yields) the
    // identity — the same checks as the Go side.
    async function cpaceFinish(st, peer) {
      if (!st || st.y === null) throw new Error('handshake already finished');
      if (!(peer instanceof Uint8Array) || peer.length !== 32) throw new Error('handshake: bad peer element');
      const R = await loadRistretto();
      let P;
      try { P = R.decode(peer); } catch (_) { throw new Error('handshake: bad peer element'); }
      if (R.isIdentity(P)) throw new Error('handshake: bad peer element');
      const K = R.multiply(P, st.y);
      if (R.isIdentity(K)) throw new Error('handshake: bad peer element');
      st.y = null; // single use
      const ya = st.initiator ? st.mine : peer;
      const yb = st.initiator ? peer : st.mine;
      const isk = await sha512(lvBytes(utf8(CPACE_DSI + '_ISK'), st.channel, st.sid, R.encode(K), ya, yb));
      return isk.slice(0, 32);
    }

    function loadNacl() {
      if (_naclPromise) return _naclPromise;
      _naclPromise = new Promise((resolve, reject) => {
        if (window.nacl) return resolve(window.nacl);
        const s = document.createElement('script');
        s.src = NACL_URL; s.integrity = NACL_SRI; s.crossOrigin = 'anonymous';
        s.onload = () => window.nacl ? resolve(window.nacl) : reject(new Error('nacl load'));
        s.onerror = () => reject(new Error('failed to load crypto fallback'));
        document.head.appendChild(s);
      });
      return _naclPromise;
    }
    let _x25519Native = null;
    async function x25519NativeOK() {
      if (_x25519Native === null) {
        // Only a browser that does not know X25519 has none: one that failed
        // to make a key three times (curveKeyMade) has it, and its own keys
        // fall back where they are made.
        try { await curveKeyMade('X25519', true, ['deriveBits']); _x25519Native = true; }
        catch (e) { _x25519Native = !(e && e.name === 'NotSupportedError'); }
      }
      return _x25519Native;
    }
    // Ephemeral keypair → { pub: Uint8Array(32), deriveShared(peerPubBytes) -> Uint8Array(32) }.
    async function newEphemeralX25519() {
      if (await x25519NativeOK()) {
        // Not made by the browser after three tries (curveKeyMade): by nacl,
        // below — the peer cannot tell the two apart.
        try {
          const kp = await curveKeyMade('X25519', true, ['deriveBits']);
          const pub = new Uint8Array(await crypto.subtle.exportKey('raw', kp.publicKey));
          return { pub, deriveShared: async (peer) => {
            const pk = await crypto.subtle.importKey('raw', peer, { name: 'X25519' }, false, []);
            return new Uint8Array(await crypto.subtle.deriveBits({ name: 'X25519', public: pk }, kp.privateKey, 256));
          } };
        } catch (_) {}
      }
      const nacl = await loadNacl();
      const priv = crypto.getRandomValues(new Uint8Array(32));
      const pub = nacl.scalarMult.base(priv); // nacl clamps the scalar internally
      return { pub, deriveShared: async (peer) => nacl.scalarMult(priv, new Uint8Array(peer)) };
    }

    let kexTimer = 0;
    // The session id the PIN box's contents were entered for. A PIN is not a
    // property of this tab, it is a property of one session: switching while
    // it sat in the box sent the previous session's PIN to the new one, which
    // fails the handshake — and, because a non-empty PIN takes the PIN path,
    // it also skipped the PIN-free owner connect that would have worked.
    let pinForSession = '';
    // The socket a handshake has been started for. Compared by identity, so a
    // new socket is automatically unclaimed and nothing needs resetting.
    let handshakeSock = null;
    // Did a person type what is in the PIN box? A typed PIN is meant for
    // whatever they are connecting to, so it is never stale; only one put
    // there on their behalf can belong to a session they have left.
    let pinTyped = false;
    async function sendKexInit() {
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      if (cryptoKey || kexInFlight) return;
      // The socket this handshake is for. Deriving the keys below takes a
      // moment, and a second connect in that moment — a tap that lands twice,
      // a switch of session — replaces `ws` with a socket still connecting.
      // Sending on that one throws and marked the session fatal; the new
      // socket runs its own handshake, so this one just stops.
      const sock = ws;
      kexInFlight = true;
      try {
      // A fresh CPace scalar per attempt; cpaceFinish clears it once used,
      // which is what keeps each connection's key independent of the next.
      const exIDBytes = crypto.getRandomValues(new Uint8Array(16));
      const exID = Array.from(exIDBytes)
        .map(b => b.toString(16).padStart(2, '0')).join('');
      // Two elements, one handshake: the sid as before (an agent from before
      // the sealed-frames binding answers it) and the sid naming sealed
      // frames (one that knows it answers that, sealed, the answer keyed in).
      const sid = concatBytes(utf8(String(sessionId).toUpperCase() + '|'), exIDBytes);
      const cp = await cpaceStart(utf8(pinValue), utf8(PAKE_CHANNEL), sid, true);
      const cpF = await cpaceStart(utf8(pinValue), utf8(PAKE_CHANNEL), concatBytes(utf8(FRAMES_LABEL + '|' + String(sessionId).toUpperCase() + '|'), exIDBytes), true);
      // Keep this attempt alongside any earlier one — completeKex accepts a
      // reply to whichever is answered first.
      if (sock !== ws || sock.readyState !== WebSocket.OPEN) return; // superseded while deriving
      kexAttempts.set(exID, { cp, cpF, exIDBytes });
      kexExID = exID;

      sock.send(JSON.stringify({
        type: 'pake_init',
        ex_id: exID,
        data: u8ToBase64(cp.mine),
        frames_data: u8ToBase64(cpF.mine),
        frames: 1,
      }));
      sendEarlyResume(sock, exID);
      dbg('▶ pake_init sent ex=' + exID.slice(0, 6) + ' agentLive=' + agentLive + ' (outstanding=' + kexAttempts.size + ')');
      // Arm a watchdog: if completeKex doesn't clear it within
      // KEX_TIMEOUT_MS, all outstanding replies are presumed lost.
      // Clear state and retry on a fresh ws if still connected, otherwise
      // surface an error so the user isn't stuck staring at a blank terminal.
      if (kexTimer) clearTimeout(kexTimer);
      kexTimer = setTimeout(() => {
        kexTimer = 0;
        if (cryptoKey) return; // raced with a late response
        dbg('✗ KEX TIMEOUT after ' + KEX_TIMEOUT_MS + 'ms (no matching pake_resp)');
        if (staleScreen) forgetCachedScreen(sessionId);
        kexAttempts.clear();
        kexExID = '';
        if (ws && ws.readyState === WebSocket.OPEN) {
          // One automatic retry. If it fails again the user will see
          // the second timeout fire and the status banner.
          // A machine on a reminal from before this handshake never answers
          // it, and retrying will not change that — so say what would.
          setStatus(pakeTimeouts++ ? 'No answer — if this machine’s reminal is older than 3.15.6, update it. Retrying…' : 'Handshake timed out — retrying…', 'warn connecting');
          sendKexInit().catch(() => {});
        } else {
          setStatus('No answer from this machine — if its reminal is older than 3.15.6, update it', 'error');
        }
      }, KEX_TIMEOUT_MS);
      } finally {
        kexInFlight = false;
      }
    }

    async function completeKex(msg) {
      const att = kexAttempts.get(msg.ex_id);
      const kexSock = ws;
      if (!att || cryptoKey) {
        // Not one of our outstanding attempts (the relay fans agent replies out
        // to every viewer), or we're already done — ignore.
        dbg('  pake_resp IGNORED (' + (cryptoKey ? 'already complete' : 'ex ' + String(msg.ex_id).slice(0, 6) + ' not outstanding') + ')');
        return;
      }
      dbg('  pake_resp matched — deriving key…');
      const wrapped = base64ToU8(msg.wrap || '');
      if (wrapped.length < 12 + 16) throw new Error('wrap too short');
      const wrapNonce = wrapped.slice(0, 12);
      const wrapCT = wrapped.slice(12);
      // The bound element answered, or the one as before: whichever key
      // unwraps the session key is the one the agent used. An AES-GCM tag
      // failure under both means the agent and we derived different keys from
      // CPace: a wrong PIN on one side or the other.
      let shared = null, sessionKeyRaw = null, bound = false, lastErr = null;
      for (const [cp, isBound] of [[att.cpF, true], [att.cp, false]]) {
        if (!cp) continue;
        // Throws on an element that does not decode or is the identity.
        const sh = await cpaceFinish(cp, base64ToU8(msg.data || ''));
        const sharedKey = await crypto.subtle.importKey('raw', sh, 'HKDF', false, ['deriveBits']);
        const wrapKeyBits = await crypto.subtle.deriveBits(
          { name: 'HKDF', hash: 'SHA-256', salt: att.exIDBytes, info: new TextEncoder().encode('reminal-wrap-v2') },
          sharedKey, 256);
        const wrapKey = await crypto.subtle.importKey('raw', wrapKeyBits, { name: 'AES-GCM' }, false, ['decrypt']);
        try {
          sessionKeyRaw = new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: wrapNonce }, wrapKey, wrapCT));
          shared = sh; bound = isBound;
          break;
        } catch (e) { lastErr = e; }
      }
      if (!sessionKeyRaw) throw lastErr || new Error('handshake did not open');
      const st = await admitSeal(shared, att.exIDBytes, msg.seal, sessionKeyRaw, bound, 's:' + String(sessionId).toUpperCase());
      const key = await crypto.subtle.importKey(
        'raw', sessionKeyRaw, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt']
      );
      // Install the key only for the connection and attempt it was made for.
      if (ws !== kexSock || cryptoKey || kexAttempts.get(msg.ex_id) !== att) return;
      sealState = st;
      cryptoKey = key;
      dbg('✓ handshake complete — session key set' + (sealState ? ', sealed frames' : ''));
      // In by PIN: not an owner here, even of a machine this device once owned
      // (ownership can be revoked on the host). Owner-only controls go.
      keyByPin = true;
      if (iOwnThisMachine) { iOwnThisMachine = false; if (typeof renderHost === 'function') renderHost(); }
      pakeTimeouts = 0;
      pakeBusyTries = 0;
      // Whatever a wait on the way said ("Busy — trying again in 3s"), the
      // session is up now: say so.
      if (agentLive) setStatus(sessionId, 'connected');
      reportSizeNow();
      // Burn ALL ephemeral material (every outstanding attempt) — nothing
      // downstream needs it, and holding on makes a long-running tab a juicier
      // target.
      kexAttempts.clear();
      kexExID = '';
      // Handshake done; cancel the watchdog so it doesn't retry.
      if (kexTimer) { clearTimeout(kexTimer); kexTimer = 0; }
    }

    // ---- PIN-free (owner) handshake — the browser mirror of the Go path ------
    // If this browser has an owner key (minted via "Own this machine"), we try a
    // mutually-authenticated connect first: prove our owner key, verify the
    // machine's identity against the key we pinned on first connect, and receive
    // the session key — no PIN. Falls back to the PIN handshake if the machine
    // doesn't recognise us. Must byte-match internal/crypto/owner.go.
    const OWNER_TIMEOUT_MS = 4000;
    // A machine under load can miss the window above without having refused
    // us — one just woken, on a small share of a CPU, restoring its sessions,
    // answers in tens of seconds, not four. Silence is asked again, waiting
    // longer each time (OWNER_SILENT_WAITS), and said as waiting; only once
    // they are all spent is it taken for a refusal. With a PIN to fall back
    // on, one more try is all silence gets: the PIN works either way. And a
    // machine that says own_busy has told us to come back — that is not a
    // refusal at all, so those retries are counted separately.
    const OWNER_SILENT_RETRIES = 1;
    const OWNER_SILENT_WAITS = [1200, 3000, 6000, 12000];
    const OWNER_BUSY_RETRIES = 4;
    const OWNER_RETRY_MS = 1200;
    // With the PIN in hand, an owner answer is waited for this long (or two
    // round trips, on a slow link) before the PIN handshake goes too; the
    // first of the two to answer is the connection. A machine that owns this
    // browser answers within a round trip, so an owner still connects as
    // one and spends none of the machine's PIN allowance; one that does not
    // stays silent (a refusal says nothing), and waiting out that silence
    // took nine seconds on every open.
    const OWNER_RACE_MS = 300;
    let ownerTimer = 0;
    let ownerSilentTries = 0;
    let ownerBusyTries = 0;
    const ownerAttempts = new Map();

    // retryOwnerInit re-runs the handshake once the current attempt is over.
    function retryOwnerInit(waitMs) {
      if (ownerTimer) { clearTimeout(ownerTimer); ownerTimer = 0; }
      ownerAttempts.clear();
      setTimeout(() => { if (!cryptoKey) sendOwnerInit().catch(() => {}); }, waitMs);
    }

    // ownerGaveUp is the end of the owner path: fall back to a PIN if we have
    // one, and otherwise say what we actually know. Silence from the machine
    // means EITHER that this device isn't enrolled OR that nothing answered,
    // and telling someone they don't own a machine they do own — which is what
    // the old wording did on every slow handshake — sends them to re-enrol a
    // device that was fine.
    function ownerGaveUp(reason) {
      if (cryptoKey) return;
      ownerAttempts.clear();
      dbg('owner handshake gave up: ' + reason);
      if (pinValue) { sendKexInit().catch(() => {}); return; }
      forgetCachedScreen(sessionId); // no way in without a PIN
      setStatus(reason === 'busy'
        ? 'This machine is too busy to confirm this device right now — retry, or enter the PIN'
        : 'Couldn’t connect as an owner — enter the PIN, or add this device as an owner', 'error');
    }

    async function loadDeviceKeyPair() {
      try {
        const db = await openOwnDB();
        const rec = await idbGet(db, 'device');
        if (rec && rec.seed && rec.pubRaw) {
          // Made from a seed, where the browser has no Ed25519 (ownDeviceId):
          // opened from its seal, and the same key it was, or none.
          const seed = new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: rec.seed.iv }, rec.seed.seal, rec.seed.sealed));
          const k = await ed25519FromSeed(seed);
          seed.fill(0);
          const pubRaw = new Uint8Array(rec.pubRaw);
          if (u8ToBase64(new Uint8Array(k.pub)) !== u8ToBase64(pubRaw)) return null;
          return { pubRaw, sign: k.sign };
        }
        if (!rec || !rec.privateKey || !rec.publicKey) return null;
        const pubRaw = new Uint8Array(await crypto.subtle.exportKey('raw', rec.publicKey));
        return { privateKey: rec.privateKey, pubRaw };
      } catch (_) { return null; }
    }
    // SHA-256 over length-prefixed [tag, sessionID, ...fields] — matches Go's
    // ownerHash (4-byte big-endian length before each field).
    async function ownerTranscript(tag, sid, fields) {
      const enc = new TextEncoder();
      const parts = [enc.encode(tag), enc.encode(sid), ...fields];
      let total = 0; for (const p of parts) total += 4 + p.length;
      const buf = new Uint8Array(total); let o = 0;
      for (const p of parts) {
        buf[o++] = (p.length >>> 24) & 0xff; buf[o++] = (p.length >>> 16) & 0xff;
        buf[o++] = (p.length >>> 8) & 0xff; buf[o++] = p.length & 0xff;
        buf.set(p, o); o += p.length;
      }
      return new Uint8Array(await crypto.subtle.digest('SHA-256', buf));
    }
    async function getPinnedMachine(sid) {
      // undefined → null for a genuine not-found; a real read error PROPAGATES so
      // completeOwner fails closed (we can't verify the machine identity) rather
      // than silently skipping the trust-on-first-use check — matches the Go side.
      const db = await openOwnDB();
      return (await idbGet(db, 'mach:' + sid)) || null;
    }
    async function pinMachine(sid, pubB64) {
      noteSessionHome(sid, pubB64);
      try { const db = await openOwnDB(); await idbPut(db, 'mach:' + sid, pubB64); } catch (_) {}
    }
    // Which machine each session is on, by the machine's key, as far as this
    // page has learned it: from a verified owner connect, or from a machine's
    // own directory listing its sessions. A switch between two sessions known
    // to be on the same machine keeps what the page knows of that machine (its
    // windows, its apps); anything else, unknown included, starts it afresh.
    const sessionHomes = new Map();
    function noteSessionHome(sid, machineB64) {
      if (sid && machineB64) sessionHomes.set(String(sid).toUpperCase(), b64URLToStd(machineB64));
    }
    // A machine's directory naming its sessions only fills in what is not
    // known: a session's verified machine (an owner connect's pin) is never
    // moved by another machine's say-so, which could list any id at all.
    function noteDirectorySessions(machineB64, sessions) {
      for (const x of sessions || []) {
        if (x && x.id && !sessionHomes.has(String(x.id).toUpperCase())) noteSessionHome(x.id, machineB64);
      }
    }
    function onSameMachine(a, b) {
      const x = a && sessionHomes.get(String(a).toUpperCase());
      const y = b && sessionHomes.get(String(b).toUpperCase());
      return !!x && x === y;
    }
    (async () => {
      try {
        const db = await openOwnDB();
        const keys = await new Promise((res, rej) => {
          const t = db.transaction('keys', 'readonly').objectStore('keys').getAllKeys();
          t.onsuccess = () => res(t.result || []); t.onerror = () => rej(t.error);
        });
        for (const k of keys) {
          if (typeof k === 'string' && k.startsWith('mach:')) noteSessionHome(k.slice(5), (await idbGet(db, k)) || '');
        }
      } catch (_) {}
    })();
    // Trust is held per machine, not per session. A machine this browser has
    // connected to as an owner is recognised by its key on any of its sessions.
    // A key it has not seen is accepted only when the session is not on one of
    // its machines, and the person confirms it. Mirrors machinetrust.go.
    const sameKey = (a, b) => b64URLToStd(a) === b64URLToStd(b);
    async function checkMachineIdentity(sid, machineB64) {
      const pinned = await getPinnedMachine(sid);
      if (pinned) {
        if (pinned !== machineB64) throw new Error('this machine’s identity changed since first connect — refusing');
        noteSessionHome(sid, machineB64);
        return;
      }
      const owned = await loadOwnedMachines();
      const keys = Object.keys(owned);
      if (keys.some(k => sameKey(k, machineB64))) { await pinMachine(sid, machineB64); return; }
      // A key this browser pinned for another session, before trust was held
      // per machine, is a machine it already trusts.
      if (await pinnedForAnySession(machineB64)) {
        await pinMachine(sid, machineB64);
        try { await recordOwnedMachineWeb(machineB64); } catch (_) {}
        return;
      }
      // The answer is in hand; the lookup and the question below can outlast
      // the handshake's own timers, which would otherwise retry over them.
      if (ownerTimer) { clearTimeout(ownerTimer); ownerTimer = 0; }
      if (kexTimer) { clearTimeout(kexTimer); kexTimer = 0; }
      const home = await findSessionHome(sid, keys);
      if (home) {
        const name = (owned[home] && owned[home].name) || 'one of your machines';
        throw new Error('session ' + sid + ' is on ' + name + ', but a different machine answered — not connecting');
      }
      const id = 'mach_' + machineB64.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      if (!confirm('This browser has not connected to this machine before.\n\nMachine: ' + id + '\n\nConnect, and remember it?')) {
        throw new Error('not connected — this browser has not connected to that machine before, and it was not confirmed');
      }
      await pinMachine(sid, machineB64);
    }
    async function pinnedForAnySession(machineB64) {
      try {
        const db = await openOwnDB();
        const keys = await new Promise((res, rej) => {
          const t = db.transaction('keys', 'readonly').objectStore('keys').getAllKeys();
          t.onsuccess = () => res(t.result || []); t.onerror = () => rej(t.error);
        });
        for (const k of keys) {
          if (typeof k === 'string' && k.startsWith('mach:') && sameKey((await idbGet(db, k)) || '', machineB64)) return true;
        }
      } catch (_) {}
      return false;
    }
    // Which of this browser's machines hosts sid, asked over their directory
    // channels.
    async function findSessionHome(sid, keys) {
      const hits = await Promise.all(keys.map(async (k) => {
        try {
          const r = await queryDirectory(base64ToU8(b64URLToStd(k)), { timeout: 5000 });
          return (r && Array.isArray(r.sessions) && r.sessions.some(x => x && x.id === sid)) ? k : null;
        } catch (_) { return null; }
      }));
      return hits.find(Boolean) || null;
    }

    // Have we ever completed an owner connect to this machine? (drives the
    // Machines list's owned-only "+".)
    async function isOwnedMachine(sid) { try { return (await getPinnedMachine(sid)) !== null; } catch (_) { return false; } }
    // The PIN handshake alongside an owner one still unanswered, on the
    // socket both are for; once is enough.
    function racePinHandshake(sock) {
      if (cryptoKey || !pinValue || sock !== ws || handshakeSock !== sock || kexAttempts.size) return;
      // The owner answer is in and its key is being made: an owner, so no
      // PIN spent on it.
      if (keyComing && keyComing.sock === sock) return;
      dbg('no owner answer yet — PIN handshake alongside');
      sendKexInit().catch(() => {});
    }

    async function sendOwnerInit() {
      if (!ws || ws.readyState !== WebSocket.OPEN) return false;
      if (cryptoKey || kexInFlight) return false;
      const sock = ws; // see sendKexInit: the handshake belongs to this socket
      const dk = await loadDeviceKeyPair();
      if (!dk) return false; // no owner key → can't try PIN-free
      kexInFlight = true;
      try {
        const eph = await newEphemeralX25519();
        const viewerEph = eph.pub;
        const exIDBytes = crypto.getRandomValues(new Uint8Array(16));
        const exID = Array.from(exIDBytes).map(b => b.toString(16).padStart(2, '0')).join('');
        const sigs = await ownerInitSigs(dk, sessionId, viewerEph);
        ownerAttempts.set(exID, { eph, exIDBytes, viewerEph, devicePubRaw: dk.pubRaw });
        dbg('▶ own_init sent ex=' + exID.slice(0, 6));
        if (sock !== ws || sock.readyState !== WebSocket.OPEN) return true; // superseded: the new socket handshakes itself
        sock.send(JSON.stringify({
          type: 'own_init', ex_id: exID,
          data: u8ToBase64(viewerEph),
          device_pub: u8ToBase64(dk.pubRaw),
          device_sig: sigs.device_sig,
          frames_sig: sigs.frames_sig,
          frames: 1,
        }));
        sendEarlyResume(sock, exID);
        if (pinValue) setTimeout(() => racePinHandshake(sock), Math.max(OWNER_RACE_MS, 2 * (observedRTT || 0) + 100));
        if (ownerTimer) clearTimeout(ownerTimer);
        ownerTimer = setTimeout(() => {
          ownerTimer = 0;
          if (cryptoKey) return;
          // No agent to answer: silence says nothing. Its coming back
          // (agent_online) starts the handshake again, counted afresh.
          if (!agentLive) { ownerAttempts.clear(); return; }
          // The PIN handshake is already under way (racePinHandshake), with a
          // watchdog of its own; an owner answer still coming may win yet.
          if (pinValue && (kexAttempts.size || kexInFlight)) return;
          if (pinValue ? ownerSilentTries < OWNER_SILENT_RETRIES : ownerSilentTries < OWNER_SILENT_WAITS.length) {
            const wait = pinValue ? OWNER_RETRY_MS : OWNER_SILENT_WAITS[ownerSilentTries];
            ownerSilentTries++;
            dbg('owner handshake timed out — retry ' + ownerSilentTries + ' in ' + wait + 'ms');
            // Asked again, as a machine just woken may need, but said now:
            // silence is as likely a machine that does not own this browser,
            // and the PIN gets in either way.
            if (!pinValue) setStatus('No answer to this device as an owner yet — still asking. Enter the PIN to connect now.', 'warn connecting');
            retryOwnerInit(wait);
            return;
          }
          ownerGaveUp('timeout');
        }, OWNER_TIMEOUT_MS);
        return true;
      } finally { kexInFlight = false; }
    }

    // One owner answer per connection is checked at a time: a retried
    // handshake can bring a second answer while the first is still being
    // checked or confirmed. A new connection is not held up by the old one's.
    let ownerCheckingSock = null;
    async function completeOwner(msg) {
      if (ownerCheckingSock === ws) return;
      const sock = ws;
      ownerCheckingSock = sock;
      try { await completeOwnerChecked(msg); } finally { if (ownerCheckingSock === sock) ownerCheckingSock = null; }
    }
    async function completeOwnerChecked(msg) {
      const att = ownerAttempts.get(msg.ex_id);
      if (!att || cryptoKey) return;
      const sock = ws, sid = sessionId;
      const agentEph = base64ToU8(msg.data || '');
      const machinePub = base64ToU8(msg.machine_pub || '');
      const machineSig = base64ToU8(msg.machine_sig || '');
      if (agentEph.length !== 32 || machinePub.length !== 32) throw new Error('bad key length');
      // 1. Verify the machine's signature over both ephemerals + both identities.
      //    Signed with the answer to the sealed-frames offer (bound), or over
      //    the transcript as before by an agent that predates it.
      const bound = await ownerAnswerBound(sessionId, att.viewerEph, agentEph, att.devicePubRaw, machinePub, machineSig, msg.seal);
      if (bound === null) {
        throw new Error('machine signature invalid — refusing (possible relay tampering)');
      }
      // 2. The machine must be one this browser trusts: the one it met on this
      //    session before, or any machine it has connected to as an owner.
      const machineB64 = u8ToBase64(machinePub);
      await checkMachineIdentity(sessionId, machineB64);
      // The check can take a while (a lookup, a question). Carry on only if
      // this is still the connection and the attempt it was for.
      if (ws !== sock || sessionId !== sid || cryptoKey || ownerAttempts.get(msg.ex_id) !== att) return;
      // 3. Same wrapped-session-key path as the PIN handshake.
      const shared = await att.eph.deriveShared(agentEph);
      const sharedKey = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveBits']);
      const wrapKeyBits = await crypto.subtle.deriveBits(
        { name: 'HKDF', hash: 'SHA-256', salt: att.exIDBytes, info: new TextEncoder().encode('reminal-wrap-v2') },
        sharedKey, 256);
      const wrapKey = await crypto.subtle.importKey('raw', wrapKeyBits, { name: 'AES-GCM' }, false, ['decrypt']);
      const wrapped = base64ToU8(msg.wrap || '');
      if (wrapped.length < 12 + 16) throw new Error('wrap too short');
      const sessionKeyRaw = new Uint8Array(await crypto.subtle.decrypt(
        { name: 'AES-GCM', iv: wrapped.slice(0, 12) }, wrapKey, wrapped.slice(12)));
      const st = await admitSeal(shared, att.exIDBytes, msg.seal, sessionKeyRaw, bound, 'm:' + machineB64);
      const key = await crypto.subtle.importKey('raw', sessionKeyRaw, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt']);
      // Install the key only for the connection and attempt it was made for.
      if (ws !== sock || sessionId !== sid || cryptoKey || ownerAttempts.get(msg.ex_id) !== att) return;
      sealState = st;
      cryptoKey = key;
      dbg('✓ owner handshake complete — PIN-free' + (sealState ? ', sealed frames' : ''));
      // As the PIN path: a "Machine is busy — retrying…" on the way is over.
      if (agentLive) setStatus(sessionId, 'connected');
      reportSizeNow();
      ownerAttempts.clear();
      if (ownerTimer) { clearTimeout(ownerTimer); ownerTimer = 0; }
      // We just proved ownership of this machine — remember it (keyed by machine
      // key, not session) so the Machines panel can list it and reach it later.
      try { await recordOwnedMachineWeb(machineB64); } catch (_) {}
      // And record the SESSION in Recents: auth_ok — where PIN connects are
      // remembered — never fires on this path, so without this an owner
      // connect leaves no trace in the history the dropdown and the session
      // dock rank by.
      rememberSession(sessionId, '');
      // We're connected PIN-free → we own this machine; hide the enroll offer.
      iOwnThisMachine = true;
      if (typeof renderHost === 'function') renderHost();
    }

    // startHandshake tries the owner path first (if this browser has a key),
    // else the PIN path.
    async function startHandshake() {
      if (cryptoKey) return;
      // Claim this socket BEFORE the first await. auth_ok and connected arrive
      // in the same tick, and the attempt only lands in kexAttempts after a
      // keypair has been generated — so a check for "is one already running"
      // that looks at kexAttempts is still empty when the second caller runs,
      // and both send.
      handshakeSock = ws;
      ownerSilentTries = 0;
      ownerBusyTries = 0;
      const tried = await sendOwnerInit();
      if (!tried) await sendKexInit();
    }

    // ===== Machines panel: list every session across the machines you own =====
    //
    // Owned machines are kept in IndexedDB keyed by MACHINE key (base64, url-safe
    // no pad — matches Go's owned_machines.json), recorded the first time this
    // browser owner-connects. To list a machine's live sessions we reach its
    // owner-derived directory channel: derive the channel id from the machine
    // key, prove ownership with the same signed handshake used for a PIN-free
    // connect, then dir_query and decrypt the reply — the relay only ever sees
    // opaque frames. Mirrors internal/client/directoryquery.go.

    async function loadOwnedMachines() {
      try { const db = await openOwnDB(); return (await idbGet(db, 'owned-machines')) || {}; }
      catch (_) { return {}; }
    }
    async function saveOwnedMachines(obj) {
      try { const db = await openOwnDB(); await idbPut(db, 'owned-machines', obj); } catch (_) {}
    }
    // Serialize load→mutate→save so concurrent mutations (several fills auto-naming
    // at once, a forget racing a rename, etc.) can't clobber each other — the same
    // lost-update hazard the Go tombstone store has. `mutate(store)` edits in place;
    // return false to skip the save. Resolves after the save.
    let _omChain = Promise.resolve();
    function withOwnedMachines(mutate) {
      const run = _omChain.then(async () => {
        const store = await loadOwnedMachines();
        const skip = await mutate(store);
        if (skip !== false) await saveOwnedMachines(store);
      });
      _omChain = run.catch(() => {}); // keep the chain alive on error
      return run;
    }
    async function recordOwnedMachineWeb(machineB64) {
      await withOwnedMachines(m => {
        const now = Date.now();
        if (!m[machineB64]) m[machineB64] = { firstOwned: now };
        m[machineB64].lastSeen = now;
      });
      renderMachines();
    }
    async function renameOwnedMachineWeb(machineB64, name) {
      await withOwnedMachines(m => { if (!m[machineB64]) return false; m[machineB64].name = name; });
      renderMachines();
    }
    async function forgetOwnedMachineWeb(machineB64) {
      await withOwnedMachines(m => { delete m[machineB64]; });
      forgetCachedScreen(); // which of them were on it is not worth the risk of a wrong guess
      closeAllWarm();
      // And the sessions it was pinned for, or the next owner connect would
      // trust it again from those pins.
      try {
        const db = await openOwnDB();
        const keys = await new Promise((res, rej) => {
          const t = db.transaction('keys', 'readonly').objectStore('keys').getAllKeys();
          t.onsuccess = () => res(t.result || []); t.onerror = () => rej(t.error);
        });
        for (const k of keys) {
          if (typeof k === 'string' && k.startsWith('mach:') && sameKey((await idbGet(db, k)) || '', machineB64)) {
            await new Promise((res) => { const t = db.transaction('keys', 'readwrite').objectStore('keys').delete(k); t.onsuccess = t.onerror = () => res(); });
          }
        }
      } catch (_) {}
      renderMachines();
    }

    // Toggle a machine's session list collapsed/expanded (persisted). Flips the
    // DOM in place — no re-query — so it's instant.
    async function toggleMachineCollapse(b64) {
      let collapsed = false;
      await withOwnedMachines(m => {
        if (!m[b64]) return false;
        m[b64].collapsed = !m[b64].collapsed;
        collapsed = m[b64].collapsed;
      });
      const box = document.querySelector('.machine-box[data-key="' + cssEscape(b64) + '"]');
      if (box) {
        box.querySelector('.machine-sessions')?.classList.toggle('collapsed', collapsed);
        const c = box.querySelector('.machine-caret');
        if (c) c.innerHTML = mIcon(collapsed ? 'right' : 'down');
      }
    }

    // Crockford-ish alphabet + 26-char length, matching crypto.DeriveDirectoryID.
    const DIR_ALPHABET = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789';
    async function deriveDirectoryID(machinePubU8) {
      const tag = new TextEncoder().encode('reminal-directory-v1:');
      const buf = new Uint8Array(tag.length + machinePubU8.length);
      buf.set(tag); buf.set(machinePubU8, tag.length);
      const h = new Uint8Array(await crypto.subtle.digest('SHA-256', buf));
      let id = '';
      for (let i = 0; i < 26; i++) id += DIR_ALPHABET[h[i % h.length] % DIR_ALPHABET.length];
      return id;
    }

    // queryDirectory reaches a machine's directory channel and either lists its
    // sessions (default) or spawns a new one (opts.spawn). Resolves with the
    // decrypted DirResponse {hostname, sessions} or, when spawning, {id, pin}.
    // dirReqId is a directory request's id, which its reply carries back:
    // every owner on a machine's channel hears every reply, and a request
    // takes only the one carrying its own. 8 random bytes as hex, never
    // empty or short, as Math.random's base-36 tail could be: an empty id
    // took another owner's reply for its own.
    // The reply type each owner action is answered with.
    const DIR_ACK_TYPE = { spawn: 'new_session', rename: 'dir_rename', revoke: 'dir_revoke_self', kill: 'dir_kill', sleep: 'dir_sleep' };
    function dirReqId() {
      return Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) => b.toString(16).padStart(2, '0')).join('');
    }
    // A machine over its allowance for a request answers "busy" with how
    // long until it would take it (retry_ms): waited exactly, and asked
    // again — never taken for the machine being offline. Two tabs or two
    // browsers on one machine pace themselves this way, whatever each
    // thinks the other is doing.
    const DIR_BUSY_TRIES = 30; // an abort cap; each wait is the machine's own
    async function queryDirectory(machinePubU8, opts) {
      for (let i = 0; ; i++) {
        try { return await queryDirectoryOnce(machinePubU8, opts); }
        catch (e) {
          if (!(e && e.busyMs) || i + 1 >= DIR_BUSY_TRIES) throw e;
          await new Promise(r => setTimeout(r, e.busyMs));
        }
      }
    }
    // own_busy travels in the clear (the relay could write its retry_ms):
    // no wait longer than a minute, whatever it says.
    const DIR_BUSY_MAX_MS = 60000;
    const dirBusyMs = ms => Math.min(DIR_BUSY_MAX_MS, Math.max(1, Number(ms) || 1));
    const dirBusy = ms => Object.assign(new Error('the machine is busy'), { busyMs: dirBusyMs(ms) });
    async function queryDirectoryOnce(machinePubU8, opts) {
      opts = opts || {};
      const dk = await loadDeviceKeyPair();
      if (!dk) throw new Error('this device has no owner key');
      const machineB64Cmp = u8ToBase64(machinePubU8);
      const dirID = await deriveDirectoryID(machinePubU8);
      const url = relayURL(dirID);
      return await new Promise((resolve, reject) => {
        let settled = false, state = 'auth', att = null, box = null;
        const sock = new WebSocket(url);
        const done = (err, val) => {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          try { sock.close(); } catch (_) {}
          if (err) { reject(err); return; }
          if (val && Array.isArray(val.sessions)) noteDirectorySessions(machineB64Cmp, val.sessions);
          resolve(val);
        };
        // At the deadline, an older machine's reply that did not answer the
        // request (kept below) is all it will say: that is the answer.
        const timer = setTimeout(() => (att && att.maybe ? done(null, att.maybe) : done(new Error('timeout'))), opts.timeout || 6000);
        const ch = { seal: null, key: null };
        const sealer = sealSocket(sock, ch);
        sock.onerror = () => done(new Error('unreachable'));
        sock.onclose = () => done(new Error('unreachable'));
        sock.onopen = () => sock.send(JSON.stringify({ type: 'auth' }));
        sock.onmessage = async (ev) => {
          let msg; try { msg = JSON.parse(ev.data); } catch (_) { return; }
          try {
            msg = await admitDirMsg(ch, sealer, msg);
            if (!msg) return;
            if (msg.type === 'error') return done(new Error('unreachable'));
            if (state === 'auth') {
              if (msg.type !== 'auth_ok') return;
              const eph = await newEphemeralX25519();
              const exIDBytes = crypto.getRandomValues(new Uint8Array(16));
              const exID = Array.from(exIDBytes).map(b => b.toString(16).padStart(2, '0')).join('');
              const sigs = await ownerInitSigs(dk, dirID, eph.pub);
              att = { eph, exIDBytes, exID, viewerEph: eph.pub, devicePubRaw: dk.pubRaw };
              sock.send(JSON.stringify({ type: 'own_init', ex_id: exID, data: u8ToBase64(eph.pub), device_pub: u8ToBase64(dk.pubRaw), device_sig: sigs.device_sig, frames_sig: sigs.frames_sig, frames: 1 }));
              state = 'own';
              return;
            }
            if (state === 'own') {
              if (msg.type === 'own_busy' && msg.ex_id === att.exID) return done(dirBusy(msg.retry_ms));
              if (msg.type !== 'own_resp' || msg.ex_id !== att.exID) return;
              const agentEph = base64ToU8(msg.data || '');
              const machinePub = base64ToU8(msg.machine_pub || '');
              const machineSig = base64ToU8(msg.machine_sig || '');
              if (agentEph.length !== 32 || machinePub.length !== 32) return done(new Error('bad key length'));
              // Must be the machine we asked for, and its signature must verify.
              if (u8ToBase64(machinePub) !== machineB64Cmp) return done(new Error('unexpected machine identity'));
              const bound = await ownerAnswerBound(dirID, att.viewerEph, agentEph, att.devicePubRaw, machinePub, machineSig, msg.seal);
              if (bound === null) return done(new Error('machine signature invalid'));
              const shared = await att.eph.deriveShared(agentEph);
              const sharedKey = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveBits']);
              const wrapBits = await crypto.subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt: att.exIDBytes, info: new TextEncoder().encode('reminal-wrap-v2') }, sharedKey, 256);
              const wrapKey = await crypto.subtle.importKey('raw', wrapBits, { name: 'AES-GCM' }, false, ['decrypt']);
              const wrapped = base64ToU8(msg.wrap || '');
              if (wrapped.length < 28) return done(new Error('wrap too short'));
              const skRaw = new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: wrapped.slice(0, 12) }, wrapKey, wrapped.slice(12)));
              ch.seal = await admitSeal(shared, att.exIDBytes, msg.seal, skRaw, bound, 'm:' + u8ToBase64(machinePub));
              box = await crypto.subtle.importKey('raw', skRaw, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt']);
              ch.key = box;
              if (opts.spawn) {
                att.reqId = dirReqId();
                const enc = await encrypt(box, JSON.stringify({ name: opts.spawnName || '', cwd: opts.spawnCwd || '', req_id: att.reqId }));
                sock.send(JSON.stringify({ type: 'new_session', data: enc }));
                state = 'spawn';
              } else if (opts.rename) {
                att.reqId = dirReqId();
                const enc = await encrypt(box, JSON.stringify({ id: opts.rename.id, name: opts.rename.name, req_id: att.reqId }));
                sock.send(JSON.stringify({ type: 'dir_rename', data: enc }));
                state = 'rename';
              } else if (opts.revokeSelf) {
                att.reqId = dirReqId();
                // Prove this is a SELF-revoke: sign (machineKey, deviceKey) with our
                // device key. Matches Go's RevokeSelfTranscript (sessionID = "").
                const tr = await ownerTranscript('reminal-revoke-self-v1', '', [machinePubU8, dk.pubRaw]);
                const sig = await deviceSign(dk, tr);
                const enc = await encrypt(box, JSON.stringify({ device_pub: u8ToBase64(dk.pubRaw), sig: u8ToBase64(sig), req_id: att.reqId }));
                sock.send(JSON.stringify({ type: 'dir_revoke_self', data: enc }));
                state = 'revoke';
              } else if (opts.kill) {
                att.reqId = dirReqId();
                const enc = await encrypt(box, JSON.stringify({ id: opts.kill, req_id: att.reqId }));
                sock.send(JSON.stringify({ type: 'dir_kill', data: enc }));
                state = 'kill';
              } else if (opts.sleep) {
                // Sleep, wake or keep awake one of its sessions: { id, act }.
                att.reqId = dirReqId();
                const enc = await encrypt(box, JSON.stringify({ id: opts.sleep.id, act: opts.sleep.act, req_id: att.reqId }));
                sock.send(JSON.stringify({ type: 'dir_sleep', data: enc }));
                state = 'sleep';
              } else if (opts.push) {
                // Phone alerts ride dir_query too. The proof names which owner
                // device asked (every owner shares the channel key) and is bound
                // to the op and endpoint, matching pushAction on the host.
                const p = opts.push;
                const proof = await signOwnerAction(pushActionOf(p), dirID);
                if (!proof) return done(new Error('this device has no owner key'));
                att.reqId = dirReqId();
                const enc = await encrypt(box, JSON.stringify(pushRequestOf(p, proof, att.reqId)));
                sock.send(JSON.stringify({ type: 'dir_query', data: enc }));
                state = 'query';
              } else if (opts.open) {
                // An open lookup rides dir_query with a payload; the host answers
                // the same dir_resp with open_url/open_pin for that one port. Asked
                // per click, so the PIN never lands in the cached session list.
                att.reqId = dirReqId();
                const enc = await encrypt(box, JSON.stringify({ open_id: opts.open, req_id: att.reqId }));
                sock.send(JSON.stringify({ type: 'dir_query', data: enc }));
                state = 'query';
              } else {
                sock.send(JSON.stringify({ type: 'dir_query' }));
                state = 'query';
              }
              return;
            }
            if (state === 'query') {
              if (msg.type !== 'dir_resp') return;
              const pt = await decrypt(box, msg.data || '');
              const r = JSON.parse(new TextDecoder().decode(pt));
              // Over its allowance: this request's busy answer (its id), or —
              // for a plain listing, which carries none — any.
              if (r.retry_ms && (r.req_id ? r.req_id === att.reqId : !att.reqId)) return done(dirBusy(r.retry_ms));
              // Every viewer on the channel hears every answer. A push request
              // waits for the one carrying its own id. A machine too old to
              // know about alerts answers with a plain reply and nothing more
              // is coming — so a plain reply from such a machine IS the answer
              // (the caller reports "needs an update"), while a plain reply
              // from a new one is somebody else's and is skipped.
              if (opts.push) {
                if (!r.push) { if (alertHostTooOld(r)) return done(null, r); return; }
                if (r.push.req_id && r.push.req_id !== att.reqId) return;
              }
              // An open lookup takes the reply carrying its id; from a machine
              // that tags its replies, any other is another owner's. An older
              // one's untagged reply answers it if it holds the link (or why
              // not); one that does not is kept for the deadline.
              if (opts.open) {
                if (r.tagged || r.req_id) { if (r.req_id !== att.reqId) return; }
                else if (!r.open_url && !r.open_error) { att.maybe = r; return; }
              }
              return done(null, r);
            }
            // An owner action's answer: the reply of its own type, carrying its
            // req_id (another owner's is not ours). A refusal keeps the whole reply
            // (a sleep says what it would end).
            if (DIR_ACK_TYPE[state]) {
              if (msg.type !== DIR_ACK_TYPE[state]) return;
              const pt = await decrypt(box, msg.data || '');
              const r = JSON.parse(new TextDecoder().decode(pt));
              if (att.reqId && r.req_id && r.req_id !== att.reqId) return;
              if (r.retry_ms) return done(dirBusy(r.retry_ms));
              if (r.error) return done(Object.assign(new Error(r.error), { reply: r }));
              return done(null, r);
            }
          } catch (e) { done(e); }
        };
      });
    }

    // renderMachines paints the "Machines you own" section, then reaches each
    // machine's directory in parallel and fills in its live sessions as replies
    // land. A machine that doesn't answer is shown offline.
    // Bumped on every renderMachines() so an in-flight fill from a superseded
    // render bails instead of writing into the newly-rebuilt skeleton.
    let machinesGen = 0;
    let ownedSessionIds = new Set(); // session ids currently shown under owned-machine cards
    // Sessions killed from the panel this page-load. They're filtered out of every
    // render so a racing re-query, a cached snapshot, or a reconnect can't bring a
    // killed session back into the list. Session ids are one-time (never reused),
    // so keeping them filtered for the life of the page never hides a live session.
    const killedSessionIds = new Set();
    // Consecutive failed liveness probes per machine (keyed by owned-machine b64).
    // A reachable machine that's momentarily between relay connections (its host
    // just woke and is re-dialing, or a probe raced the reconnect) misses a probe
    // or two before it answers. We keep it in the amber "reconnecting…" state for
    // MACHINE_GRACE misses instead of flipping straight to a hard "offline", so a
    // machine that's actually up doesn't read as down (and its cached session list
    // — which would otherwise diverge from `reminal list` — isn't replaced with
    // "offline"). Reset to 0 on any successful probe.
    const machineFail = new Map();
    const MACHINE_GRACE = 3;
    // Consistent stroke icons for the Machines panel — the caret / + / edit /
    // kill controls all render at the same visual size (Unicode glyphs don't).
    const MICONS = {
      moon:  '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>',
      down:  '<path d="M6 9l6 6 6-6"/>',
      right: '<path d="M9 6l6 6-6 6"/>',
      plus:  '<path d="M12 5v14M5 12h14"/>',
      copy:  '<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
      edit:  '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19.5 3 20.5l1-4z"/>',
      x:     '<path d="M18 6 6 18M6 6l12 12"/>',
      bell:  '<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/>',
      belloff: '<path d="M8.7 3A6 6 0 0 1 18 8a21.3 21.3 0 0 0 .6 5"/><path d="M17 17H3s3-2 3-9a4.67 4.67 0 0 1 .3-1.7"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/><path d="m2 2 20 20"/>',
      cpu:   '<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9" y="9" width="6" height="6" rx="1"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/>',
      battery: '<rect x="2" y="7" width="17" height="10" rx="2"/><path d="M22 11v2"/><path d="M6 11v2"/>',
      plug:  '<path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8z"/>',
      send:  '<path d="m22 2-7 20-4-9-9-4z"/><path d="M22 2 11 13"/>',
      lock:  '<rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>',
      share: '<path d="M4 12v7a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-7"/><path d="m16 6-4-4-4 4"/><path d="M12 2v13"/>',
      info:  '<circle cx="12" cy="12" r="9"/><path d="M12 16v-4M12 8h.01"/>',
      check: '<path d="M20 6 9 17l-5-5"/>',
      left:  '<path d="M15 6l-6 6 6 6"/>',
      clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
    };
    function mIcon(name) {
      return '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' + (MICONS[name] || '') + '</svg>';
    }
    // ---- Machines panel search -----------------------------------------------
    // Pure client-side text filtering over what is already painted. The box has
    // to live OUTSIDE #machines-list, because the panel refreshes itself on a
    // timer and renderMachines rebuilds that root from scratch — a search input
    // inside it would lose focus and its value mid-type. So the filter is a pass
    // over the rendered DOM, re-applied after every render instead of being
    // baked into one.
    const machinesSearch = document.getElementById('machines-search');

    // Text of a row's named parts, joined by spaces. NOT row.textContent: that
    // concatenates adjacent nodes with no separator, so a label running into its
    // metadata invents substrings that are in neither ("web frontend" + its id
    // "BSJPFEVG" reads as "...frontendBSJPFEVG", which matches a search for
    // "db"). Joining the fields keeps every match a match the user can see.
    function rowHaystack(row, sels) {
      const parts = [];
      for (const sel of sels) {
        for (const el of row.querySelectorAll(sel)) parts.push(el.textContent || '');
      }
      return parts.join(' ').toLowerCase();
    }

    function applyMachinesFilter() {
      const listRoot = document.getElementById('machines-list');
      const recRoot = document.getElementById('recents-list');
      const noMatch = document.getElementById('machines-nomatch');
      const f = (machinesSearch ? machinesSearch.value : '').trim().toLowerCase();
      let hits = 0;
      if (listRoot) {
        for (const box of listRoot.querySelectorAll('.machine-box')) {
          const name = (box.querySelector('.machine-name') || {}).textContent || '';
          const nameHit = !f || name.toLowerCase().includes(f);
          // A machine matched by its own name keeps all of its sessions.
          // Otherwise only the sessions that match stay, so searching for one
          // session doesn't drag its siblings along with it.
          let sessionHits = 0;
          for (const row of box.querySelectorAll('.machine-session')) {
            const hit = nameHit || rowHaystack(row, ['.ms-label', '.ms-meta']).includes(f);
            row.classList.toggle('filter-hide', !hit);
            if (hit && !nameHit) sessionHits++;
          }
          const keep = nameHit || sessionHits > 0;
          box.classList.toggle('filter-hide', !keep);
          // Reveal a collapsed card's matches rather than reporting a hit the
          // user cannot see; plain browsing leaves the collapse alone.
          box.classList.toggle('filter-open', !!f && keep && sessionHits > 0);
          if (keep) hits++;
        }
      }
      if (recRoot) {
        let recHits = 0;
        for (const row of recRoot.querySelectorAll('.recents-row')) {
          const hit = !f || rowHaystack(row, ['.name', '.meta']).includes(f);
          row.classList.toggle('filter-hide', !hit);
          if (hit) recHits++;
        }
        // The "Recent sessions" heading is noise with everything under it gone.
        const label = recRoot.querySelector('.section-label');
        if (label) label.classList.toggle('filter-hide', !!f && recHits === 0);
        hits += recHits;
      }
      const anything = !!(listRoot && listRoot.querySelector('.machine-box')) ||
                       !!(recRoot && recRoot.querySelector('.recents-row'));
      // Hide the box itself until there is something to search, so a browser
      // with no machines and no history doesn't lead with an empty filter.
      if (machinesSearch) machinesSearch.classList.toggle('is-idle', !anything && !f);
      if (noMatch) noMatch.classList.toggle('filter-hide', !(f && hits === 0));
    }

    if (machinesSearch) {
      machinesSearch.addEventListener('input', applyMachinesFilter);
      // Machine cards, their session lists and Recents are each repainted by a
      // different function, several of them on background timers and with early
      // returns. Rather than hunt every one of those exits for a re-filter call
      // (and miss one the next time a path is added), watch the panel: any row
      // appearing or disappearing re-runs the filter. The filter itself only
      // toggles classes, never children, so it cannot retrigger this.
      // The panel, not the box's parent: the box shares a row with the alerts
      // bell, and that row never changes.
      const panel = machinesSearch.closest('.recents-list');
      if (panel && window.MutationObserver) {
        new MutationObserver(applyMachinesFilter)
          .observe(panel, { childList: true, subtree: true });
      }
    }

    async function renderMachines() {
      const root = document.getElementById('machines-list');
      if (!root) return;
      const store = await loadOwnedMachines();
      const keys = Object.keys(store).sort((a, b) => (store[b].lastSeen || 0) - (store[a].lastSeen || 0));
      if (keys.length === 0) {
        root.innerHTML = '<div class="recents-empty">This device isn\'t an owner of any other machines yet — owner-connect to one and it shows up here.</div>';
        return;
      }
      // Always paint the cached skeleton (machine names + a pulsing "checking"
      // dot) synchronously so opening the panel is NEVER blank — even while a
      // previous refresh's fills are still in flight against an offline machine's
      // timeout. Those older fills bail via the generation check below rather
      // than writing into this freshly-rebuilt skeleton.
      const gen = ++machinesGen;
      // Complete any queued self-revokes for machines that are now reachable.
      retryPendingRevokes();
      root.innerHTML = '';
      ownedSessionIds = new Set(); // repopulated by the cached + fresh session renders below
      for (const b64 of keys) {
        const rec = store[b64];
        const box = document.createElement('div');
        box.className = 'machine-box' + (rec.pendingRevoke ? ' is-pending' : '');
        box.dataset.key = b64;

        const head = document.createElement('div');
        head.className = 'machine-head';
        const nameEl = document.createElement('span');
        nameEl.className = 'machine-name';
        nameEl.textContent = machineLabel(rec, b64);
        const dot = document.createElement('span');
        dot.className = 'machine-dot spin';
        dot.title = 'refreshing…';
        if (rec.pendingRevoke) {
          // Laid out like every other row — caret's width, dot, name — with
          // the note where the stats would be, so the column lines up.
          const caret = document.createElement('span');
          caret.className = 'machine-caret';
          caret.setAttribute('aria-hidden', 'true');
          const title = document.createElement('span');
          title.className = 'machine-title';
          title.appendChild(nameEl);
          const pend = document.createElement('span');
          pend.className = 'machine-pending';
          pend.textContent = 'removing when online…';
          // A queued self-revoke can wait forever if the machine never comes back
          // (or was already revoked from a terminal). Let the user just drop it
          // from this browser now instead of being stuck.
          const forget = document.createElement('button');
          forget.className = 'machine-btn';
          forget.title = 'Stop waiting — forget this machine now';
          forget.innerHTML = mIcon('x');
          forget.addEventListener('click', (ev) => { ev.stopPropagation(); forgetPendingMachine(b64, nameEl.textContent); });
          head.appendChild(caret); head.appendChild(dot); head.appendChild(title); head.appendChild(pend); head.appendChild(forget);
          const sessions = document.createElement('div');
          sessions.className = 'machine-sessions';
          box.appendChild(head); box.appendChild(sessions);
          root.appendChild(box);
          continue; // no actions/fill for a machine we're removing
        }
        const addBtn = document.createElement('button');
        addBtn.className = 'machine-btn';
        addBtn.title = 'Start a new session on this machine (' + NEW_TAB_KEY + '-click to start it in a new tab)';
        addBtn.innerHTML = mIcon('plus');
        addBtn.addEventListener('click', (ev) => {
          ev.stopPropagation();
          // The tab is opened here, synchronously, or the popup blocker kills
          // it; a blocked open returns null and we simply spawn in place.
          spawnOnMachine(b64, nameEl.textContent, wantsNewTab(ev) ? window.open('', '_blank') : null);
        });
        const renameBtn = document.createElement('button');
        renameBtn.className = 'machine-btn';
        renameBtn.title = 'Rename this machine';
        renameBtn.innerHTML = mIcon('edit');
        renameBtn.addEventListener('click', (ev) => {
          ev.stopPropagation();
          const next = prompt('Name for this machine:', rec.name || '');
          if (next !== null) renameOwnedMachineWeb(b64, next.trim());
        });
        const forgetBtn = document.createElement('button');
        forgetBtn.className = 'machine-btn';
        forgetBtn.title = 'Remove this machine from your list';
        forgetBtn.innerHTML = mIcon('x');
        forgetBtn.addEventListener('click', (ev) => { ev.stopPropagation(); forgetMachine(b64, nameEl.textContent); });
        // Collapsible: a caret + a live session count; clicking the header (not a
        // button) toggles the session list so you're not always staring at every
        // instance. Collapsed state is remembered per machine.
        const caret = document.createElement('span');
        caret.className = 'machine-caret';
        caret.innerHTML = mIcon(rec.collapsed ? 'right' : 'down');
        const count = document.createElement('span');
        count.className = 'machine-count';
        head.style.cursor = 'pointer';
        head.title = 'Show/hide sessions';
        head.addEventListener('click', () => toggleMachineCollapse(b64));
        const title = document.createElement('div');
        title.className = 'machine-title';
        title.appendChild(nameEl);
        title.appendChild(count);
        head.appendChild(caret);
        head.appendChild(dot);
        head.appendChild(title);
        head.appendChild(addBtn);
        head.appendChild(renameBtn);
        head.appendChild(forgetBtn);

        const sessions = document.createElement('div');
        sessions.className = 'machine-sessions' + (rec.collapsed ? ' collapsed' : '');
        box.appendChild(head);
        box.appendChild(sessions);
        root.appendChild(box);
        // Instant cached view: if we remember this machine's sessions from a
        // previous refresh, paint them NOW (with the spinner still turning) so the
        // panel is populated the moment it opens; the background fill updates them
        // in place. First-ever open (no cache) shows a placeholder until it lands.
        if (Array.isArray(rec.sessions)) {
          renderMachineSessions(box, rec.sessions, b64);
        } else {
          sessions.innerHTML = '<div class="machine-sub">checking…</div>';
        }
        // Remembered battery, painted as history until this refresh's query
        // proves otherwise. A machine we have never seen a reading from paints
        // nothing, which is what a desktop should look like forever.
        const remembered = battRemembered(rec);
        if (remembered) paintBattery(box, remembered, true);
      }
      refreshAlertBadges();
      // Fire the fills in the background — each box updates itself as its query
      // resolves, so one offline machine never blocks the others (or the panel).
      // Machines pending removal are handled by retryPendingRevokes, not filled.
      await Promise.all(keys.filter(b64 => !store[b64].pendingRevoke).map(b64 => fillMachineSessions(b64, store[b64], gen)));
      if (gen !== machinesGen) return; // a newer render superseded us
      // Now that we know which sessions live under owned machines, re-render
      // Recents so it drops the duplicates (e.g. the current session shown in a
      // card no longer also appears in the Recent list below).
      renderRecents();
    }

    // Re-probe every owned machine's liveness in place, WITHOUT rebuilding the
    // skeleton — updates each existing card's dot + session list as its query
    // lands. Runs on a timer while the Machines panel is open so a machine that
    // comes back (or whose host finishes reconnecting after a sleep/wake) flips
    // to green on its own, instead of staying stuck on a stale gray dot until the
    // user closes and reopens the panel. fillMachineSessions is idempotent and
    // gen-guarded, so re-running it against live boxes just refreshes them.
    let machinesPolling = false;
    async function pollMachines() {
      if (document.hidden) return; // don't burn probes on a backgrounded tab
      // Re-entrancy guard: a directory query can take up to its ~6s timeout, which
      // is longer than the poll interval, so without this a slow/offline machine
      // would let successive ticks stack — each opening a fresh WebSocket handshake
      // to every machine. Skip a tick if the previous poll is still in flight.
      if (machinesPolling) return;
      machinesPolling = true;
      try {
        const store = await loadOwnedMachines();
        const gen = machinesGen; // don't bump — we're updating the current skeleton
        const keys = Object.keys(store).filter(b64 =>
          !store[b64].pendingRevoke &&
          document.querySelector('.machine-box[data-key="' + cssEscape(b64) + '"]'));
        await Promise.all(keys.map(b64 => fillMachineSessions(b64, store[b64], gen)));
        if (gen === machinesGen) renderRecents(); // reflect newly-revealed sessions
      } finally {
        machinesPolling = false;
      }
    }

    // ── battery ────────────────────────────────────────────────────────
    // A machine's power state, remembered in the same per-machine record that
    // already caches its sessions and hostname. The remembering is the point:
    // a laptop that has gone dark cannot tell you anything, so the last thing
    // it said — and when — is the whole answer.
    //
    // battFromResp returns null when the host reported no battery. Desktops,
    // VMs and older agents all land there, and null renders nothing at all:
    // absence is the signal, so there is no "unknown" state to draw.
    // battRemembered returns a stored reading only while it is still worth
    // asserting. Anything older reads as "we know nothing about this machine",
    // which is exactly what the row showed before we first heard from it.
    //
    // The TTL is local rather than a module const on purpose: this function is
    // called from renderMachines, which is declared ABOVE it. A function
    // declaration hoists and a `const` does not, so a module-level constant
    // here would sit in the temporal dead zone for any caller that ever runs
    // during script evaluation. None do today; this makes that impossible to
    // get wrong later. Mirrors batteryHistoryTTL in batteryhistory.go — past a
    // week, a laptop shut in a drawer is not "at 20%" in any useful sense, and
    // the same expiry stops a machine that is UP but running a pre-battery
    // agent from showing a stale charge beside a green online dot forever.
    function battRemembered(rec) {
      const TTL_MS = 7 * 24 * 60 * 60 * 1000;
      const b = rec && rec.batt;
      if (!b || typeof b.pct !== 'number' || !b.at) return null;
      if (Date.now() - b.at > TTL_MS) return null;
      return b;
    }

    function battFromResp(resp) {
      if (!resp || typeof resp.battery_pct !== 'number') return null;
      const pct = Math.max(0, Math.min(100, Math.round(resp.battery_pct)));
      return { pct, state: resp.battery_state || '', mins: resp.battery_mins || 0, at: Date.now() };
    }

    function battDur(mins) {
      if (!mins || mins <= 0) return '';
      if (mins < 60) return mins + 'm';
      const h = Math.floor(mins / 60), m = mins % 60;
      return m ? h + 'h ' + m + 'm' : h + 'h';
    }

    // battWhen renders the moment a stale reading was taken. A clock time for
    // today, a day name beyond that — "was 20% at 3:04pm" is useless if you
    // cannot tell which day it means.
    function battWhen(ts) {
      const d = new Date(ts), now = new Date();
      const time = d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
      if (now - d < 60000) return 'just now';
      const sameDay = d.toDateString() === now.toDateString();
      if (sameDay) return 'at ' + time;
      const y = new Date(now); y.setDate(y.getDate() - 1);
      if (d.toDateString() === y.toDateString()) return 'yesterday ' + time;
      return d.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' + time;
    }

    // syncMachineTight marks the headers whose hostname has been ellipsised.
    // The separator dot before the session count is shown only for those: with
    // room to spare the name sits far to the left and the dot floats alone in
    // the gap. Truncation is a measured fact, so it is recomputed whenever the
    // list changes or the panel is resized.
    function syncMachineTight(root) {
      const scope = root || document;
      scope.querySelectorAll('.machine-title').forEach(t => {
        const n = t.querySelector('.machine-name');
        if (!n) return;
        // 1px of slack: sub-pixel text metrics make scrollWidth exceed
        // clientWidth by a hair on names that are not actually clipped.
        t.classList.toggle('tight', n.scrollWidth > n.clientWidth + 1);
      });
    }
    let battTightRAF = 0;
    addEventListener('resize', () => {
      if (battTightRAF) return;
      battTightRAF = requestAnimationFrame(() => { battTightRAF = 0; syncMachineTight(); });
    });

    // An open battery bubble closes on the next tap anywhere else, the way
    // every other transient popover in here behaves. Registered once.
    let battCloserWired = false;
    function wireBattCloser() {
      if (battCloserWired) return;
      battCloserWired = true;
      document.addEventListener('click', (e) => {
        if (e.target && e.target.closest && e.target.closest('.machine-batt, .machine-stats, .stats-portal')) return;
        document.querySelectorAll('.machine-batt.open').forEach(o => o.classList.remove('open'));
        closeStatsPortal();
      });
    }

    // paintBattery writes the gauge into a machine header. stale=true renders
    // the remembered reading in past tense; a machine with no battery removes
    // the element entirely rather than leaving an empty one behind.
    //
    // The estimate is always labelled with what it counts down TO. While
    // charging the OS reports time until full, and calling that "left" would
    // read as time until dead — the exact opposite of the truth.
    // paintStats shows a machine's vitals from its directory reply. Live only:
    // a remembered CPU figure from an hour ago says nothing, so an offline
    // machine shows no vitals rather than stale ones (the battery is the
    // exception, and says when it was read).
    function paintStats(box, st) {
      if (!box) return;
      const head = box.querySelector('.machine-title');
      if (!head) return;
      let el = head.querySelector('.machine-stats');
      if (!st || (typeof st.cpu_pct !== 'number' && !st.mem_total)) {
        if (el) { if (statsPortalAnchor === el) closeStatsPortal(); el.remove(); }
        return;
      }
      if (!el) {
        el = document.createElement('span');
        el.className = 'machine-stats';
        el.innerHTML = '<span class="st-label"></span>';
        head.insertBefore(el, head.querySelector('.machine-count'));
        el.setAttribute('role', 'button');
        el.setAttribute('tabindex', '0');
        // Touch has no hover, so the full metrics open on a tap \u2014 like the
        // battery badge. stopPropagation so opening it doesn't also toggle the
        // machine's session list underneath. The bubble is a body-level portal
        // (openStatsPortal) so the panel's overflow can't clip it.
        const toggle = (ev) => {
          ev.stopPropagation();
          const wasOpen = el.classList.contains('open');
          document.querySelectorAll('.machine-batt.open').forEach(o => o.classList.remove('open'));
          closeStatsPortal();
          if (!wasOpen) { el.classList.add('open'); openStatsPortal(el); }
        };
        el.addEventListener('click', toggle);
        el.addEventListener('keydown', (ev) => {
          if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); toggle(ev); }
        });
        wireBattCloser();
      }
      // The row shows one number (CPU%, or memory if there's no CPU reading);
      // the full set lives in the tap-open portal and the hover title, built
      // from ONE list so they can't drift \u2014 and so a new metric (e.g. gpu_pct,
      // once the daemon reports it) shows up in both automatically.
      let cpu = '';
      if (typeof st.cpu_pct === 'number') {
        const pct = Math.max(0, Math.min(100, Math.round(st.cpu_pct)));
        const cls = pct >= 85 ? ' hot' : pct >= 50 ? ' busy' : '';
        cpu = '<span class="st-cpu' + cls + '">' + pct + '%</span>';
      }
      el.querySelector('.st-label').innerHTML = cpu || escHTML(fmtBytes(st.mem_used || 0));
      const rows = [];
      if (typeof st.cpu_pct === 'number') rows.push('CPU ' + Math.round(st.cpu_pct) + '% busy');
      if (typeof st.gpu_pct === 'number') rows.push('GPU ' + Math.round(st.gpu_pct) + '% busy');
      if (st.mem_total) rows.push('Memory ' + fmtBytes(st.mem_used || 0) + ' of ' + fmtBytes(st.mem_total));
      if (st.load1 !== undefined) {
        let load = Number(st.load1).toFixed(2);
        if (st.load5 !== undefined) load += ' \u00b7 ' + Number(st.load5).toFixed(2);
        if (st.load15 !== undefined) load += ' \u00b7 ' + Number(st.load15).toFixed(2);
        rows.push('Load ' + load);
      }
      if (st.cpus) rows.push(st.cpus + ' core' + (st.cpus === 1 ? '' : 's') + (st.cpu_model ? ' \u00b7 ' + st.cpu_model : ''));
      if (st.uptime) rows.push('Up ' + fmtUptime(st.uptime));
      el._statsRows = rows;
      el.title = rows.join(' \u00b7 ');
      if (statsPortalAnchor === el) openStatsPortal(el); // keep an open bubble live as the poll repaints
      syncMachineTight(box);
    }
    // ── stats detail portal ───────────────────────────────────────────────
    // A single body-level bubble showing a machine's full vitals on tap. It
    // lives at document.body, not inside the badge, so the machines panel's
    // overflow (overflow-y:auto / overflow-x:hidden) can't clip it; the position
    // is fixed and computed from the badge's rect, re-tracked as the panel
    // scrolls or the window resizes.
    let statsPortal = null, statsPortalAnchor = null, statsPortalWired = false;
    function openStatsPortal(anchor) {
      if (!statsPortal) {
        statsPortal = document.createElement('div');
        statsPortal.className = 'stats-portal';
        document.body.appendChild(statsPortal);
      }
      if (!statsPortalWired) {
        statsPortalWired = true;
        // capture=true so the PANEL's own scroll fires this, not just the window's.
        addEventListener('scroll', positionStatsPortal, true);
        addEventListener('resize', positionStatsPortal);
      }
      const rows = (anchor && anchor._statsRows) || [];
      statsPortal.innerHTML = rows.map(r => '<div>' + escHTML(r) + '</div>').join('');
      statsPortalAnchor = anchor;
      statsPortal.style.display = 'block';
      positionStatsPortal();
    }
    function closeStatsPortal() {
      if (statsPortal) statsPortal.style.display = 'none';
      if (statsPortalAnchor) statsPortalAnchor.classList.remove('open');
      statsPortalAnchor = null;
    }
    function positionStatsPortal() {
      if (!statsPortal || !statsPortalAnchor || statsPortal.style.display === 'none') return;
      // Anchor gone (list re-rendered) or hidden (panel closed) → close it.
      if (!document.contains(statsPortalAnchor)) { closeStatsPortal(); return; }
      const r = statsPortalAnchor.getBoundingClientRect();
      if ((r.width === 0 && r.height === 0) || r.bottom < 0 || r.top > innerHeight) { closeStatsPortal(); return; }
      const w = statsPortal.offsetWidth;
      const left = Math.max(6, Math.min(r.right - w, innerWidth - w - 6)); // right-align to the badge, clamp to viewport
      statsPortal.style.left = left + 'px';
      statsPortal.style.top = (r.bottom + 4) + 'px';
    }

    function paintBattery(box, b, stale) {
      if (!box) return;
      const head = box.querySelector('.machine-title');
      if (!head) return;
      let el = head.querySelector('.machine-batt');
      if (!b) { if (el) el.remove(); return; }
      if (!el) {
        el = document.createElement('span');
        el.className = 'machine-batt';
        // Battery sits between the name and the session count, so insert
        // rather than append — paintBattery runs after the header is built.
        el.innerHTML = '<span class="batt-meter"><i></i></span>'
          + '<span class="batt-main"></span><span class="batt-est"></span>'
          + '<span class="batt-detail"></span>';
        head.insertBefore(el, head.querySelector('.machine-count'));
        el.setAttribute('role', 'button');
        el.setAttribute('tabindex', '0');
        // Tapping the gauge opens the reading. stopPropagation because the
        // header's own click toggles the machine's session list, and opening
        // the battery must not also collapse the machine underneath it.
        const toggle = (ev) => {
          ev.stopPropagation();
          const wasOpen = el.classList.contains('open');
          document.querySelectorAll('.machine-batt.open').forEach(o => o.classList.remove('open'));
          if (!wasOpen) el.classList.add('open');
        };
        el.addEventListener('click', toggle);
        el.addEventListener('keydown', (ev) => {
          if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); toggle(ev); }
        });
        wireBattCloser();
      }
      const dur = battDur(b.mins);
      let cls = 'machine-batt';
      if (stale) cls += ' stale';
      else if (b.state === 'charging') cls += ' charging';
      else if (b.state === 'charged') cls += ' plugged';
      else if (b.pct <= 10) cls += ' low';
      el.className = cls;
      el.querySelector('.batt-meter i').style.width = Math.max(0, Math.min(100, b.pct)) + '%';

      // main survives every width; est is dropped on a narrow screen. For a
      // stale reading the TIME lives in main, because "was 20%" without a when
      // is the one thing this feature exists to avoid saying.
      let main, est, title;
      if (stale) {
        main = 'was ' + b.pct + '%';
        est = ' \u00b7 ' + battWhen(b.at).replace(/^at /, '');
        title = 'Last reading before this machine stopped answering'
          + (dur && b.state === 'discharging' ? ' \u2014 about ' + dur + ' left at that point' : '');
      } else if (b.state === 'charging') {
        // The meter is green and filling, so "charging" is reinforcement, not
        // information — it goes in the half that can be dropped.
        main = b.pct + '%';
        est = ' charging' + (dur ? ' \u00b7 ' + dur + ' to full' : '');
        title = dur ? 'About ' + dur + ' until fully charged' : 'Charging';
      } else if (b.state === 'charged') {
        main = b.pct + '%';
        est = ' charged';
        title = 'Plugged in and fully charged';
      } else {
        main = b.pct + '%';
        est = dur ? ' \u00b7 ' + dur + ' left' : '';
        title = dur ? 'About ' + dur + ' of battery left' : 'On battery';
      }
      el.querySelector('.batt-main').textContent = main;
      el.querySelector('.batt-est').textContent = est;
      // The bubble is the ONLY thing visible when collapsed, so it carries the
      // whole reading rather than the abbreviation the row would have shown.
      el.querySelector('.batt-detail').textContent = (main + est).trim()
        + (stale ? ' \u00b7 last seen before it went offline' : '');
      el.title = title;
      el.setAttribute('aria-label', title);
      // The badge just took (or freed) width, so whether the name is clipped
      // may have changed with it.
      syncMachineTight(box);
    }

    async function fillMachineSessions(b64, rec, gen) {
      let resp;
      let ok = true;
      const attnAt = Date.now(); // observation time for attnStore = when we asked
      try {
        resp = await queryDirectory(base64ToU8(b64URLToStd(b64)));
        // The reply comes from a remote machine — validate its shape before
        // trusting it. A malformed/non-object payload (buggy or compromised host)
        // is treated as offline rather than throwing out of the fill (which would
        // strand the box on "checking…" and raise an unhandled rejection).
        if (!resp || typeof resp !== 'object') throw new Error('malformed directory reply');
      } catch (_) { ok = false; }
      // A newer renderMachines() rebuilt the skeleton while we were querying —
      // its own fills own the new boxes; ours would write into stale ones.
      if (gen !== undefined && gen !== machinesGen) return;
      // Re-select the box AFTER the await: the query took seconds, during which
      // the panel may have been re-rendered, so the reference we'd have captured
      // up front could be detached.
      const box = document.querySelector('.machine-box[data-key="' + cssEscape(b64) + '"]');
      if (!box) return;
      const dot = box.querySelector('.machine-dot');
      const sessions = box.querySelector('.machine-sessions');
      const countEl = box.querySelector('.machine-count');
      if (!ok) {
        const n = (machineFail.get(b64) || 0) + 1;
        machineFail.set(b64, n);
        // Grace before a hard offline: the machine's directory host may just be
        // between relay connections (it woke from sleep and is re-dialing, or the
        // probe raced its reconnect). Hold the amber "reconnecting…" dot and keep
        // the cached sessions for the first few misses — the panel's poll re-probes
        // and flips it green the moment the host answers, without a false gray flash
        // or a stale "offline" that no longer matches `reminal list`.
        if (n < MACHINE_GRACE) {
          // Amber "checking" pulse, NOT the blue refresh spinner — a machine
          // that's missing probes must read as "not quite healthy, retrying",
          // visually distinct from a healthy machine refreshing in the background.
          dot.className = 'machine-dot checking';
          dot.title = 'reconnecting…';
          return;
        }
        dot.className = 'machine-dot offline';
        dot.title = 'offline';
        // This is the case the whole feature is for: it cannot tell us
        // anything now, so show what it last said and when.
        const rememberedOffline = battRemembered(rec);
        if (rememberedOffline) paintBattery(box, rememberedOffline, true);
        // Keep the last-known (cached) list visible so a transient blip doesn't
        // blank the card; only show "offline" when there's nothing cached to show.
        if (!Array.isArray(rec.sessions)) {
          if (countEl) countEl.textContent = '';
          sessions.innerHTML = '<div class="machine-sub">offline</div>';
        }
        return;
      }
      machineFail.set(b64, 0);
      dot.className = 'machine-dot online';
      dot.title = 'online';
      maybeCatchUpAlerts(b64, rec, resp); // finish an alert change it missed while offline
      checkAlertRenewFor(b64, rec); // alerts here that need renewing on this device
      // Auto-name an unnamed machine from its reported hostname. Persist WITHOUT
      // re-rendering: we're mid-fill inside renderMachines's Promise.all, and a
      // renderMachines() here would tear down and rebuild every box (flicker) and
      // needlessly re-query the other machines. Just update this label in place
      // and save quietly.
      if (!rec.name && resp.hostname) {
        const nameEl = box.querySelector('.machine-name');
        if (nameEl) nameEl.textContent = resp.hostname;
        // Serialized so parallel fills auto-naming different machines don't clobber
        // each other; re-check the name inside the lock (someone may have set it).
        await withOwnedMachines(m => { if (m[b64] && !m[b64].name) m[b64].name = resp.hostname; else return false; });
      }
      const batt = battFromResp(resp);
      if (batt) {
        paintBattery(box, batt, false);
      } else {
        // The host answered but reported no battery — an older agent that
        // cannot read one. Keep the remembered value rather than blanking it,
        // still marked as history, until it ages out. A true desktop has
        // nothing remembered to keep.
        const kept = battRemembered(rec);
        if (kept) paintBattery(box, kept, true);
        else paintBattery(box, null, false); // clear a badge that just expired
      }
      paintStats(box, resp.stats || null);
      const list = (resp.sessions || []);
      noteAttnList(list, attnAt); // feed the store before painting, so the chips read fresh values
      renderMachineSessions(box, list, b64);
      // Colour the (online) machine dot by what its sessions need — anything
      // awaiting you wins, else anything working, else stay green (idle / all
      // done, which also just means "online"). Offline/checking set above.
      const agg = machineAttn(list);
      if (agg === 'logged-out') { dot.className = 'machine-dot attn-input'; dot.title = 'its agent is logged out'; }
      else if (agg === 'limited') { dot.className = 'machine-dot attn-input'; dot.title = 'its agent is at its usage limit'; }
      else if (agg === 'input') { dot.className = 'machine-dot attn-input'; dot.title = 'needs you'; }
      else if (agg === 'working') { dot.className = 'machine-dot attn-working'; dot.title = 'working'; }
      // Remember this snapshot so the NEXT time the panel opens it paints these
      // sessions instantly from cache while the fresh query runs in the background.
      await withOwnedMachines(m => { if (!m[b64]) return false; m[b64].sessions = list; if (resp.hostname) m[b64].host = resp.hostname; if (batt) { m[b64].batt = batt; if (batt.mins > 0) m[b64].battEst = true; } });
    }

    // Render a machine's session list into its card. Shared by the instant cached
    // paint (renderMachines, from rec.sessions) and the fresh list from a
    // completed query (fillMachineSessions) so the two never drift.
    // sleepingOwned are the sessions asleep on owned machines, as their
    // listings last said (id → its machine and row): opened from the session
    // dock too, one is woken first, as from Machines.
    const sleepingOwned = new Map();
    function wakeIfAsleep(id) {
      const k = sleepingOwned.get(String(id || '').toUpperCase());
      if (!k) return false;
      wakeOwned(k.b64, k.s);
      return true;
    }

    function renderMachineSessions(box, list, b64) {
      const sessions = box.querySelector('.machine-sessions');
      const countEl = box.querySelector('.machine-count');
      if (!sessions) return;
      list = (Array.isArray(list) ? list : []).filter(s => !killedSessionIds.has(s.id));
      for (const s of list) {
        if (s.kind === 'port') continue;
        if (s.asleep) sleepingOwned.set(s.id.toUpperCase(), { b64, s }); else sleepingOwned.delete(s.id.toUpperCase());
      }
      if (countEl) countEl.textContent = list.length ? String(list.length) : '';
      syncMachineTight(box);
      if (list.length === 0) {
        sessions.innerHTML = '<div class="machine-sub">no sessions running</div>';
        return;
      }
      const sorted = list.slice().sort((a, b) => (a.kind === 'port' ? 1 : 0) - (b.kind === 'port' ? 1 : 0) || (a.idle_secs || 0) - (b.idle_secs || 0));
      sessions.innerHTML = '';
      for (const s of sorted) {
        // A shell session shown under its machine card is redundant in Recents —
        // record its id so renderRecents can skip it (ports aren't in Recents).
        if (s.kind !== 'port') ownedSessionIds.add(s.id);
        const isCurrent = !!sessionId && s.id === sessionId;
        const row = document.createElement('div');
        row.className = 'machine-session' + (isCurrent ? ' is-current' : '');
        // Two-line block: the label (title/cwd, wraps up to 2 lines) over its
        // metadata (id · viewers · idle), so nothing gets crushed to an ellipsis.
        // A shell row is an anchor to ?s=<id> so the browser's own "open this
        // somewhere else" gestures work; a port-forward row isn't connectable,
        // so it stays a plain div with no link to offer.
        const main = document.createElement(s.kind === 'port' ? 'div' : 'a');
        main.className = 'ms-main';
        if (s.kind !== 'port') main.href = sessionHref(s.id);
        const label = document.createElement('div');
        label.className = 'ms-label';
        label.textContent = sessionLabelWeb(s);
        const meta = document.createElement('div');
        meta.className = 'ms-meta';
        // Always surface the reminal id (it's how you identify/connect to a
        // session), then viewers/idle — or "current" for the one you're viewing.
        const bits = [s.id];
        if (s.asleep) {
          // Asleep says so even for the session this tab was showing: nothing
          // runs there until it is opened again.
          bits.push('asleep · open it to wake it');
        } else if (isCurrent) {
          // The session you're viewing: keep the live viewer count visible (it
          // includes you), just tagged "current" — idle is meaningless while
          // you're actively connected, so drop it.
          const vc = s.viewers > 0 ? s.viewers + (s.viewers === 1 ? ' viewer' : ' viewers') : '';
          bits.push(['current', vc].filter(Boolean).join(' · '));
        } else {
          const extra = sessionMetaWeb(s);
          if (extra) bits.push(extra);
        }
        // Shells the harness still has running, beside the state and never folded
        // into it: a turn that ended leaving a build going is honestly "done",
        // and this is what says whether done means finished.
        if (s.kind !== 'port' && s.shells > 0) {
          bits.push(s.shells + (s.shells === 1 ? ' shell' : ' shells'));
        }
        meta.textContent = bits.join(' · ');
        main.appendChild(label);
        main.appendChild(meta);
        row.appendChild(main);
        // Attention state word — a quiet chip, right-aligned before the actions,
        // so working/needs-you/done reads without hunting. Idle shows nothing.
        // Reads through attnOf, so a live-pushed state beats the polled one.
        const msAttn = s.asleep ? 'asleep' : s.kind !== 'port' ? attnOf(s.id, s.attn) : '';
        if (msAttn && ATTN_LABELS[msAttn]) {
          const st = document.createElement('div');
          st.className = 'ms-state attn-' + msAttn;
          st.textContent = ATTN_LABELS[msAttn];
          row.appendChild(st);
        }
        const actions = document.createElement('div');
        actions.className = 'ms-actions';
        if (s.kind !== 'port') {
          row.title = (isCurrent ? "The session you're viewing" : 'Connect (no PIN — you own this machine)')
            + ' · ' + NEW_TAB_KEY + '-click opens it in a new tab';
          if (s.asleep) {
            row.classList.add('is-asleep');
            row.title = 'Asleep · open it to wake it (same link and PIN)'
              + (s.dropped && s.dropped.length ? ' · when it slept it let go of: ' + s.dropped.join('; ') : '');
          }
          row.addEventListener('click', (ev) => {
            // A modified click is the browser's to handle — it's already
            // opening the anchor in a new tab/window, and hijacking it here
            // would ALSO tear down this tab's session to connect in place.
            if (wantsNewTab(ev)) return;
            ev.preventDefault(); // don't navigate this tab away; connect in place
            if (s.asleep) wakeOwned(b64, s); else connectOwned(s.id);
          });
          const dup = document.createElement('button');
          dup.className = 'ms-btn';
          dup.title = (s.cwd
            ? 'New session in ' + s.cwd
            : 'New session on this machine')
            + ' (' + NEW_TAB_KEY + '-click opens it in a new tab)';
          dup.innerHTML = mIcon('copy');
          dup.addEventListener('click', (ev) => {
            ev.stopPropagation();
            ev.preventDefault();
            const machineName = box.querySelector('.machine-name')?.textContent || 'this machine';
            spawnOnMachine(b64, machineName, wantsNewTab(ev) ? window.open('', '_blank') : null, s.cwd || '');
          });
          actions.appendChild(dup);
          const ren = document.createElement('button');
          ren.className = 'ms-btn';
          ren.title = 'Rename this session';
          ren.innerHTML = mIcon('edit');
          ren.addEventListener('click', (ev) => { ev.stopPropagation(); renameSessionOnMachine(b64, s.id, s.name || ''); });
          if (!s.asleep) actions.appendChild(ren); // a name is changed in the running session
          if (!s.asleep) {
            const sl = document.createElement('button');
            sl.className = 'ms-btn';
            sl.title = 'Sleep: end its processes and free their memory; it wakes as itself when opened';
            sl.innerHTML = mIcon('moon');
            sl.addEventListener('click', (ev) => { ev.stopPropagation(); ev.preventDefault(); sleepOnMachine(b64, s); });
            actions.appendChild(sl);
          }
        } else {
          row.classList.add('is-port');
          row.title = 'Open :' + (s.port || '?') + ' in a new tab';
          row.addEventListener('click', (ev) => { ev.stopPropagation(); openPortOnMachine(b64, s.id, s.port); });
        }
        // Kill applies to every reminal instance (shells and port forwards).
        const kill = document.createElement('button');
        kill.className = 'ms-btn ms-kill';
        kill.title = 'Kill this session';
        kill.innerHTML = mIcon('x');
        kill.addEventListener('click', (ev) => { ev.stopPropagation(); killSessionOnMachine(b64, s.id, s.name || (s.kind === 'port' ? 'port :' + s.port : '')); });
        actions.appendChild(kill);
        row.appendChild(actions);
        sessions.appendChild(row);
      }
    }

    // Attention state → the word shown in the session's state chip.
    const ATTN_LABELS = { input: 'needs you', working: 'working', done: 'done', 'logged-out': 'logged out', limited: 'limited', asleep: 'asleep' };

    // attnChipEl builds the little state pill shown beside a session name in the
    // Recents dropdown and the mobile dock, or null for an idle/unknown state.
    // Same words + palette as the Machines panel's .ms-state, so the three
    // surfaces can't drift.
    function attnChipEl(attn) {
      if (!attn || !ATTN_LABELS[attn]) return null;
      const c = document.createElement('span');
      c.className = 'attn-chip attn-' + attn;
      c.textContent = ATTN_LABELS[attn];
      return c;
    }

    // attnStore is the ONE source of truth for per-session attention in this
    // viewer. Both feeds write into it — the connected session's live push and
    // every fleet directory poll — and each write carries the time the state was
    // OBSERVED. The newest observation wins; no source ever permanently outranks
    // another. That is what makes it self-healing: a live push lands instantly,
    // and if that socket ever goes quiet, the next poll (≤10s) simply overwrites
    // it. A poll records the time it was STARTED, so a slow in-flight poll can't
    // clobber a push that arrived while it was on the wire.
    //
    // (The previous model — a "live" map that hard-overrode the polled value —
    // could never be invalidated, so a session that went idle stayed stuck on its
    // last non-idle pill. That is gone.)
    const attnStore = new Map(); // id -> { attn, at }
    function noteAttn(id, attn, at) {
      const cur = attnStore.get(id);
      if (cur && cur.at > at) return false; // we already know something newer
      const v = attn || '';
      const changed = !cur || cur.attn !== v;
      attnStore.set(id, { attn: v, at });
      return changed;
    }
    function attnOf(id, polled) {
      const e = attnStore.get(id);
      return e ? e.attn : (polled || '');
    }
    // noteAttnList folds a directory reply's sessions into the store, stamped
    // with the time that query was started. Returns true if any pill changed.
    function noteAttnList(list, at) {
      let changed = false;
      for (const s of (list || [])) {
        if (s && s.id && s.kind !== 'port') changed = noteAttn(s.id, s.attn, at) || changed;
      }
      return changed;
    }
    // repaintAttn refreshes the switchers that show a per-session pill: the
    // Recents dropdown, and the mobile dock IF it's open — patched row by row,
    // never rebuilt: the dock is only open mid-drag, and a rebuild would drop the
    // .active highlight under the finger and invalidate the cached hit boxes.
    function repaintAttn() {
      renderRecents();
      if (sessdockEl && sessdockEl.classList.contains('open')) {
        for (const item of [...sessdockEl.children]) {
          if (item._entry) updateSessdockChip(item._entry.id);
        }
      }
    }
    // setLiveAttn records the connected session's live-pushed state (observed
    // now) and repaints if it changed. Empty means idle — an explicit, real value.
    function setLiveAttn(id, attn) {
      if (noteAttn(id, attn, Date.now())) repaintAttn();
    }

    // updateSessdockChip surgically refreshes one open-dock row's attention chip
    // without touching the rest of the DOM (see repaintAttn).
    function updateSessdockChip(id) {
      for (const item of sessdockEl.children) {
        if (!item._entry || item._entry.id !== id) continue;
        const nameRow = item.querySelector('.name-row');
        if (!nameRow) return;
        const old = nameRow.querySelector('.attn-chip');
        if (old) old.remove();
        const chip = attnChipEl(attnOf(id, item._entry.attn));
        if (chip) nameRow.appendChild(chip);
        return;
      }
    }

    // ---- Warm owner connections: fleet attention polling at socket cost ----
    //
    // The daemon's machine channel answers `dir_query` REPEATEDLY on one open,
    // authenticated socket (handleDirQuery is served from the persistent reader
    // loop and re-reads the registry each time). queryDirectory() does the full
    // owner handshake — a fresh WebSocket plus X25519/Ed25519/HKDF/AES — and
    // closes after one reply. Fine for a one-off; far too expensive to repeat
    // every second per machine (continuous crypto + radio on a phone, and ~4
    // billable relay messages per query). So the fleet poller keeps ONE warm
    // connection per owned machine: handshake once, then each poll is a single
    // tiny message each way. That is what makes a 1s cadence affordable.
    //
    // Cadence is tiered by what the switcher shows: a machine that owns a
    // session in the swipe-up dock (the top SESSDOCK_MAX recents — the ones the
    // user is choosing between) is polled every FLEET_HOT_MS; every other owned
    // machine every FLEET_COLD_MS. Nothing runs while the tab is hidden — nobody
    // is looking — connections are dropped on hide and reopened on show, and an
    // unreachable machine backs off (2s → 30s) instead of being hammered.
    //
    // The handshake below is deliberately IDENTICAL to queryDirectory()'s (same
    // transcript labels, same HKDF salt/info). Keep the two in lockstep.
    const FLEET_HOT_MS = 1000;
    const FLEET_COLD_MS = 5000;
    const FLEET_TICK_MS = 1000;
    const FLEET_REPLY_TIMEOUT_MS = 5000;
    const warmDirs = new Map(); // b64 -> { sock, box, state, inflight, sentAt, lastPoll, fails, retryAt }

    function warmEntry(b64) {
      let w = warmDirs.get(b64);
      if (!w) {
        w = { sock: null, box: null, state: 'closed', inflight: false, sentAt: 0, lastPoll: 0, fails: 0, retryAt: 0 };
        warmDirs.set(b64, w);
      }
      return w;
    }
    function warmFail(w) {
      w.sock = null; w.box = null; w.inflight = false; w.state = 'closed';
      w.fails = Math.min((w.fails || 0) + 1, 5);
      w.retryAt = Date.now() + Math.min(30000, 2000 * Math.pow(2, w.fails - 1)); // 2s, 4s, 8s, 16s, 30s
    }
    function warmClose(w) {
      const s = w.sock;
      w.sock = null; w.box = null; w.inflight = false; w.state = 'closed';
      if (s) { s.onclose = null; s.onerror = null; try { s.close(); } catch (_) {} }
    }
    function warmCloseAll() { for (const w of warmDirs.values()) warmClose(w); }
    function warmSend(b64, w) {
      if (w.state !== 'open' || !w.sock || w.inflight) return;
      w.inflight = true; w.sentAt = Date.now(); w.lastPoll = w.sentAt;
      try { w.sock.send(JSON.stringify({ type: 'dir_query' })); } catch (_) { warmClose(w); warmFail(w); }
    }

    async function warmOpen(b64) {
      const w = warmEntry(b64);
      if (w.state !== 'closed') return;
      w.state = 'opening';
      let dk = null;
      try { dk = await loadDeviceKeyPair(); } catch (_) {}
      if (!dk) { warmFail(w); return; }
      const machinePubU8 = base64ToU8(b64URLToStd(b64));
      const machineB64Cmp = u8ToBase64(machinePubU8);
      let dirID;
      try { dirID = await deriveDirectoryID(machinePubU8); } catch (_) { warmFail(w); return; }
      const sock = new WebSocket(relayURL(dirID));
      let hs = 'auth', att = null;
      w.sock = sock;
      const timer = setTimeout(() => { if (w.sock === sock && w.state === 'opening') { try { sock.close(); } catch (_) {} } }, 6000);
      const ch = { seal: null, key: null };
      const sealer = sealSocket(sock, ch);
      sock.onopen = () => sock.send(JSON.stringify({ type: 'auth' }));
      sock.onerror = () => {};
      sock.onclose = () => { clearTimeout(timer); if (w.sock === sock) warmFail(w); };
      sock.onmessage = async (ev) => {
        if (w.sock !== sock) return; // superseded
        let msg; try { msg = JSON.parse(ev.data); } catch (_) { return; }
        try {
          msg = await admitDirMsg(ch, sealer, msg);
          if (!msg) return;
          if (msg.type === 'error') { try { sock.close(); } catch (_) {} return; }
          if (hs === 'auth') {
            if (msg.type !== 'auth_ok') return;
            const eph = await newEphemeralX25519();
            const exIDBytes = crypto.getRandomValues(new Uint8Array(16));
            const exID = Array.from(exIDBytes).map(b => b.toString(16).padStart(2, '0')).join('');
            const sigs = await ownerInitSigs(dk, dirID, eph.pub);
            att = { eph, exIDBytes, exID, viewerEph: eph.pub, devicePubRaw: dk.pubRaw };
            sock.send(JSON.stringify({ type: 'own_init', ex_id: exID, data: u8ToBase64(eph.pub), device_pub: u8ToBase64(dk.pubRaw), device_sig: sigs.device_sig, frames_sig: sigs.frames_sig, frames: 1 }));
            hs = 'own';
            return;
          }
          if (hs === 'own') {
            // Over its allowance for handshakes: opened again when it says,
            // not taken for a failure (no backoff).
            if (msg.type === 'own_busy' && msg.ex_id === att.exID) {
              clearTimeout(timer);
              warmClose(w);
              w.retryAt = Date.now() + dirBusyMs(msg.retry_ms);
              return;
            }
            if (msg.type !== 'own_resp' || msg.ex_id !== att.exID) return;
            const agentEph = base64ToU8(msg.data || '');
            const machinePub = base64ToU8(msg.machine_pub || '');
            const machineSig = base64ToU8(msg.machine_sig || '');
            if (agentEph.length !== 32 || machinePub.length !== 32) throw new Error('bad key length');
            // Must be the machine we asked for, and its signature must verify.
            if (u8ToBase64(machinePub) !== machineB64Cmp) throw new Error('unexpected machine identity');
            const bound = await ownerAnswerBound(dirID, att.viewerEph, agentEph, att.devicePubRaw, machinePub, machineSig, msg.seal);
            if (bound === null) throw new Error('machine signature invalid');
            const shared = await att.eph.deriveShared(agentEph);
            const sharedKey = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveBits']);
            const wrapBits = await crypto.subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt: att.exIDBytes, info: new TextEncoder().encode('reminal-wrap-v2') }, sharedKey, 256);
            const wrapKey = await crypto.subtle.importKey('raw', wrapBits, { name: 'AES-GCM' }, false, ['decrypt']);
            const wrapped = base64ToU8(msg.wrap || '');
            if (wrapped.length < 28) throw new Error('wrap too short');
            const skRaw = new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: wrapped.slice(0, 12) }, wrapKey, wrapped.slice(12)));
            ch.seal = await admitSeal(shared, att.exIDBytes, msg.seal, skRaw, bound, 'm:' + u8ToBase64(machinePub));
            w.box = await crypto.subtle.importKey('raw', skRaw, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt']);
            ch.key = w.box;
            clearTimeout(timer);
            hs = 'ready'; w.state = 'open'; w.fails = 0;
            warmSend(b64, w); // first poll right away — don't wait for the tick
            return;
          }
          if (hs === 'ready' && msg.type === 'dir_resp') {
            // Observation time = when we ASKED, so a reply that lands after a
            // newer live push can't overwrite it (see attnStore).
            const sentAt = w.sentAt;
            const pt = await decrypt(w.box, msg.data || '');
            const resp = JSON.parse(new TextDecoder().decode(pt));
            // "Busy" (retry_ms) is no listing. To this poll (a plain one: no
            // id), it is asked again when the machine says; another owner's
            // is not this one's to act on.
            if (resp && resp.retry_ms) {
              if (!resp.req_id && w.inflight) setTimeout(() => { if (w.sock === sock) { w.inflight = false; warmSend(b64, w); } }, dirBusyMs(resp.retry_ms));
              return;
            }
            w.inflight = false;
            if (!resp || typeof resp !== 'object' || !Array.isArray(resp.sessions)) return;
            const list = resp.sessions;
            await withOwnedMachines(m => {
              if (!m[b64]) return false;
              m[b64].sessions = list;
              if (resp.hostname) m[b64].host = resp.hostname;
            });
            const changed = noteAttnList(list, sentAt);
            await refreshSessdockPool();
            // Only repaint when a pill actually changed — at 1Hz, rebuilding the
            // Recents list under the user's finger for no reason would be rude.
            if (changed) repaintAttn();
          }
        } catch (_) {
          try { sock.close(); } catch (_) {}
        }
      };
    }

    // The machines whose sessions are in the dock right now — polled hot.
    function fleetHotMachines() {
      const hot = new Set();
      for (const e of sessdockPool.slice(0, SESSDOCK_MAX)) if (e.mkey) hot.add(e.mkey);
      return hot;
    }

    async function fleetTick() {
      if (document.hidden) return;
      let store;
      try { store = await loadOwnedMachines(); } catch (_) { return; }
      const hot = fleetHotMachines();
      const now = Date.now();
      for (const b64 of Object.keys(store)) {
        if (store[b64].pendingRevoke) {
          const gone = warmDirs.get(b64);
          if (gone) { warmClose(gone); warmDirs.delete(b64); }
          continue;
        }
        const w = warmEntry(b64);
        // A reply that never came: the daemon or relay went away under us.
        if (w.inflight && now - w.sentAt > FLEET_REPLY_TIMEOUT_MS) { warmClose(w); warmFail(w); }
        if (w.state === 'closed') { if (now >= w.retryAt) warmOpen(b64); continue; }
        if (w.state !== 'open') continue;
        const period = hot.has(b64) ? FLEET_HOT_MS : FLEET_COLD_MS;
        if (now - w.lastPoll >= period - 50) warmSend(b64, w); // -50: don't skip a beat to tick jitter
      }
    }
    setInterval(fleetTick, FLEET_TICK_MS);
    document.addEventListener('visibilitychange', () => {
      if (document.hidden) warmCloseAll(); else fleetTick();
    });

    // pollFleetNow: the swipe-up gesture wants the freshest state immediately —
    // fire every open connection now rather than waiting for the next tick.
    function pollFleetNow() {
      for (const [b64, w] of warmDirs) warmSend(b64, w);
      fleetTick();
    }

    // machineAttn rolls a machine's sessions up to one dot state: 'logged-out'
    // if an agent there cannot work at all, else 'limited' if one is held back
    // by its usage limit, else 'input' if any session needs
    // you, else 'working' if any is working, else '' (idle / all done → the
    // plain online-green dot). Port forwards don't count. Reads through attnOf
    // so a live push colours the dot too.
    function machineAttn(list) {
      let working = false, needs = false, limited = false;
      for (const s of (list || [])) {
        if (s.kind === 'port') continue;
        const a = attnOf(s.id, s.attn);
        if (a === 'logged-out') return 'logged-out';
        if (a === 'limited') limited = true;
        if (a === 'input') needs = true;
        if (a === 'working') working = true;
      }
      if (limited) return 'limited';
      if (needs) return 'input';
      return working ? 'working' : '';
    }
    function sessionLabelWeb(s) {
      // A named forward is called what its owner called it — "Quarterly
      // report", not "port :8080". The port is still shown, in the detail
      // line, because it is how you recognise which one this is.
      if (s.kind === 'port') return s.name || ('port :' + (s.port || '?'));
      return s.name || s.title || s.cwd || (s.headless ? 'background shell' : 'shell');
    }
    function sessionMetaWeb(s) {
      const parts = [];
      if (s.kind === 'port' && s.name && s.port) parts.push('port :' + s.port);
      if (s.viewers > 0) parts.push(s.viewers + (s.viewers === 1 ? ' viewer' : ' viewers'));
      if (s.idle_secs > 0) parts.push('idle ' + fmtShort(s.idle_secs));
      return parts.join(' · ');
    }
    function fmtShort(secs) {
      if (secs < 60) return secs + 's';
      if (secs < 3600) return Math.floor(secs / 60) + 'm';
      if (secs < 86400) return Math.floor(secs / 3600) + 'h';
      return Math.floor(secs / 86400) + 'd';
    }
    // Owned machine keys are stored url-safe (no pad); the on-wire base64 the
    // handshake uses is standard. Convert for base64ToU8.
    function b64URLToStd(s) {
      let t = s.replace(/-/g, '+').replace(/_/g, '/');
      while (t.length % 4) t += '=';
      return t;
    }
    function cssEscape(s) { return (window.CSS && CSS.escape) ? CSS.escape(s) : s.replace(/["\\]/g, '\\$&'); }

    // Connect to an owned machine's session with no PIN (owner path).
    // Sleep (sleep.go): a session of an owned machine can be put to sleep from
    // here — its processes end, its record is kept — and woken again on its
    // id, PIN and conversation, over the machine channel (the owner's alone).
    // While it wakes, its last saved screen shows, and what you type is held
    // until its agent is back at its prompt (wakeGate): typed into the shell
    // before the agent is back, it would run there. A wake that takes long
    // keeps them held all the same (WAKE_SLOW_MS only changes what the chip
    // says): never let go into a shell whose agent is not back, unless the
    // person says so. An agent may never be back (its resume refused, it is
    // gone, it went back to the shell): Send sends what is held to what is
    // there, and Discard drops it; either stops the wait.
    const WAKE_SLOW_MS = 60000;
    let wakeGate = null; // { id, agent, since }
    function wakeHolding() {
      if (!wakeGate) return false;
      if (sessionId && sessionId !== wakeGate.id) endWakeGate(false); // left for another: what was typed was for this one
      else if (!wakeGate.agent && agentLive && authed) endWakeGate(true); // a plain shell: once it is live
      return !!wakeGate;
    }
    const wakeSlow = () => !!wakeGate && Date.now() - wakeGate.since > WAKE_SLOW_MS;
    // endWakeGate ends the hold of a wake: what was held is sent (send), or
    // dropped (it was for a session no longer shown).
    function endWakeGate(send) {
      if (!wakeGate) return;
      wakeGate = null;
      hideSleepWakeScreen();
      if (!send) { dropHeldInput(); return; }
      if (heldInput.length) heldSince = Date.now(); // held while it woke: not late
      updateHeldChip();
      flushHeldInput();
    }
    // An agent back and ready for what is typed: at its prompt, or held back by
    // its service's limit (still at its prompt). Not one asking something
    // ("input": a question, a folder to trust), which the held keys would answer.
    const WAKE_READY = ['done', 'limited'];
    // wakeAgentBack is told each attention state the woken session reports:
    // its agent back and ready lets the held keys go.
    // Back asking something, it is shown, and what was typed for its prompt
    // is not sent as the answer: said, never lost silently.
    function wakeAgentBack(id, attn) {
      if (!wakeGate || !wakeGate.agent || wakeGate.id !== id) return;
      if (WAKE_READY.includes(attn)) endWakeGate(true);
      else if (attn === 'input') {
        const k = heldCount(), typed = heldText();
        endWakeGate(false);
        if (k) viewerNote('Not sent, it is asking you something. You typed: ' + (typed ? '"' + typed + '"' : heldChars(k)), 20000);
      }
    }
    // heldText is what is held, as text to show (and copy): keys and paste
    // markers out, line ends as spaces, at most 200 characters.
    function heldText() {
      const s = heldInput.map((it) => it.data).join('')
        .replace(/\x1b\[20[01]~/g, '')
        .replace(/\x1b\[[0-9;?]*[ -/]*[@-~]|\x1bO.|\x1b./g, '')
        .replace(/[\r\n]+/g, ' ')
        .replace(/[\x00-\x1f\x7f]/g, '')
        .trim();
      return s.length > 200 ? s.slice(0, 200) + '…' : s;
    }
    function showSleepWakeScreen(name, screen) {
      const el = document.getElementById('wake-screen');
      if (!el) return;
      el.querySelector('.wake-title').textContent = 'Waking ' + name + '…';
      el.querySelector('.wake-rows').textContent = screen || '';
      el.hidden = false;
    }
    function hideSleepWakeScreen() {
      const el = document.getElementById('wake-screen');
      if (el) el.hidden = true;
    }
    async function wakeOwned(b64, s) {
      const name = sessionLabelWeb(s);
      showSleepWakeScreen(name, '');
      setStatus('Waking ' + name + '…', 'info');
      try {
        const r = await queryDirectory(base64ToU8(b64URLToStd(b64)), { sleep: { id: s.id, act: 'wake' }, timeout: DIR_SPAWN_TIMEOUT_MS });
        if (r && r.screen) showSleepWakeScreen(name, r.screen);
        wakeGate = { id: s.id.toUpperCase(), agent: !!s.fg, since: Date.now() };
        sleepingOwned.delete(s.id.toUpperCase());
        setTimeout(updateHeldChip, WAKE_SLOW_MS + 100); // the chip says when it is taking long
        connectOwned(s.id);
        renderMachines();
      } catch (e) {
        hideSleepWakeScreen();
        setStatus('Could not wake ' + s.id + ': ' + dirActionError(e), 'error');
      }
    }
    // dirActionError is why an action on an owned machine failed, in words a
    // person can act on: a machine whose reminal is older than the action is
    // told to update.
    function dirActionError(e) {
      if (e && e.reply && e.reply.unsupported) return 'reminal on that computer is too old for this · run reminal upgrade there';
      return (e && e.message) || 'error';
    }
    // sleepOnMachine puts one of an owned machine's sessions to sleep. Refused
    // for what it would end (a job running in it, an unfinished turn), it says
    // what, and sleeps only if you say so.
    async function sleepOnMachine(b64, s) {
      const machine = base64ToU8(b64URLToStd(b64));
      const ask = async (act) => queryDirectory(machine, { sleep: { id: s.id, act }, timeout: DIR_SPAWN_TIMEOUT_MS });
      try {
        try {
          await ask('sleep');
        } catch (e) {
          const lose = e.reply && e.reply.lose;
          if (!lose || !lose.length) throw e;
          if (!confirm('Sleeping ' + s.id + ' would end:\n\n- ' + lose.join('\n- ') + '\n\nA wake can\x27t bring these back. Sleep anyway?')) return;
          await ask('sleep_anyway');
        }
        setStatus(s.id + ' is asleep · open it to wake it', 'info');
        renderMachines();
      } catch (e) {
        setStatus('Could not sleep ' + s.id + ': ' + dirActionError(e), 'error');
      }
    }

    function connectOwned(id) {
      sessionInput.value = id;
      pinInput.value = '';
      pinForSession = '';
      pinTyped = false;
      const rec = document.getElementById('recents'); if (rec) rec.open = false;
      if (sessionId && sessionId !== id.toUpperCase()) userDisconnect({ switching: true });
      startConnect();
    }

    // Spawn a new session on an owned machine, then connect to it.
    //
    // With a `tab` handle (⌘-click on the + button) the new session lands THERE
    // instead of here. The tab must be opened synchronously inside the click or
    // the popup blocker eats it, so it arrives blank and we point it at the
    // session once the spawn lands — or close it again if the spawn never does,
    // rather than leaving a stranded about:blank behind.
    async function spawnOnMachine(b64, name, tab, cwd) {
      try {
        const where = cwd ? ' in ' + cwd : '';
        setStatus('Starting a session on ' + name + where + (tab ? ' in a new tab…' : '…'), 'info');
        // DIR_SPAWN_TIMEOUT_MS covers the host's spawn budget + the directory
        // channel's owner handshake, so a slow-but-successful spawn isn't reported
        // as a failure here (which would strand a session the user was told never
        // started).
        const r = await queryDirectory(base64ToU8(b64URLToStd(b64)), { spawn: true, spawnName: '', spawnCwd: cwd || '', timeout: DIR_SPAWN_TIMEOUT_MS });
        if (!r || !r.id) {
          if (tab) tab.close();
          setStatus('Could not start a session on ' + name, 'error');
          return;
        }
        if (tab) {
          tab.location = sessionHref(r.id); // owner tab → PIN-free connect on load
          setStatus('Started ' + r.id + ' on ' + name + ' in a new tab', 'info');
          return;
        }
        connectToSpawnedSession(r, true); // owner spawn → PIN-free connect
      } catch (e) {
        if (tab) tab.close();
        // Reached-but-failed (host returned an error) and couldn't-reach both land
        // here; surface the host's reason when we have one rather than guessing.
        setStatus('Could not start a session on ' + name + (e && e.message && e.message !== 'timeout' ? ' (' + e.message + ')' : ''), 'error');
      }
    }

    // Rename a live session ON an owned machine — routed through its directory
    // host to the session's control socket, so the new name is real (persists and
    // shows everywhere), not just a local nickname.
    async function renameSessionOnMachine(b64, id, currentName) {
      const next = prompt('Rename session ' + id + ':', currentName || '');
      if (next === null) return;
      const name = next.trim();
      if (!name) return;
      try {
        await queryDirectory(base64ToU8(b64URLToStd(b64)), { rename: { id, name } });
        setStatus('Renamed ' + id + ' → “' + name + '”', 'info');
        renderMachines(); // re-query so the new name shows
      } catch (e) {
        setStatus('Could not rename ' + id + ' (' + (e.message || 'error') + ')', 'error');
      }
    }

    // Kill a live session ON an owned machine — terminates it (shell + anything
    // running in it). Confirmed, since it's irreversible.
    // Open an exposed port's app in a new tab, already past its PIN gate.
    //
    // The link and its PIN are fetched per click rather than read from the
    // session list, which is cached in IndexedDB — a PIN carried in that list
    // would be written to disk in every browser that opened this panel. Only an
    // owner can ask: dir_query is served on the machine channel alone.
    //
    // The tab is opened BEFORE the await, while the click is still on the stack.
    // A window.open() after it reads as unprompted and gets swallowed.
    async function openPortOnMachine(b64, id, port) {
      const tab = window.open('', '_blank');
      // The forwarded app is someone's own page: it gets no handle on this
      // one (window.opener), as a link opened with noopener would not.
      if (tab) { try { tab.opener = null; } catch (_) {} }
      try {
        const r = await queryDirectory(base64ToU8(b64URLToStd(b64)), { open: id });
        if (r.open_error) throw new Error(r.open_error);
        if (!r.open_url) throw new Error('this host is too old to open ports from here');
        // A web address only: the host names it, and a javascript: one opened
        // in a tab this page made would run as this page.
        if (!/^https?:\/\//i.test(r.open_url)) throw new Error('the host gave an address that is not a web page');
        // The PIN rides the fragment, which never leaves the browser: the gate
        // fills it in, submits, and drops it from history. A --public port has
        // no gate and simply ignores the fragment.
        // The address comes from the machine. The tab it is assigned to was
        // opened from this page, so anything but a web address — a script
        // URL, say — would run as this page. Only http(s) is a link.
        let target;
        try { target = new URL(String(r.open_url)); } catch (_) { throw new Error('the machine sent an address that is not a link'); }
        if (target.protocol !== 'https:' && target.protocol !== 'http:') throw new Error('the machine sent an address that is not a link');
        if (r.open_pin) target.hash = 'p=' + encodeURIComponent(String(r.open_pin));
        const url = target.toString();
        if (!tab) { setStatus('Allow pop-ups to open :' + (port || '?') + ' from here', 'error'); return; }
        tab.location = url;
        setStatus('Opened :' + (port || '?'), 'info');
      } catch (e) {
        if (tab) { try { tab.close(); } catch (_) {} }
        setStatus('Could not open :' + (port || '?') + ' (' + (e.message || 'error') + ')', 'error');
      }
    }

    async function killSessionOnMachine(b64, id, label) {
      if (!confirm('Kill session ' + id + (label ? ' (' + label + ')' : '') + ' on this machine?\n\n' +
          'The shell and anything running in it stop immediately. This is irreversible.')) return;
      try {
        await queryDirectory(base64ToU8(b64URLToStd(b64)), { kill: id });
        setStatus('Killed ' + id, 'info');
        // Remember it as killed so it stays out of the list through any racing
        // re-query / cached snapshot / reconnect, and drop it from the cache now.
        killedSessionIds.add(id);
        // A killed session is gone for good (ids are never reused), so its
        // Recents entry is a dead link — and worse, removing it from the
        // machine card alone PROMOTES that entry into "Recent sessions"
        // (it stops being filtered as an owned-card duplicate). Forget it
        // everywhere: history store, Recents list, and the session dock.
        forgetSession(id);
        await withOwnedMachines(m => {
          if (m[b64] && Array.isArray(m[b64].sessions)) { m[b64].sessions = m[b64].sessions.filter(s => s.id !== id); }
          else { return false; }
        });
        renderMachines();
      } catch (e) {
        setStatus('Could not kill ' + id + ' (' + (e.message || 'error') + ')', 'error');
      }
    }

    // Forget an owned machine — removes it from THIS device's Machines list. It
    // reappears if you owner-connect to it again; to fully revoke this device's
    // ownership, run `reminal owners revoke` on that machine.
    async function forgetMachine(b64, name) {
      if (!confirm('Remove “' + name + '” and revoke this device\'s PIN-free access to it?\n\n' +
          'The machine drops this device as an owner. If it\'s offline now, that happens the next time it\'s reachable. (You can re-own it later by enrolling again.)')) return;
      setStatus('Revoking this device on ' + name + '…', 'info');
      if (await tryRevokeSelf(b64)) {
        await forgetOwnedMachineWeb(b64);
        setStatus('Removed “' + name + '” and revoked this device on it', 'info');
      } else {
        // Machine unreachable — queue the revoke; retryPendingRevokes (run on
        // every panel open) completes it the next time the machine is online.
        await withOwnedMachines(m => { if (m[b64]) m[b64].pendingRevoke = true; else return false; });
        renderMachines();
        setStatus('“' + name + '” is offline — it\'ll be revoked and removed when it\'s next reachable', 'info');
      }
    }

    // Drop a machine stuck in the "removing when online…" queue from this browser
    // right now, without waiting for it to come back. The pending self-revoke is
    // abandoned — if you still need to drop this device as an owner, do it on the
    // machine itself (reminal owners revoke <id>).
    async function forgetPendingMachine(b64, name) {
      if (!confirm('Stop waiting and forget “' + name + '” now?\n\n' +
          'It just disappears from this browser. The queued revoke is dropped — if you still need to remove this device as an owner, run “reminal owners revoke <id>” on the machine.')) return;
      await forgetOwnedMachineWeb(b64);
    }

    // Ask the machine to revoke THIS device's ownership (self-revoke). Resolves
    // true only if the machine acked; false if unreachable/failed.
    async function tryRevokeSelf(b64) {
      try {
        const r = await queryDirectory(base64ToU8(b64URLToStd(b64)), { revokeSelf: true });
        return !!(r && r.ok);
      } catch (_) { return false; }
    }

    // Complete any queued self-revokes whose machine is now reachable. Fire-and-
    // forget from renderMachines so opening the panel drains the queue.
    async function retryPendingRevokes() {
      let store;
      try { store = await loadOwnedMachines(); } catch (_) { return; }
      for (const b64 of Object.keys(store)) {
        if (!store[b64].pendingRevoke) continue;
        if (await tryRevokeSelf(b64)) await forgetOwnedMachineWeb(b64);
      }
    }

    // startSessionFlow runs the one-time "hello" line plus the
    // resume/resize handshake the agent needs to start streaming. It's
    // gated on BOTH cryptoKey (EKE finished) and agentLive (the relay
    // says the agent is here): the two arrive in arbitrary order and
    // we have to wait for the slower one.
    function startSessionFlow() {
      if (!cryptoKey || !agentLive) return;
      settleUploadWaiters(true); // an upload waiting for this link goes now
      if (!helloShown) {
        helloShown = true;
        const ht = lastDialMs > 0 ? ' · handshake ' + lastDialMs + 'ms' : '';
        viewerNote('Connected to ' + sessionId + ht + ' · AES-256-GCM end-to-end');
      } else if (reconnectMessagePending) {
        // We dropped (either WS hiccup or agent_offline transition)
        // and just came back. Status-bar change alone isn't enough —
        // users staring at the terminal area don't notice it: a note says
        // we're live again, over the terminal and never in it (viewerNote).
        reconnectMessagePending = false;
        // An upgrade's restart ends in exactly this reconnect; onHostInfo
        // waits for it before judging the version that comes back.
        if (upgradeState && upgradeState.stage === 'restart') upgradeState.reconnected = true;
        viewerNote('Reconnected to ' + sessionId);
      }
      // Sent already, right behind the handshake whose answer brought the
      // key (sendEarlyResume). Behind any other — one answered pake_busy, an
      // owner attempt that fell back to the PIN — its screen came before
      // there was a key to open it, and is asked for again.
      const early = earlyResume;
      earlyResume = null;
      const dropped = droppedBeforeKey === ws;
      droppedBeforeKey = null;
      if (!(early && early.sock === ws && early.exID === keyExID && !dropped)) sendResume();
      publishSize();
      term.focus();
      if (winStreamsNeedRestart) {
        winStreamsNeedRestart = false;
        restartWinStreams();
      }
      // Warm the window list so the Windows dropdown opens instantly (no
      // "Loading…" flash) the first time it's clicked — and let the reply
      // restore any panes this session had open (Recent → reopens its windows).
      pendingRestore = true;
      if (typeof sendWin === 'function') sendWin('window_list');
      if (typeof refreshOpenApps === 'function') refreshOpenApps();
      // Ask the host about itself (name + basic stats) for the Host dropdown
      // and to label this session in Recent with its computer name.
      if (typeof sendWin === 'function') sendWin('host_info');
      // Offer the agent a peer-to-peer channel for window frames (no-op if one
      // is already negotiating/up). Frames fall back to WS until it connects.
      ensureWebRTC();
      // Live: what was typed while it was not goes now, in order.
      flushHeldInput();
    }

    async function encrypt(key, data) {
      const bytes = typeof data === 'string' ? new TextEncoder().encode(data) : data;
      const nonce = crypto.getRandomValues(new Uint8Array(12));
      const ct = await crypto.subtle.encrypt({ name: 'AES-GCM', iv: nonce }, key, bytes);
      const out = new Uint8Array(nonce.length + ct.byteLength);
      out.set(nonce);
      out.set(new Uint8Array(ct), nonce.length);
      // Chunked because a spread (`...out`) blows the call stack for
      // large ciphertexts (chunked file uploads can produce ~256 KB).
      let binary = '';
      const stride = 0x1000;
      for (let i = 0; i < out.length; i += stride) {
        binary += String.fromCharCode.apply(null, out.subarray(i, i + stride));
      }
      return btoa(binary);
    }

    // A sealed frame's payload once this page has opened it (unsealIn):
    // only what openedFrame made is taken as opened. Anything else that is
    // not ciphertext — an object a message carries in its JSON — is refused,
    // never passed through as if it had been decrypted.
    const openedFrames = new WeakSet();
    function openedFrame(bytes) {
      const d = { __plain: bytes };
      openedFrames.add(d);
      return d;
    }
    async function decrypt(key, encoded) {
      // A sealed frame arrives already opened (unsealIn); pass it through.
      if (encoded && typeof encoded === 'object' && openedFrames.has(encoded)) return encoded.__plain;
      if (typeof encoded !== 'string') throw new Error('not an encrypted payload');
      const raw = Uint8Array.from(atob(encoded), c => c.charCodeAt(0));
      const nonce = raw.slice(0, 12);
      const ct = raw.slice(12);
      const pt = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: nonce }, key, ct);
      return new Uint8Array(pt);
    }

    // ===== Sealed frames (internal/crypto/frames.go) =====
    // Once the agent's handshake answer says it supports them, every session
    // message travels sealed: its type, its direction, our stream and counter,
    // and its seq are bound into the encryption, with one key per direction. A
    // frame then opens only as the message it was sent as, the way it was sent,
    // once. An agent that predates them never says so, and nothing changes.
    const UNSEALED = new Set(['auth', 'auth_ok', 'register', 'join', 'connected', 'error',
      'ping', 'pong', 'closed', 'resume', 'agent_online', 'agent_offline', 'kex_init',
      'kex_resp', 'pake_init', 'pake_resp', 'pake_busy', 'own_init', 'own_resp', 'own_busy', 'sealed',
      'park', 'unpark', 'parked', 'unparked', 'park_refused']);
    const sealable = (t) => !!t && !UNSEALED.has(t);
    let sealState = null; // this connection's: { a2v, v2a, stream, outCtr, inLast }
    let sealSendChain = Promise.resolve();
    let sealRecvChain = Promise.resolve();
    async function frameKeyFrom(sessionKeyRaw, info) {
      const ikm = await crypto.subtle.importKey('raw', sessionKeyRaw, 'HKDF', false, ['deriveBits']);
      const bits = await crypto.subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt: new Uint8Array(0), info: utf8(info) }, ikm, 256);
      return crypto.subtle.importKey('raw', bits, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt']);
    }
    function u32be(n) { return new Uint8Array([(n >>> 24) & 255, (n >>> 16) & 255, (n >>> 8) & 255, n & 255]); }
    function u64be(n) {
      const b = new Uint8Array(8);
      let v = BigInt(n);
      for (let i = 7; i >= 0; i--) { b[i] = Number(v & 0xffn); v >>= 8n; }
      return b;
    }
    function frameAAD(dir, inner, stream, ctr, seq) {
      const t = utf8(inner);
      return concatBytes(utf8('reminal-frame-v1'), new Uint8Array([dir]), u32be(t.length), t, u32be(stream), u64be(ctr), u64be(seq));
    }
    // The handshake answer's seal info: our stream, and the agent counter it
    // has already passed. Encrypted under the handshake's own key, so it is
    // what the agent sent. null when the agent predates sealed frames.
    // A handshake's sealed-frames answer as this browser takes it: the bound
    // offer answered must carry it; an unbound answer is taken sealed when it
    // carries it, and unsealed only from a peer never seen to seal.
    async function admitSeal(shared, exIDBytes, sealed, sessionKeyRaw, bound, peer) {
      if (!sealed) {
        if (bound) throw new Error('no sealed frames in an answer to the offer of them');
        if (SEALED_FRAMES_REQUIRED || sealedSeen(peer)) throw new Error('this machine answered with sealed frames before and now does not — refused');
        return null;
      }
      const st = await openSealInfo(shared, exIDBytes, sealed, sessionKeyRaw);
      noteSealed(peer);
      return st;
    }
    // An owner handshake's two device signatures: over the transcript as
    // before, and over the one naming sealed frames.
    async function ownerInitSigs(dk, sid, viewerEph) {
      const tr = await ownerTranscript('reminal-owner-cli-v1', sid, [viewerEph, dk.pubRaw]);
      const trF = await ownerTranscript('reminal-owner-cli-v1', sid, [viewerEph, dk.pubRaw, utf8(FRAMES_LABEL)]);
      return {
        device_sig: u8ToBase64(await deviceSign(dk, tr)),
        frames_sig: u8ToBase64(await deviceSign(dk, trF)),
      };
    }
    // The machine's signature on an owner answer: over the transcript with the
    // answer to the offer signed in (true), over the one as before (false), or
    // neither (null).
    async function ownerAnswerBound(sid, viewerEph, agentEph, devicePubRaw, machinePub, machineSig, seal) {
      const trF = await ownerTranscript('reminal-owner-srv-v1', sid, [viewerEph, agentEph, devicePubRaw, machinePub, utf8(FRAMES_LABEL), utf8(seal || '')]);
      if (await ed25519Verify(machinePub, machineSig, trF)) return true;
      const tr = await ownerTranscript('reminal-owner-srv-v1', sid, [viewerEph, agentEph, devicePubRaw, machinePub]);
      if (await ed25519Verify(machinePub, machineSig, tr)) return false;
      return null;
    }
    async function openSealInfo(shared, exIDBytes, sealed, sessionKeyRaw) {
      if (!sealed) return null;
      const ikm = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveBits']);
      const bits = await crypto.subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt: exIDBytes, info: utf8('reminal-seal-info-v1') }, ikm, 256);
      const k = await crypto.subtle.importKey('raw', bits, { name: 'AES-GCM' }, false, ['decrypt']);
      const raw = base64ToU8(sealed);
      const pt = new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: raw.slice(0, 12), additionalData: exIDBytes }, k, raw.slice(12)));
      if (pt.length !== 12) throw new Error('bad seal info');
      const dv = new DataView(pt.buffer);
      return {
        a2v: await frameKeyFrom(sessionKeyRaw, 'reminal-frame-v1 agent-to-viewer'),
        v2a: await frameKeyFrom(sessionKeyRaw, 'reminal-frame-v1 viewer-to-agent'),
        stream: dv.getUint32(0), inLast: Number(dv.getBigUint64(4)), outCtr: 0,
      };
    }
    // makeSealer seals and opens frames for one socket's handshake state
    // (getState) and session key (getKey): send() takes what would have been
    // sent as it is, open() takes a sealed frame and gives back the message it
    // carries, or null to drop it. Both keep arrival order.
    function makeSealer(getState, getKey) {
      let sendChain = Promise.resolve(), recvChain = Promise.resolve();
      return {
        send(rawSend, text) {
          const st = getState();
          if (!st) { rawSend(text); return; }
          sendChain = sendChain.then(async () => {
            if (st !== getState()) return; // a newer handshake: this one's messages are moot
            let msg = null;
            try { msg = JSON.parse(text); } catch (_) {}
            if (!msg || !sealable(msg.type)) { rawSend(text); return; }
            let kind = 0, payload = new Uint8Array(0);
            if (msg.data) {
              try { payload = await decrypt(getKey(), msg.data); kind = 1; }
              catch (_) { payload = utf8(String(msg.data)); kind = 2; }
            }
            const ctr = ++st.outCtr;
            const seq = typeof msg.seq === 'number' ? msg.seq : 0;
            const nonce = crypto.getRandomValues(new Uint8Array(12));
            const ct = new Uint8Array(await crypto.subtle.encrypt(
              { name: 'AES-GCM', iv: nonce, additionalData: frameAAD(2, msg.type, st.stream, ctr, seq) },
              st.v2a, concatBytes(new Uint8Array([kind]), payload)));
            const out = { type: 'sealed', inner: msg.type, stream: st.stream, ctr, data: u8ToBase64(concatBytes(nonce, ct)) };
            if (seq) out.seq = seq;
            rawSend(JSON.stringify(out));
          }).catch(() => {});
        },
        open(msg) {
          const st = getState();
          recvChain = recvChain.then(async () => {
            if (!st || st !== getState() || !sealable(msg.inner)) return null;
            const ctr = Number(msg.ctr) || 0;
            const seq = typeof msg.seq === 'number' ? msg.seq : 0;
            if (ctr <= st.inLast) return null;
            try {
              const raw = base64ToU8(msg.data || '');
              const pt = new Uint8Array(await crypto.subtle.decrypt(
                { name: 'AES-GCM', iv: raw.slice(0, 12), additionalData: frameAAD(1, msg.inner, 0, ctr, seq) },
                st.a2v, raw.slice(12)));
              if (!pt.length || pt[0] > 2 || ctr <= st.inLast) return null;
              st.inLast = ctr;
              const out = { type: msg.inner };
              if (typeof msg.seq === 'number') out.seq = msg.seq;
              if (pt[0] === 1) out.data = openedFrame(pt.slice(1));
              else if (pt[0] === 2) out.data = new TextDecoder().decode(pt.slice(1));
              return out;
            } catch (_) { return null; }
          }).catch(() => null);
          return recvChain;
        },
      };
    }
    // The session socket's: everything sent on it passes through sessionSend.
    const sessionSealer = makeSealer(() => sealState, () => cryptoKey);
    const sessionSend = (rawSend, text) => sessionSealer.send(rawSend, text);
    const unsealIn = (msg) => sessionSealer.open(msg);
    // sealSocket puts a machine-channel socket's sends through a sealer and
    // returns that sealer; its handshake state and key live on ch.
    function sealSocket(sock, ch) {
      const sealer = makeSealer(() => ch.seal, () => ch.key);
      const rawSend = sock.send.bind(sock);
      sock.send = (text) => sealer.send((t) => { try { rawSend(t); } catch (_) {} }, text);
      return sealer;
    }
    // admitDirMsg is what a machine-channel socket does with each message
    // before acting on it: the message to act on, null to drop it, or throws
    // when the machine seals but this socket's handshake answer said it does
    // not.
    async function admitDirMsg(ch, sealer, msg) {
      if (msg.type === 'sealed') {
        if (!ch.key) return null; // another owner's traffic, before our handshake
        if (!ch.seal) throw new Error('connection interrupted');
        return await sealer.open(msg);
      }
      if (ch.seal && sealable(msg.type)) return null; // the copy for clients that predate sealing
      return msg;
    }

    function relayURL(sessionId) {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      return proto + '//' + location.host + '/ws/' + sessionId.toUpperCase() + '/viewer';
    }

    // viewerNote shows a line of the viewer's own over the terminal for a
    // few seconds: never in the terminal's text. A program drawing the whole
    // screen (an agent's TUI) moves its cursor relative to where it left it;
    // a line of ours written in between moved the screen under it, and its
    // next redraws landed a row off, leaving half-lines and stray characters
    // until something made it repaint (a resize).
    function viewerNote(text, ms = 5000) {
      const box = document.getElementById('viewer-notes');
      if (!box) return;
      const el = document.createElement('div');
      el.className = 'viewer-note';
      el.textContent = text;
      box.appendChild(el);
      while (box.children.length > 3) box.firstChild.remove();
      setTimeout(() => { el.classList.add('gone'); setTimeout(() => el.remove(), 450); }, ms);
    }

    function setStatus(text, cls) {
      statusEl.textContent = text;
      statusEl.className = cls || '';
    }
    // Before any connect, what is still to type: the session and its PIN,
    // or, the session there already, only the PIN. Once a connect has said
    // something of its own, that stays.
    const STATUS_IDLE = ['Session + PIN required', 'Enter the PIN'];
    const idleStatus = () => {
      if (!STATUS_IDLE.includes(statusEl.textContent) || statusEl.className) return;
      statusEl.textContent = sessionInput.value.trim() && !pinInput.value.trim() ? 'Enter the PIN' : 'Session + PIN required';
    };
    sessionInput.addEventListener('input', idleStatus);
    pinInput.addEventListener('input', idleStatus);
    idleStatus();

    function showOverlay(text) {
      overlayText.textContent = text;
      overlay.classList.add('visible');
    }

    function hideOverlay() {
      overlay.classList.remove('visible');
    }

    function setBtnConnect() {
      connectBtn.textContent = 'Connect';
      connectBtn.disabled = false;
      connectBtn.classList.remove('danger');
      document.body.classList.remove('connected');
      // Reset the tab title so a disconnected pane in the browser strip
      // doesn't pretend to still be live.
      if (typeof refreshTabTitle === 'function') {
        const realId = sessionId;
        sessionId = '';
        refreshTabTitle();
        sessionId = realId;
      }
      if (typeof releaseWakeLock === 'function') releaseWakeLock();
    }

    function setBtnDisconnect() {
      connectBtn.textContent = 'Disconnect';
      connectBtn.disabled = false;
      connectBtn.classList.add('danger');
      // Collapse the session/PIN inputs out of the header so the terminal
      // gets the freed vertical space (real estate is precious on phones).
      document.body.classList.add('connected');
    }

    function setBtnBusy(label) {
      connectBtn.textContent = label;
      connectBtn.disabled = true;
      connectBtn.classList.remove('danger');
    }

    // Batch incoming terminal data so a refresh-replay (hundreds of
    // small TypeData frames arriving in <100ms) renders as a single
    // term.write() — not as N animated scrolls. Single-keystroke echo
    // is still <16ms perceived latency (one frame), well below any
    // human noticeable threshold.
    const pendingChunks = [];
    let flushScheduled = false;
    function flushPendingChunks() {
      if (pendingChunks.length === 0) return;
      // Every path that writes the session's output comes through here.
      if (staleScreen) {
        staleScreen = false;
        const enc = new TextEncoder();
        pendingChunks.unshift(enc.encode('\x1b[H\x1b[2J\x1b[3J'));
        document.getElementById('terminal').classList.remove('stale');
      }
      let total = 0;
      for (const c of pendingChunks) total += c.byteLength;
      if (total === 0) { pendingChunks.length = 0; return; }
      const merged = new Uint8Array(total);
      let off = 0;
      for (const c of pendingChunks) {
        merged.set(c instanceof Uint8Array ? c : new Uint8Array(c), off);
        off += c.byteLength;
      }
      pendingChunks.length = 0;
      // Position-preserving repaint: the agent's snapshot (sent whenever ANY
      // viewer attaches or falls behind — the relay broadcasts it to everyone)
      // begins with ED2+ED3, wiping this viewer's scrollback and repainting.
      // If the user is scrolled up reading, that yanks them off their spot
      // (worst with a TUI generating output, when this viewer is momentarily
      // behind the snapshot's seq so the dedup-by-seq doesn't drop it). Detect
      // the wipe signature and restore the same distance-from-bottom after the
      // write lands. A genuine in-app clear (ESC[2J ESC[3J) leaves no history,
      // so the restore clamps to the bottom there — indistinguishable from
      // today's behavior.
      const bufA = term.buffer.active;
      const distUp = bufA.baseY - bufA.viewportY;
      if (hasWipeSignature(merged)) {
        term.write(merged, () => {
          const b2 = term.buffer.active;
          cleanBaseY = b2.baseY;                     // buffer just laundered
          if (distUp > 0) term.scrollLines((b2.baseY - distUp) - b2.viewportY); // clamps at top
        });
      } else {
        term.write(merged);
      }
    }
    // ESC[2J ESC[3J — the scrollback-wipe prefix every agent snapshot carries.
    function hasWipeSignature(u8) {
      for (let i = 0, n = u8.length - 7; i < n; i++) {
        if (u8[i] === 0x1b && u8[i + 1] === 0x5b && u8[i + 2] === 0x32 && u8[i + 3] === 0x4a &&
            u8[i + 4] === 0x1b && u8[i + 5] === 0x5b && u8[i + 6] === 0x33 && u8[i + 7] === 0x4a) return true;
      }
      return false;
    }
    function enqueueData(bytes) {
      // Skip any prefix the user already saw via local echo (mosh-
      // style predictive). When predictions match, this prefix is the
      // shell's actual echo coming back — rendering it would double
      // the visible character.
      const u8 = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
      lastDataAt = Date.now();
      // Moments only watches: nothing it does may stop a chunk reaching the screen.
      if (window.reminalMoments) try { reminalMoments.output(sessionId, u8); } catch (_) {}
      const after = reconcilePredictions(u8);
      if (after.byteLength === 0) return;
      pendingChunks.push(after);
      if (flushScheduled) return;
      flushScheduled = true;
      requestAnimationFrame(() => {
        flushScheduled = false;
        // pendingChunks may have already been drained synchronously
        // by predictLocalEcho's flushPendingChunks() — that's fine,
        // flushPendingChunks is a no-op on empty.
        flushPendingChunks();
      });
    }

    async function send(msg) {
      // The socket it was asked on: sealing is awaited, and the session may
      // be left meanwhile — its socket closed (null), or the next one's open.
      // A frame sealed for this session goes on this session's socket or not
      // at all.
      const sock = ws;
      if (!sock || sock.readyState !== WebSocket.OPEN) return;
      if (msg.type === 'data' || msg.type === 'resize') {
        // Drop rather than leak plaintext: cryptoKey is only null
        // during the brief window between auth_ok and pake_resp, when
        // the user shouldn't be able to type anyway (the terminal
        // isn't focused yet). Sending unencrypted would just get
        // dropped on the agent side after a failed Decrypt, but
        // dropping here is cleaner.
        if (!authed || !cryptoKey) return;
        if (msg.type === 'data') {
          msg = { type: 'data', data: await encrypt(cryptoKey, Uint8Array.from(atob(msg.data), c => c.charCodeAt(0))) };
        } else {
          const payload = JSON.stringify({ cols: msg.cols, rows: msg.rows, viewer: viewerId, ...(msg.away ? { away: true } : {}) });
          msg = { type: 'resize', data: await encrypt(cryptoKey, payload) };
        }
        if (ws !== sock || sock.readyState !== WebSocket.OPEN) return;
      }
      sock.send(JSON.stringify(msg));
    }

    // iOS (incl. iPadOS, which reports as "MacIntel" + touch) is the only
    // platform whose soft keyboard OVERLAYS the page instead of resizing
    // the layout viewport — there CSS 100dvh stays full-height and the
    // keyboard hides the terminal, so we must pin <body> to the visual
    // viewport. EVERYWHERE ELSE (Android Chrome with
    // interactive-widget=resizes-content, desktop) the layout viewport
    // resizes natively and 100dvh tracks it reliably — overriding height
    // there is actively harmful: Android reports a STALE visualViewport
    // .height for a beat after a keyboard close, so an override pins <body>
    // back to the old small height and fights the correct 100dvh grow
    // (the "increase then snap back to a gap" flicker). So: only ever
    // touch body height on iOS; let CSS own it otherwise.
    // Must live above TermSize: keybar init calls TermSize.schedule() during
    // parse, and a const IS_IOS below that call is a TDZ error that aborts
    // the rest of the page (Connect never runs).
    const IS_IOS = /iP(hone|ad|od)/.test(navigator.userAgent) ||
      (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
    let lastAppliedH = -1;
    function applyViewportHeight() {
      // One job on iOS: pin <body> height to the visual viewport so the
      // overlay keyboard does not cover the keybar. Do not translate the
      // body to chase visualViewport.offsetTop — that is a control loop
      // against Chrome/Safari's own pan (stable HUD, infinite scroll).
      // Do not scrollTo from here either: window.scroll fires this path
      // and a reset-that-fires-scroll is the same loop.
      if (!IS_IOS) {
        if (lastAppliedH !== 0) { document.body.style.height = ''; lastAppliedH = 0; }
        if (document.body.style.transform) document.body.style.transform = '';
        return;
      }
      const vv = window.visualViewport;
      if (!vv) return;
      const h = Math.round(vv.height);
      if (h !== lastAppliedH) { document.body.style.height = h + 'px'; lastAppliedH = h; }
    }

    // Size-pipeline trace. ?sizelog=1 or Settings → Size debug. Shows wrap
    // → send → PTY broadcast → paint so a phone can tell whether the hole
    // is the client's measure, the agent's min, or the local grid.
    const sizeEvents = [];
    function sizeTrace(kind, c, r) {
      const line = kind + ' ' + c + 'x' + r;
      sizeEvents.push({ t: Date.now(), kind, c, r });
      if (sizeEvents.length > 24) sizeEvents.shift();
      SizeLog.trace(line);
    }
    window.__size = function () {
      let snap = {};
      try { snap = TermSize.snapshot() || {}; } catch (_) {}
      return {
        viewer: viewerId,
        vv: window.visualViewport ? {
          w: Math.round(visualViewport.width),
          h: Math.round(visualViewport.height),
          top: Math.round(visualViewport.offsetTop),
          left: Math.round(visualViewport.offsetLeft),
        } : null,
        inner: { w: window.innerWidth, h: window.innerHeight },
        grid: { c: term.cols, r: term.rows },
        snap,
        events: sizeEvents.slice(),
      };
    };

    // Whether this tab's terminal is in sight: the tab visible and nothing
    // covering it. A view that covers the terminal says so with
    // TermSight.cover(name, true/false). The agent is told on the resize
    // report (away), so a terminal that is open but not being looked at —
    // behind another view, or in a background tab — is known as that.
    const TermSight = (() => {
      const covers = new Set();
      const onChange = [];
      let last = null;
      const inSight = () => !document.hidden && covers.size === 0;
      function changed() {
        const now = inSight();
        if (now === last) return;
        last = now;
        for (const f of onChange) { try { f(now); } catch (_) {} }
      }
      document.addEventListener('visibilitychange', changed);
      return {
        inSight,
        cover(name, on) { if (on) covers.add(name); else covers.delete(name); changed(); },
        onChange(f) { onChange.push(f); },
      };
    })();

    // TermSize — the only size loop on this page.
    //
    // This tab reports the wrap it can show. The agent owns the PTY
    // (min of every viewer's wrap, including keyboard-open) and
    // broadcasts the applied size. We render min(wrap, PTY): we
    // never grow past the PTY, so a laptop cannot stomp a phone. A short
    // grid hugs the canvas (flex-end) so a keyboard-open cannot leave
    // a black hole under the prompt. xterm.js resize is top-anchored,
    // so after every grid change we pin to the live bottom if the user
    // was already there.
    const TermSize = (() => {
      const MIN_C = 20, MIN_R = 8, SETTLE_MS = 200;
      let timer = 0, lastW = 0, lastH = 0;
      // What was last told, and to whom: the session key it went under.
      // Another key is another agent (a switch, warm or fresh, or a new
      // handshake), which has been told nothing yet — told at once, even
      // when this screen's size is the one the last session was told.
      let lastReportC = 0, lastReportR = 0, lastReportAway = null, lastReportKey = null;
      // The PTY an agent said it has, under its key: another session's is
      // no limit on this one's grid.
      let ptyC = 0, ptyR = 0, ptyKey = null;

      function atLiveBottom() {
        try {
          const buf = term.buffer.active;
          return !buf || buf.viewportY >= buf.baseY;
        } catch (_) { return true; }
      }
      function pinLive(wasLive) {
        const pin = () => {
          try { term.scrollToBottom(); } catch (_) {}
          if (typeof updateJumpBtn === 'function') updateJumpBtn();
        };
        if (wasLive) {
          pin();
          requestAnimationFrame(pin);
        } else if (typeof updateJumpBtn === 'function') updateJumpBtn();
      }
      function cellSize() {
        try {
          const cell = term._core && term._core._renderService &&
            term._core._renderService.dimensions &&
            term._core._renderService.dimensions.css &&
            term._core._renderService.dimensions.css.cell;
          if (cell && cell.width > 0 && cell.height > 0) return cell;
        } catch (_) {}
        return null;
      }
      // peek wrap; never commit lastW. Debug HUD and anything else that
      // only wants to READ geometry must use this. Committing lastW here
      // would hide a real wrap change from the next boxDrift poll.
      function peekWrap(wrapEl) {
        if (!wrapEl) return null;
        const cell = cellSize();
        if (!cell) return null;
        try {
          const st = getComputedStyle(wrapEl);
          const padX = (parseFloat(st.paddingLeft) || 0) + (parseFloat(st.paddingRight) || 0);
          const padY = (parseFloat(st.paddingTop) || 0) + (parseFloat(st.paddingBottom) || 0);
          const cols = Math.floor((wrapEl.clientWidth - padX) / cell.width);
          const rows = Math.floor((wrapEl.clientHeight - padY) / cell.height);
          if (cols >= MIN_C && rows >= MIN_R) return { cols, rows };
        } catch (_) {}
        return null;
      }
      function measure() {
        applyViewportHeight();
        const wrapEl = document.getElementById('terminal-wrap');
        if (wrapEl) { lastW = wrapEl.clientWidth; lastH = wrapEl.clientHeight; }
        // Always measure the WRAP, never #terminal / the canvas. A short
        // PTY grid inside a tall wrap would otherwise report itself and
        // stay stuck at 8 rows while the keyboard-open box is ~20.
        // No FitAddon fallback: proposeDimensions measures the canvas,
        // which is min(wrap, PTY). Reporting that as the wrap feeds the
        // painted grid back into the min — never report the canvas as the wrap.
        return peekWrap(wrapEl);
      }
      function pullScrollbackToBottom(term) {
        // Same arithmetic as the agent's growAnchoredBottom: if the grid
        // grew and xterm left blank rows under the cursor while history
        // sits above, pull that history onto the screen so the bottom
        // does not move. xterm.resize only does this when there are
        // already no empty rows below the cursor; a column change
        // reflow, or a cursor that is not on the last row, skips it
        // and the prompt sits in a void — the keyboard-collapse case.
        let b, lines;
        try {
          b = term._core.buffer;
          lines = b && b.lines;
        } catch (_) { return; }
        if (!b || !lines || typeof lines.get !== 'function') return;
        const rows = term.rows;
        const ybase = b.ybase | 0;
        const cursorY = b.y | 0;
        if (ybase <= 0) return;
        function trimmed(abs) {
          try {
            const line = lines.get(abs);
            if (!line || typeof line.getTrimmedLength !== 'function') return 0;
            return line.getTrimmedLength();
          } catch (_) { return 0; }
        }
        let lastUsed = cursorY;
        for (let y = rows - 1; y > cursorY; y--) {
          if (trimmed(ybase + y) > 0) { lastUsed = y; break; }
        }
        const pull = Math.min(rows - 1 - lastUsed, ybase);
        if (pull <= 0) return;
        let popped = 0;
        while (popped < pull && lines.length > ybase + cursorY + 1) {
          if (trimmed(lines.length - 1) > 0) break;
          if (typeof lines.pop !== 'function') break;
          lines.pop();
          popped++;
        }
        if (popped <= 0) return;
        const n = Math.min(popped, b.ybase | 0);
        b.ybase -= n;
        b.y = Math.min((b.y | 0) + n, rows - 1);
        if ((b.ydisp | 0) > b.ybase) b.ydisp = b.ybase;
        try { term.refresh(0, rows - 1); } catch (_) {}
      }
      function resizeAnchoredBottom(cols, rows) {
        const oldRows = term.rows;
        const alt = term.buffer.active !== term.buffer.normal;
        try { term.resize(cols, rows); } catch (_) { return; }
        if (alt || rows <= oldRows) return;
        pullScrollbackToBottom(term);
      }
      function paint(cols, rows) {
        if (cols < MIN_C || rows < MIN_R) return;
        const shrinking = rows < term.rows;
        // A keyboard-open shrink must show the prompt, even if the user
        // had scrolled up — they opened the keyboard to type.
        const wasLive = shrinking || atLiveBottom();
        const changed = term.cols !== cols || term.rows !== rows;
        if (changed && predictions.length > 0) resetPredictionsSoft();
        if (changed) {
          resizeAnchoredBottom(cols, rows);
        }
        try {
          const cell = cellSize();
          if (term.element && cell) {
            term.element.style.height = (term.rows * cell.height) + 'px';
            term.element.style.flexGrow = '0';
            term.element.style.flexShrink = '0';
            term.element.style.marginTop = 'auto';
          }
        } catch (_) {}
        // Pin only when the grid actually changed. scrollToBottom on every
        // paint (the 300ms poll, a visualViewport pan) is itself a scroll
        // loop — the very lines the grow needs stay put unless rows moved.
        if (changed) pinLive(wasLive);
      }
      function display(wrap) {
        if (!ownsTerminal) return;
        if (!wrap) wrap = measure();
        if (!wrap) return;
        let cols = wrap.cols, rows = wrap.rows;
        if (ptyC >= MIN_C && ptyR >= MIN_R && ptyKey === cryptoKey) {
          cols = Math.min(cols, ptyC);
          // Match the visible wrap, including keyboard-open. A tall PTY
          // grid CSS-clipped into a short wrap left a black hole under
          // the prompt and Chrome panned the page to chase the cursor.
          rows = Math.min(rows, ptyR);
        }
        paint(cols, rows);
      }
      // parked: a warm socket being unparked, whose agent is not live to
      // this page yet but takes the resize behind the unpark (resumeWarm).
      function report(wrap, parked) {
        // Not before the session key: send() drops a resize it cannot
        // encrypt, and a report remembered as sent is never sent again.
        if (!ownsTerminal || (!agentLive && !parked) || !cryptoKey) return null;
        if (!wrap) wrap = measure();
        if (!wrap) return null;
        const away = !TermSight.inSight();
        if (cryptoKey === lastReportKey && wrap.cols === lastReportC && wrap.rows === lastReportR && away === lastReportAway) return null;
        lastReportC = wrap.cols;
        lastReportR = wrap.rows;
        lastReportAway = away;
        lastReportKey = cryptoKey;
        sizeTrace('send', wrap.cols, wrap.rows);
        return send({ type: 'resize', cols: wrap.cols, rows: wrap.rows, away });
      }
      function sync() {
        const wrap = measure();
        display(wrap);
        report(wrap);
      }

      return {
        sync,
        // A warm socket's agent told this screen's size now, ahead of the
        // screen asked for behind it: the promise of the send, or null.
        reportUnparking() { const wrap = measure(); display(wrap); return report(wrap, true); },
        schedule() {
          applyViewportHeight();
          if (timer) clearTimeout(timer);
          timer = setTimeout(() => { timer = 0; sync(); }, SETTLE_MS);
        },
        adoptPty(c, r) {
          if (c === 0 && r === 0) {
            lastReportC = lastReportR = 0;
            lastReportAway = null;
            ptyC = ptyR = 0;
            sync();
            return;
          }
          if (c < MIN_C || r < MIN_R) return;
          ptyC = c;
          ptyR = r;
          ptyKey = cryptoKey;
          display();
        },
        boxDrift(el) {
          return !!(el && (el.clientWidth !== lastW || el.clientHeight !== lastH));
        },
        snapshot() {
          const wrapEl = document.getElementById('terminal-wrap');
          const wrap = peekWrap(wrapEl);
          return {
            measC: wrap ? wrap.cols : 0,
            measR: wrap ? wrap.rows : 0,
            sentC: lastReportC,
            sentR: lastReportR,
            ptyC,
            ptyR,
            wrapW: wrapEl ? wrapEl.clientWidth : 0,
            wrapH: wrapEl ? wrapEl.clientHeight : 0,
          };
        },
      };
    })();
    // reportSizeNow sends this screen's size the moment the session key
    // exists — the first moment a resize can be sent at all. The sync at
    // page load ran before that and could report nothing, and after it a
    // report went out only when the box moved. On a phone nothing moves
    // until the keyboard opens, so the PTY stayed at another screen's
    // width and the frame came out garbled until then.
    function reportSizeNow() { try { TermSize.sync(); } catch (_) {} }
    // Coming into or going out of sight is reported like a size change.
    TermSight.onChange(() => reportSizeNow());
    function sendResize() { TermSize.sync(); }
    function publishSize() { TermSize.sync(); }
    function scheduleSize() { TermSize.schedule(); }
    function fitTerminal() { TermSize.sync(); }
    function scheduleFit() { TermSize.schedule(); }
    function relayout() { TermSize.schedule(); }

    // Size debug HUD. Reads TermSize.snapshot() only — never measure(),
    // never report(), never lastW. Dragging the handle moves the overlay
    // and does not schedule a wrap.
    const SizeLog = (() => {
      const KEY = 'reminal:sizelog';
      const POS_KEY = 'reminal:sizelog-pos';
      const lines = [];
      let on = false;
      try { on = params.has('sizelog') || localStorage.getItem(KEY) === '1'; } catch (_) {}
      let box = null, body = null, timer = 0;

      function paint(line) {
        if (!body) return;
        if (line) {
          lines.push((performance.now() / 1000 % 1000).toFixed(1) + ' ' + line);
          while (lines.length > 40) lines.shift();
        }
        const y = body.scrollTop;
        let s = window.__size();
        let cell = '-';
        try {
          const c = term._core._renderService.dimensions.css.cell;
          cell = c.width.toFixed(1) + 'x' + c.height.toFixed(1);
        } catch (_) {}
        const meas = s.snap.measC ? s.snap.measC + 'x' + s.snap.measR : '-';
        const sent = s.snap.sentC ? s.snap.sentC + 'x' + s.snap.sentR : '-';
        const pty = s.snap.ptyC ? s.snap.ptyC + 'x' + s.snap.ptyR : '-';
        const paintC = Math.min(s.snap.measC || 0, s.snap.ptyC || s.snap.measC || 0);
        const paintR = Math.min(s.snap.measR || 0, s.snap.ptyR || s.snap.measR || 0);
        const grid = (paintC && paintR) ? paintC + 'x' + paintR : '-';
        let hint = '';
        if (s.snap.measC && s.snap.ptyC && (s.snap.ptyC < s.snap.measC || s.snap.ptyR < s.snap.measR)) {
          hint = 'CLAMP: agent min (another viewer wrap) < this wrap';
        } else if (s.snap.measR && s.snap.wrapH && cell !== '-') {
          hint = 'client wrap is what we send; pty is what the source applied';
        }
        body.textContent = [
          'SIZE  viewer ' + s.viewer,
          'vv    ' + (s.vv ? s.vv.w + 'x' + s.vv.h + ' @' + s.vv.top : '-'),
          'inner ' + s.inner.w + 'x' + s.inner.h,
          'wrap  ' + (s.snap.wrapW || 0) + 'x' + (s.snap.wrapH || 0) + ' px',
          'cell  ' + cell,
          'meas  ' + meas + '   ← send',
          'sent  ' + sent,
          'pty   ' + pty + '   ← agent',
          'paint ' + grid + '   min(meas,pty)',
          'grid  ' + s.grid.c + 'x' + s.grid.r,
          hint,
          '---',
          lines.join('\n') || '(open/close keyboard)',
        ].join('\n');
        body.scrollTop = y;
      }
      function clamp(el, x, y) {
        const w = el.offsetWidth || 160;
        const maxX = Math.max(0, window.innerWidth - w);
        const maxY = Math.max(0, window.innerHeight - 36);
        return {
          x: Math.min(maxX, Math.max(0, x)),
          y: Math.min(maxY, Math.max(0, y)),
        };
      }
      function place(el, x, y) {
        const p = clamp(el, x, y);
        el.style.left = p.x + 'px';
        el.style.top = p.y + 'px';
        el.style.right = 'auto';
        el.style.bottom = 'auto';
        return p;
      }
      function savePos(p) {
        try { localStorage.setItem(POS_KEY, JSON.stringify(p)); } catch (_) {}
      }
      function loadPos() {
        try {
          const v = JSON.parse(localStorage.getItem(POS_KEY) || 'null');
          if (v && Number.isFinite(v.x) && Number.isFinite(v.y)) return v;
        } catch (_) {}
        return null;
      }
      function bindDrag(handle, el) {
        let drag = null;
        handle.addEventListener('pointerdown', (e) => {
          if (e.button !== 0) return;
          e.stopPropagation();
          handle.setPointerCapture(e.pointerId);
          const r = el.getBoundingClientRect();
          drag = { dx: e.clientX - r.left, dy: e.clientY - r.top };
          handle.classList.add('dragging');
        });
        handle.addEventListener('pointermove', (e) => {
          if (!drag) return;
          e.preventDefault();
          e.stopPropagation();
          place(el, e.clientX - drag.dx, e.clientY - drag.dy);
        });
        const end = (e) => {
          if (!drag) return;
          handle.classList.remove('dragging');
          const r = el.getBoundingClientRect();
          savePos(place(el, r.left, r.top));
          drag = null;
          if (e) e.stopPropagation();
        };
        handle.addEventListener('pointerup', end);
        handle.addEventListener('pointercancel', end);
      }
      function mount() {
        if (box) return;
        box = document.createElement('div');
        box.id = 'sizelog';
        const handle = document.createElement('div');
        handle.className = 'sizelog-handle';
        handle.textContent = 'SIZE  ·  drag';
        body = document.createElement('div');
        body.className = 'sizelog-body';
        box.appendChild(handle);
        box.appendChild(body);
        box.addEventListener('touchstart', (e) => e.stopPropagation(), { passive: true });
        box.addEventListener('touchmove', (e) => e.stopPropagation(), { passive: true });
        box.addEventListener('wheel', (e) => e.stopPropagation(), { passive: true });
        document.body.appendChild(box);
        bindDrag(handle, box);
        paint();
        const saved = loadPos();
        if (saved) place(box, saved.x, saved.y);
        timer = setInterval(() => paint(), 400);
      }
      function unmount() {
        if (timer) { clearInterval(timer); timer = 0; }
        if (box) { box.remove(); box = null; }
        body = null;
      }
      function set(v) {
        on = !!v;
        try { localStorage.setItem(KEY, on ? '1' : '0'); } catch (_) {}
        if (on) mount(); else unmount();
        const sb = document.getElementById('settings-btn');
        if (sb) sb.classList.toggle('on', on);
      }
      if (on) set(true);
      return { set, isOn: () => on, trace: paint };
    })();
    window.__sizeLogPaint = SizeLog.trace;

    (function initSettings() {
      const dock = document.getElementById('settdock');
      const btn = document.getElementById('settings-btn');
      if (!dock || !btn) return;
      btn.classList.toggle('on', SizeLog.isOn());
      function render() {
        dock.innerHTML = '';
        const row = document.createElement('button');
        row.type = 'button';
        row.className = 'sett-row' + (SizeLog.isOn() ? ' on' : '');
        row.innerHTML = '<span>Size debug</span><span class="sett-sw">' + (SizeLog.isOn() ? 'On' : 'Off') + '</span>';
        row.addEventListener('click', (e) => {
          e.stopPropagation();
          SizeLog.set(!SizeLog.isOn());
          render();
        });
        dock.appendChild(row);
        const tour = document.createElement('button');
        tour.type = 'button';
        tour.className = 'sett-row';
        tour.innerHTML = '<span>Take the tour</span><span class="sett-sw"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12h14M13 6l6 6-6 6"/></svg></span>';
        tour.addEventListener('click', (e) => {
          e.stopPropagation();
          close();
          if (window.reminalTour) window.reminalTour.start();
        });
        dock.appendChild(tour);
      }
      function position() {
        if (!dock.classList.contains('open')) return;
        const kb = document.getElementById('keybar');
        const kbTop = kb ? kb.getBoundingClientRect().top : window.innerHeight;
        const r = btn.getBoundingClientRect();
        dock.style.bottom = (window.innerHeight - kbTop + 8) + 'px';
        dock.style.left = Math.max(8, Math.min(r.left, window.innerWidth - 220)) + 'px';
        dock.style.right = 'auto';
        dock.style.top = 'auto';
      }
      function close() { dock.classList.remove('open'); }
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        if (dock.classList.contains('open')) { close(); return; }
        render();
        dock.classList.add('open');
        position();
      });
      document.addEventListener('click', (e) => {
        if (!dock.classList.contains('open')) return;
        if (dock.contains(e.target) || e.target === btn) return;
        close();
      });
      window.addEventListener('resize', position);
      if (window.visualViewport) visualViewport.addEventListener('resize', position);
    })();

    // The resume goes right behind the handshake instead of after its answer:
    // the agent answers a handshake before it reads the next frame, so its
    // answer — the session key — is always ahead of the screen the resume
    // asks for, and the screen comes a round trip sooner. A resume carries no
    // secret (it is unsealed, as ever: a seq and this viewer's id), and the
    // frames that come back are sealed under the key the answer brings; they
    // wait for it (keyComing), never the other way round.
    // One per connection, with the first attempt: { sock, exID }.
    let earlyResume = null;
    let keyExID = ''; // the attempt whose answer gave this connection its key
    let droppedBeforeKey = null; // the connection a sealed frame came on before its key
    function sendEarlyResume(sock, exID) {
      if (sock !== ws || !ownsTerminal || (earlyResume && earlyResume.sock === sock)) return;
      sendResume();
      earlyResume = { sock, exID };
    }
    // A handshake answer being worked on (the key derived, unwrapped): the
    // sealed frames behind it on the same connection wait for it.
    let keyComing = null; // { sock, done }
    function sendResume() {
      // Resume rewinds the agent's output cursor, and the agent holds ONE
      // socket to the relay which fans out — so a pane tab asking to resume
      // from seq 0 replays the entire scrollback to EVERY viewer, not just
      // itself, and re-renders the terminal of whoever was already watching.
      // A tab that never shows the terminal has no business moving that
      // cursor.
      if (!ownsTerminal) return;
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      // A snapshot owed to this viewer may come whole, compressed and in
      // parts addressed to it (snapshot_part), when it can unpack them.
      // snap_ack: the screen first, the history paced behind live output.
      const snap = snapPartsOK() ? { viewer: viewerId, snap: 'gz+parts', snap_ack: true } : {};
      ws.send(JSON.stringify({ type: 'resume', from_seq: lastSeq, ...snap }));
    }

    // ---- A joining snapshot in parts (snapshot_part) ----
    // The plain snapshot is one frame, so its history stops at what fits a
    // relay message; in parts it is all of it. The parts are gathered, the
    // whole unpacked and painted ONCE; output that arrives meanwhile is held
    // and played after it (what the snapshot already shows is dropped). A
    // part that never comes is never a half paint: the parts are dropped and
    // the plain snapshot asked for instead.
    const SNAP_PARTS_WAIT_MS = 20000;
    // The most a snapshot may unpack to: the agent sends at most its history
    // cap (8 MiB rendered by default) and a screen. A small part can unpack
    // to a great deal more (gzip of zeros); past this it is not painted.
    const SNAP_MAX_BYTES = 16 << 20;
    // The most parts a paced snapshot may come in: 16 KiB of the gzip stream
    // each, so 16 MiB of it, more than SNAP_MAX_BYTES ever unpacks from.
    const SNAP_PACED_MAX_PARTS = 1024;
    let snapAsm = null;          // { id, n, seq, parts: [], got, timer, held: [{seq, bytes}] }
    let snapPartsOff = false;    // this connection fell back once: plain from here on
    function snapPartsOK() {
      return !snapPartsOff && typeof DecompressionStream === 'function';
    }
    // A paced snapshot (snap_ack): the screen comes alone, first, and is
    // painted at once; the history follows in parts, each acknowledged, so
    // the agent sends them between the live output instead of ahead of it.
    // Output meanwhile is played as it comes AND kept (past the snapshot's
    // seq), so that when the history is in it is painted once — history,
    // screen, and the kept output again on top. Nothing waits on the history:
    // if it never completes, the screen and its output simply stay.
    let snapLive = null;         // { id, seq, n, parts: [], got, held: [{seq, bytes}], heldBytes }
    async function gunzipParts(parts) {
      // Unpacked as it streams, counted: past SNAP_MAX_BYTES it stops.
      const reader = new Blob(parts).stream().pipeThrough(new DecompressionStream('gzip')).getReader();
      const chunks = [];
      let total = 0;
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        total += value.length;
        if (total > SNAP_MAX_BYTES) {
          try { await reader.cancel(); } catch (_) {}
          throw new Error('a snapshot unpacking to more than ' + SNAP_MAX_BYTES + ' bytes');
        }
        chunks.push(value);
      }
      const whole = new Uint8Array(total);
      let at = 0;
      for (const c of chunks) { whole.set(c, at); at += c.length; }
      return whole;
    }
    async function onPacedPart(msg, h, body) {
      if (h.screen) {
        // A new snapshot for this connection: whatever was under way is over.
        if (snapAsm) { clearTimeout(snapAsm.timer); snapAsm = null; }
        const seq = typeof msg.seq === 'number' ? msg.seq : 0;
        snapLive = h.more ? { id: h.id, seq, n: 0, parts: null, got: 0, held: [], heldBytes: 0 } : null;
        awaitingSnapshotUntil = 0;
        if (seq > lastSeq || lastSeq === 0) lastSeq = seq;
        enqueueData(body);
        bytesReceived += body.byteLength;
        return;
      }
      const a = snapLive;
      // Parts of a snapshot whose screen this viewer did not paint (or one
      // given up) are of no use: without the output kept since, the history
      // could not be painted under it.
      if (!a || a.id !== h.id || !(h.n > 0) || h.n > SNAP_PACED_MAX_PARTS || !(h.i >= 0 && h.i < h.n)) return;
      if (!a.parts) { a.n = h.n; a.parts = new Array(h.n); }
      if (a.n !== h.n || a.parts[h.i]) return;
      a.parts[h.i] = body;
      const last = ++a.got === a.n; // decided here: parts acknowledge concurrently
      // How many from the first are in: the agent sends the next as these come.
      let have = 0;
      while (have < a.n && a.parts[have]) have++;
      // Acknowledged on the socket the part came on (send()): sealed for
      // this session, so never on the next one's after a switch.
      const sock = ws;
      try {
        const ack = await encrypt(cryptoKey, JSON.stringify({ viewer: viewerId, snap: a.id, seq: have }));
        if (sock && ws === sock && sock.readyState === WebSocket.OPEN) sock.send(JSON.stringify({ type: 'snapshot_ack', data: ack }));
      } catch (_) {}
      if (!last) return;
      let whole;
      try { whole = await gunzipParts(a.parts); } catch (_) {
        if (snapLive === a) snapLive = null; // the screen stays, live
        return;
      }
      if (snapLive !== a) return; // a newer snapshot meanwhile
      snapLive = null;
      // Painted once, in one write with the kept output (xterm draws a write
      // only once it is through, so the repaint is never seen half done), at
      // the same distance from the bottom (flushPendingChunks); and not
      // matched against typing predictions: the kept output was matched when
      // it first came, and what is predicted now is wiped with the rest (its
      // echo, still to come, paints it).
      resetPredictionsHard();
      // The terminal first put back as a snapshot assumes it: the snapshot
      // clears the screen and the scrollback but not the modes, and output
      // during the history's download may have set some — a scroll region
      // (ESC[3;10r) kept the whole history inside its rows, so all but a few
      // lines of it went. A soft reset and the full region; the kept output
      // after it sets again whatever the program set.
      pendingChunks.push(new TextEncoder().encode('\x1b[!p\x1b[r'));
      pendingChunks.push(whole);
      bytesReceived += whole.byteLength;
      for (const f of a.held) if (f.seq > a.seq) pendingChunks.push(f.bytes);
      flushPendingChunks();
    }
    // Output kept for a paced snapshot's repaint; past SNAP_MAX_BYTES of it
    // the history is given up (the screen stays, live).
    function keepForSnapshot(seq, bytes) {
      const a = snapLive;
      if (!a || !(seq > a.seq)) return;
      a.held.push({ seq, bytes });
      a.heldBytes += bytes.byteLength;
      if (a.heldBytes > SNAP_MAX_BYTES) snapLive = null;
    }
    async function onSnapshotPart(msg) {
      if (!cryptoKey) return;
      let bytes;
      try { bytes = await decrypt(cryptoKey, msg.data); } catch (_) { return; }
      if (bytes.length < 4) return;
      const hl = new DataView(bytes.buffer, bytes.byteOffset, 4).getUint32(0);
      let h;
      try { h = JSON.parse(new TextDecoder().decode(bytes.subarray(4, 4 + hl))); } catch (_) { return; }
      // Addressed inside the part (a sealed frame carries no address): parts
      // for another viewer are not this one's to paint.
      if (h && h.to === viewerId && typeof h.id === 'string' && (h.screen || h.ack)) {
        await onPacedPart(msg, h, bytes.subarray(4 + hl));
        return;
      }
      if (!h || h.to !== viewerId || typeof h.id !== 'string' || !(h.n > 0) || !(h.i >= 0 && h.i < h.n) || h.n > 64) return;
      // Caught up already (a snapshot no longer owed): nothing to repaint.
      if (!snapAsm && typeof msg.seq === 'number' && msg.seq <= lastSeq && Date.now() >= awaitingSnapshotUntil) return;
      if (!snapAsm || snapAsm.id !== h.id) {
        if (snapAsm) clearTimeout(snapAsm.timer);
        snapAsm = { id: h.id, n: h.n, seq: msg.seq || 0, parts: new Array(h.n), got: 0, held: [] };
        snapAsm.timer = setTimeout(snapPartsGiveUp, SNAP_PARTS_WAIT_MS);
      }
      const a = snapAsm;
      if (a.parts[h.i]) return;
      a.parts[h.i] = bytes.subarray(4 + hl);
      if (++a.got < a.n) return;
      clearTimeout(a.timer);
      let whole;
      try {
        // Past SNAP_MAX_BYTES it is not painted (the plain snapshot is asked
        // for instead).
        whole = await gunzipParts(a.parts);
      } catch (_) {
        snapPartsGiveUp();
        return;
      }
      if (snapAsm !== a) return; // given up meanwhile
      snapAsm = null;
      awaitingSnapshotUntil = 0;
      if (a.seq > lastSeq || lastSeq === 0) lastSeq = a.seq;
      enqueueData(whole);
      bytesReceived += whole.byteLength;
      for (const f of a.held) {
        if (f.seq > a.seq) {
          if (f.seq > lastSeq) lastSeq = f.seq;
          enqueueData(f.bytes);
        }
      }
    }
    function snapPartsGiveUp() {
      if (!snapAsm) return;
      clearTimeout(snapAsm.timer);
      snapAsm = null;
      snapPartsOff = true;
      // The plain snapshot instead: a resume from nothing, without parts.
      lastSeq = 0;
      awaitingSnapshotUntil = Date.now() + 15000;
      sendResume();
    }

    // Snapshots (resume from seq 0) wipe the viewer with ESC[3J and then
    // paint the entire buffer. Cursor CLI / Ink conversations can be tens
    // of thousands of lines; doing that on a timer after every burst of
    // output is the "redraw from the top for 20 seconds" stall. Snapshots
    // are for join/reconnect only — sendResume() on connect. Do not
    // auto-refresh while live.
    let lastDataAt = 0, cleanBaseY = 0;
    function scheduleSoftRefresh() {
      // Kept as a no-op so older call sites cannot resurrect the stall.
    }

    function cancelReconnect() {
      if (reconnectTimer !== null) {
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
    }

    function cancelDial() {
      if (dialTimer !== null) {
        clearTimeout(dialTimer);
        dialTimer = null;
      }
    }

    // Streams must be re-armed after the NEXT successful (re)connect: the
    // visibility-resume path can fire while the socket is still down (its
    // start commands drop silently in sendWin), and the agent idle-stops any
    // stream whose viewer vanished — so without this, panes come back frozen
    // (inputs flow, frames don't) until closed and reopened by hand.
    let winStreamsNeedRestart = false;

    // Waking the page (or the network coming back) shouldn't sit out a 30s
    // backoff that grew while every throttled background retry failed — dial
    // immediately and let the normal machinery take over.
    // ---- A socket that says it is open but has died ----
    // A network change (wifi to cell, a new address) leaves the old socket
    // "open" with nothing behind it: no close ever comes, and what is typed
    // goes nowhere. So when the network comes back, the page is shown again,
    // or a key gets nothing back, the socket is asked — a ping — and given
    // the round trip it has been taking, twice, and a second; silent past
    // that, it is dropped and the session dialled again at once. Keys typed
    // from then on wait for the new connection (held, as when connecting);
    // those already sent into the dead one are counted on screen — never
    // shown, they may have been a password — so nothing is lost unsaid.
    let probe = null;     // { sock, timer }: a ping out, waiting for anything back
    let echoWatch = 0;    // a key sent, nothing back yet
    let unechoed = 0;     // characters sent since anything last came back
    function probeDeadline() { return Math.max(1500, 2 * (observedRTT || 0) + 1000); }
    function noteInbound(sock) {
      if (sock !== ws) return;
      unechoed = 0;
      if (echoWatch) { clearTimeout(echoWatch); echoWatch = 0; }
      if (probe) { clearTimeout(probe.timer); probe = null; }
    }
    function probeSocket(why) {
      if (!ws || ws.readyState !== WebSocket.OPEN || probe || manualClose || fatal) return;
      const sock = ws;
      try { sock.send(JSON.stringify({ type: 'ping' })); } catch (_) {}
      probe = { sock, timer: setTimeout(() => { probe = null; if (ws === sock) socketDead(why); }, probeDeadline()) };
    }
    function noteKeySent(n) {
      unechoed += n;
      if (!echoWatch && !probe) echoWatch = setTimeout(() => { echoWatch = 0; probeSocket('no echo'); }, probeDeadline());
    }
    function socketDead(why) {
      dbg('socket silent past ' + probeDeadline() + 'ms (' + why + ') — dialling again');
      if (echoWatch) { clearTimeout(echoWatch); echoWatch = 0; }
      const lost = unechoed;
      unechoed = 0;
      if (lost) viewerNote('The connection dropped: the last ' + lost + ' character' + (lost === 1 ? '' : 's') + ' you typed may not have reached the session.');
      const sock = ws;
      ws = null;
      try { sock.onopen = null; sock.onmessage = null; sock.onclose = null; sock.onerror = null; sock.close(); } catch (_) {}
      authed = false;
      agentLive = false;
      if (helloShown) { reconnectMessagePending = true; winStreamsNeedRestart = true; }
      backoffMs = 1000;
      cancelReconnect();
      openSocket();
    }
    function fastReconnect() {
      if (manualClose || fatal || !helloShown) return;
      if (ws && ws.readyState === WebSocket.OPEN) { probeSocket('back'); return; } // open, maybe only in name
      if (ws && ws.readyState === WebSocket.CONNECTING) return;
      backoffMs = 1000;
      cancelReconnect();
      openSocket();
    }
    window.addEventListener('online', fastReconnect);

    function scheduleReconnect() {
      cancelReconnect();
      if (manualClose || fatal) return;
      if (redialNow) {
        redialNow = false;
        setStatus('Reconnecting…', 'warn connecting');
        showOverlay('Reconnecting…');
        reconnectTimer = setTimeout(() => {
          reconnectTimer = null;
          openSocket();
        }, 0);
        return;
      }
      // Reset backoff if the previous session stayed up for a while.
      if (connectedAt && (Date.now() - connectedAt) > 30000) {
        backoffMs = 1000;
      }
      const delay = Math.min(backoffMs, MAX_BACKOFF_MS);
      const secs = Math.max(1, Math.round(delay / 1000));
      setStatus('Reconnecting in ' + secs + 's…', 'warn connecting');
      showOverlay('Reconnecting in ' + secs + 's…');
      reconnectTimer = setTimeout(() => {
        reconnectTimer = null;
        openSocket();
      }, delay);
      backoffMs = Math.min(backoffMs * 2, MAX_BACKOFF_MS);
    }

    // The last screen of each session this page has shown, put up the moment
    // a switch back to it starts, so there is something to look at while the
    // connection is made — dimmed, without its colours (#terminal.stale), and
    // replaced by the live screen when the first output arrives. Text only,
    // in this page's memory only: never stored, gone with the tab, and never
    // more than a few sessions' worth.
    const SCREEN_CACHE_MAX = 8;
    const screenCache = new Map(); // session id → [row text, …]
    let screenCachedFor = ''; // the session whose screen was taken, before anything was written over it
    let staleScreen = false;  // a remembered screen is up, not the live one
    function cacheScreen() {
      if (!sessionId || !cryptoKey || lastSeq === 0 || staleScreen) return; // nothing live to remember
      const b = term.buffer.active;
      const rows = [];
      for (let y = b.baseY; y < b.baseY + term.rows; y++) {
        const line = b.getLine(y);
        rows.push(line ? line.translateToString(true) : '');
      }
      screenCache.delete(sessionId);
      screenCache.set(sessionId, rows);
      while (screenCache.size > SCREEN_CACHE_MAX) screenCache.delete(screenCache.keys().next().value);
      screenCachedFor = sessionId;
    }
    // A session this page can no longer open, or no longer should show, has
    // no screen to remember: forgotten, its connect failed or ended before it
    // was live, its machine forgotten, this browser's owner key new, the page
    // going away. Taken down at once if it is the one up.
    function forgetCachedScreen(id) {
      if (id) screenCache.delete(id); else screenCache.clear();
      if (staleScreen && (!id || id === sessionId)) {
        staleScreen = false;
        term.write('\x1b[H\x1b[2J\x1b[3J');
        document.getElementById('terminal').classList.remove('stale');
      }
    }
    function showCachedScreen(id) {
      const rows = screenCache.get(id);
      if (!rows) return;
      // Cleared first: writes queued before the reset (the "Disconnected"
      // line) land after it.
      term.write('\x1b[H\x1b[2J\x1b[3J\x1b[2m' + rows.slice(0, term.rows).map(r => r.slice(0, term.cols)).join('\r\n') + '\x1b[0m\x1b[H');
      staleScreen = true;
      document.getElementById('terminal').classList.add('stale');
    }
    // Live output takes it away, without a trace in the scrollback: see
    // flushPendingChunks.
    // ---- Warm sockets ----
    // A switch away from a live session parks its socket instead of closing
    // it (the relay then sends it nothing and does not count it as a viewer),
    // and a switch back unparks it: no connect, no handshake, just the screen.
    // At most WARM_MAX, the most recent; each keeps its own session key and
    // sealing state, in this page's memory only, dropped with the socket.
    // Closed when the page goes, after a minute hidden, when the session is
    // forgotten — and by the relay when the agent leaves or comes back. One
    // that does not answer, is refused, or whose frames do not open is
    // dropped and the session dialled afresh; never retried.
    const WARM_MAX = 3;
    const warmSockets = new Map(); // session id → { sid, ws, cryptoKey, sealState, iOwn, parked, lastPingAt }
    let unparkTimer = 0;
    let unparkProbe = null; // a socket just unparked, until a frame on it opens
    function closeWarm(e) {
      if (warmSockets.get(e.sid) === e) warmSockets.delete(e.sid);
      const sock = e.ws;
      e.ws = null; e.cryptoKey = null; e.sealState = null;
      if (sock) {
        sock.onmessage = null; sock.onclose = null; sock.onerror = null;
        try { sock.close(1000, 'warm socket closed'); } catch (_) {}
      }
    }
    function closeAllWarm() { for (const e of [...warmSockets.values()]) closeWarm(e); }
    function parkCurrent() {
      if (!ws || ws.readyState !== WebSocket.OPEN || !cryptoKey || !agentLive || !sessionId || !ownsTerminal) return false;
      const sock = ws;
      const e = { sid: sessionId, ws: sock, cryptoKey, sealState, iOwn: iOwnThisMachine, parked: false, lastPingAt: Date.now() };
      if (!sock.sendRaw) return false;
      sock.sendRaw(JSON.stringify({ type: 'park' }));
      const old = warmSockets.get(e.sid);
      if (old) closeWarm(old);
      sock.onopen = null;
      sock.onerror = null;
      sock.onmessage = (ev) => {
        let m;
        try { m = JSON.parse(ev.data); } catch { return; }
        if (m.type === 'parked') e.parked = true;
        else if (m.type === 'park_refused') closeWarm(e);
        // Anything else — output that was on its way before the park — is
        // of no use: an unpark asks for the screen afresh.
      };
      sock.onclose = () => { if (warmSockets.get(e.sid) === e) warmSockets.delete(e.sid); e.ws = null; e.cryptoKey = null; e.sealState = null; };
      warmSockets.set(e.sid, e);
      ws = null;
      return true; // trimmed to WARM_MAX once the session switched to is out (trimWarm)
    }
    // Past WARM_MAX the least recently used go. Run after the switch has
    // taken its own: trimmed before, the session being switched to could be
    // the one dropped.
    function trimWarm() {
      while (warmSockets.size > WARM_MAX) closeWarm(warmSockets.values().next().value);
    }
    // resumeWarm takes session s's parked socket for this page's connection,
    // if it has one that is still open, and unparks it.
    function resumeWarm(s) {
      const e = warmSockets.get(s);
      if (!e) return false;
      warmSockets.delete(s);
      if (!e.parked || !e.ws || e.ws.readyState !== WebSocket.OPEN) { closeWarm(e); return false; }
      cancelReconnect();
      cancelDial();
      if (ws) {
        try { ws.onopen = null; ws.onmessage = null; ws.onclose = null; ws.onerror = null; ws.close(); } catch (_) {}
      }
      ws = e.ws;
      cryptoKey = e.cryptoKey;
      sealState = e.sealState;
      // A probe or a key's wait belongs to the socket it was for (as in openSocket).
      if (probe) { clearTimeout(probe.timer); probe = null; }
      if (echoWatch) { clearTimeout(echoWatch); echoWatch = 0; }
      unechoed = 0;
      authed = true;
      agentLive = false; // until the relay says it is a viewer again
      if (snapAsm) { clearTimeout(snapAsm.timer); snapAsm = null; }
      snapLive = null;
      snapPartsOff = false;
      kexAttempts.clear(); kexExID = ''; kexInFlight = false;
      ownerAttempts.clear();
      if (ownerTimer) { clearTimeout(ownerTimer); ownerTimer = 0; }
      handshakeSock = ws; // its handshake was done long ago
      earlyResume = null;
      keyExID = 'warm';
      iOwnThisMachine = e.iOwn;
      teardownWebRTC();
      rtcRetryAt = 0;
      setStatus('Connecting…', 'warn connecting');
      showOverlay('Connecting…');
      wireSocket(ws);
      const sock = ws;
      sock.sendRaw(JSON.stringify({ type: 'unpark' }));
      // This screen's size right behind it, and the screen asked for behind
      // that, as behind a handshake: the relay takes them in order, and
      // drops what comes from a socket it has not unparked (refused:
      // dialled afresh, which asks again). The size first, so the agent
      // sizes the PTY before it draws the screen: drawn at the size the
      // last viewer left it, a full-screen program filled the top of the
      // grid and left a band under it until the next resize.
      if (ownsTerminal) {
        earlyResume = { sock, exID: 'warm' };
        const sized = TermSize.reportUnparking();
        if (sized) sized.then(() => { if (ws === sock) sendResume(); }, () => { if (ws === sock) sendResume(); });
        else sendResume();
      }
      // Abort cap: a socket that died quietly while parked answers nothing.
      if (unparkTimer) clearTimeout(unparkTimer);
      unparkTimer = setTimeout(() => { unparkTimer = 0; if (ws === sock && !agentLive) warmFailed('no answer'); },
        Math.max(1500, 2 * (observedRTT || 0) + 1000));
      return true;
    }
    // The socket just unparked will not do: dial afresh.
    function warmFailed(why) {
      dbg('warm socket dropped (' + why + ') — dialling afresh');
      if (unparkTimer) { clearTimeout(unparkTimer); unparkTimer = 0; }
      unparkProbe = null;
      if (ws) {
        try { ws.onopen = null; ws.onmessage = null; ws.onclose = null; ws.onerror = null; ws.close(); } catch (_) {}
        ws = null;
      }
      openSocket();
    }
    async function startConnect() {
      const s = sessionInput.value.trim().toUpperCase();
      let p = pinInput.value.trim();
      // A PIN left over from the session we are leaving is not a credential
      // for this one. Drop it and take the no-PIN path, which is PIN-free for
      // an owner and says "PIN required" for everyone else — both of which
      // beat failing the handshake with a PIN that was never meant for it.
      if (p && !pinTyped && pinForSession && pinForSession !== s) {
        p = '';
        pinInput.value = '';
      }
      if (!s) { setStatus('Session required', 'error'); return; }
      if (s !== sessionId) settleUploadWaiters('left'); // an upload for the session being left is not sent to this one
      // The PIN handshake's library, fetched now rather than once auth_ok
      // asks for it: on a slow link that fetch was a round trip and more on
      // every first open. Fetching it does nothing else (loadRistretto: one
      // load, retried at its use if this one fails).
      loadRistretto().catch(() => {});
      // Settle where keys go BEFORE the first await below: a key typed while
      // loadDeviceKeyPair runs is held for the session we are joining — not
      // dropped later as "typed for the session being left", not left in the
      // form field that had focus, and not sent down the socket of a session
      // still open while we switch away from it (switchingTo holds it).
      const gen = ++connectGen;
      // Typed for the session being left — or held for one a switch was
      // under way to, when this connect is to another (back to the session
      // still on the page, say): never sent where it was not typed for.
      // Held while a switch to s itself was under way (it asked again), it
      // was typed for s: kept.
      if ((s !== sessionId && switchingTo !== s) || (switchingTo && switchingTo !== s)) {
        if (switchingTo && switchingTo !== s && (heldInput.length || heldOverflow)) {
          viewerNote('What you typed for ' + switchingTo + ' was not sent.');
        }
        dropHeldInput();
      }
      switchingTo = s !== sessionId ? s : '';
      term.focus();
      if (!p) {
        // PIN-free is only possible if this browser has an owner key to try;
        // otherwise a PIN is required.
        const dk = await loadDeviceKeyPair();
        if (gen !== connectGen) return; // a newer connect took over
        if (!dk) { abandonSwitch(); setStatus('PIN required', 'error'); pinInput.focus(); return; }
      } else if (!/^\d{6,8}$/.test(p)) {
        abandonSwitch(); setStatus('PIN must be 6–8 digits', 'error'); return;
      }
      switchingTo = '';

      manualClose = false;
      fatal = false;
      fatalWords = '';
      backoffMs = 1000;
      // Switching to a different session invalidates the replay cursor and
      // any previously rendered output; start the terminal clean.
      if (s !== sessionId) {
        // What the page shows of the machine it was on goes with it — another
        // machine's windows, apps or banners are never shown for this one —
        // unless the session it is going to is on that same machine.
        const sameMachine = onSameMachine(sessionId, s);
        if (screenCachedFor !== sessionId) cacheScreen();
        screenCachedFor = '';
        // A remembered screen of the session being left is not this one's.
        staleScreen = false;
        document.getElementById('terminal').classList.remove('stale');
        lastSeq = 0;
        sessionId = s;
        term.reset();
        showCachedScreen(s);
        // host_info describes the session we are LEAVING. Share falls back to
        // it for the PIN (see sharePin), so keeping it here handed out an
        // invite pairing the new session's id with the old session's PIN —
        // which cannot connect, and looks to whoever pastes it like the
        // machine hanging rather than a wrong credential.
        lastHostInfo = null;
        if (typeof forgetHostState === 'function') forgetHostState(sameMachine);
      }
      if (lastSeq === 0) awaitingSnapshotUntil = Date.now() + 15000;
      pinValue = p;
      pinForSession = p ? sessionId : '';
      pinTyped = false;
      // cryptoKey is established asynchronously by the EKE handshake
      // (see sendKexInit / completeKex) inside openSocket → ws.onmessage.
      cryptoKey = null;
      sealState = null;
      sessionInput.value = sessionId;
      history.replaceState(null, '', '?s=' + sessionId);
      if (!resumeWarm(sessionId)) openSocket();
      trimWarm();
      // Keys typed from now on are the terminal's — held until it is live
      // (sendInput) — not lost to whatever had focus before it was shown.
      // startSessionFlow focuses it again once connected, as it always has.
      term.focus();
    }

    function openSocket() {
      cancelReconnect();
      cancelDial();
      // A probe or a key's wait belongs to the socket it was for.
      if (probe) { clearTimeout(probe.timer); probe = null; }
      if (echoWatch) { clearTimeout(echoWatch); echoWatch = 0; }
      if (ws) {
        // Detach handlers BEFORE closing so any messages already in-flight
        // on the old socket don't fire our onmessage as they arrive —
        // otherwise two live sockets (the old one during its closing
        // grace period and the new one we're about to open) can both
        // deliver the same agent broadcast and we'd render every byte
        // twice.
        try {
          ws.onopen = null;
          ws.onmessage = null;
          ws.onclose = null;
          ws.onerror = null;
          ws.close();
        } catch (_) {}
        ws = null;
      }
      // Old socket's ping timer is moot now — its onclose was
      // nulled out above so its own cleanup won't fire. The next
      // ws.onopen rearms.
      authed = false;
      agentLive = false;
      // Every WS connection runs its own EKE handshake — the previous
      // connection's session key is invalid here (the agent may have
      // restarted, or it's simply a different K). Clear so we never
      // accidentally encrypt/decrypt with stale material.
      cryptoKey = null;
      sealState = null;
      // A snapshot in parts belongs to the socket it came on.
      if (snapAsm) { clearTimeout(snapAsm.timer); snapAsm = null; }
      snapLive = null;
      snapPartsOff = false;
      kexAttempts.clear();
      kexExID = '';
      kexInFlight = false;
      // Owner-handshake state resets the same way, so a reconnect never carries a
      // stale attempt or a live timeout from the previous socket.
      ownerAttempts.clear();
      if (ownerTimer) { clearTimeout(ownerTimer); ownerTimer = 0; }
      // Re-evaluate ownership for this session's machine: false until proven by an
      // owner connect, but prime it from the pin store so a machine we already own
      // hides the enroll offer immediately instead of flashing it.
      keyByPin = false;
      iOwnThisMachine = false;
      isOwnedMachine(sessionId).then(v => { if (v && !iOwnThisMachine && !keyByPin) { iOwnThisMachine = true; if (typeof renderHost === 'function') renderHost(); } }).catch(() => {});
      // A new socket means a fresh session key; tear down any peer connection so
      // it renegotiates cleanly (the agent may be a different process now). Clear
      // the cooldown so P2P is retried immediately — the network may have changed.
      teardownWebRTC();
      rtcRetryAt = 0;

      const fresh = lastSeq === 0;
      setStatus(fresh ? 'Connecting…' : 'Reconnecting…', 'warn connecting');
      showOverlay(fresh ? 'Connecting…' : 'Reconnecting…');
      setBtnBusy('Cancel');
      // Allow user to cancel during connect by re-purposing the button.
      connectBtn.disabled = false;
      connectBtn.classList.add('danger');
      connectBtn.textContent = 'Cancel';

      const dialStart = Date.now();
      pakeBusyTries = 0; // each connection gets its own few retries
      ws = new WebSocket(relayURL(sessionId));
      {
        const rawSend = ws.send.bind(ws);
        ws.send = (text) => sessionSend((t) => { try { rawSend(t); } catch (_) {} }, text);
        // Unsealed, straight out: what a warm socket sends (park, ping) is
        // never the page's session's to seal, nor held behind its queue.
        ws.sendRaw = (t) => { try { rawSend(t); } catch (_) {} };
      }
      const dialingWs = ws;
      cancelDial();
      dialTimer = setTimeout(() => {
        dialTimer = null;
        // Only a stalled pre-auth dial: once authed, the relay is talking to us
        // and agent_online/agent_offline drive the UI (including the legitimate
        // "agent offline — waiting…" hold, which must NOT be torn down here).
        if (ws !== dialingWs || authed || manualClose || fatal) return;
        dbg('dial watchdog: no auth within ' + DIAL_TIMEOUT_MS + 'ms → forcing reconnect');
        try {
          dialingWs.onopen = null;
          dialingWs.onmessage = null;
          dialingWs.onclose = null;
          dialingWs.onerror = null;
          dialingWs.close();
        } catch (_) {}
        ws = null;
        scheduleReconnect();
      }, DIAL_TIMEOUT_MS);

      ws.onopen = () => {
        // Stash for the one-time "Connected · handshake Xms" line below.
        lastDialMs = Date.now() - dialStart;
        dbg('ws OPEN (' + lastDialMs + 'ms) → sending auth');
        // Seed the predictive-echo round-trip estimate so the very first
        // burst of typing on a slow link uses an RTT-scaled stale window
        // instead of the 700ms floor (which would wipe in-flight dim chars
        // before their echo lands). Refined by real echo timings as soon
        // as the user starts typing.
        if (lastDialMs > 0 && observedRTT <= 0) observedRTT = lastDialMs;
        // NB: the PIN is deliberately NOT sent to the relay — it authenticates
        // end-to-end via the EKE (sendKexInit), so an untrusted relay never sees
        // it and can't MITM the handshake. The relay auths a viewer on the
        // session being live; a wrong PIN fails the EKE unwrap ("PIN mismatch").
        ws.send(JSON.stringify({ type: 'auth' }));
        // The handshake goes right behind auth instead of waiting for auth_ok:
        // a round trip less on every connect and switch. Both relays take a
        // socket's frames in order — the Go relay reads them in turn, the
        // Worker's Durable Object holds the next frame while auth's storage
        // calls run — so it always reaches the agent authed. If the agent is
        // away the relay drops it, and agent_online sends another, as before.
        startHandshake().catch((e) => {
          setStatus('Handshake failed: ' + e.message, 'error');
          fatal = true;
          try { ws.close(); } catch (_) {}
        });
        // Heartbeat. Cloudflare Workers keeps the WS alive on its end,
        // but mobile carriers / NAT timeouts / VPN tunnels can silently
        // drop an idle socket without surfacing close to either side
        // — the user types after 5 min of idle, the keystroke vanishes
        // into the void, and only the next `setTimeout(reconnect)` rescue
        // path notices. A 30s app-level ping forces traffic through the
        // tunnel and any drop becomes a concrete ws.onerror/close, which
        // triggers the existing reconnect logic. Matches the Go viewer's
        // ping cadence (internal/client/viewer.go:317).
        // Cadence only — the ping is SENT from viewerTick, not from a timer of
        // its own. A dedicated interval is throttled to about once a minute
        // once this tab has been hidden a while, which is the normal state
        // when a pane is popped out and the main window is minimized: the
        // keepalive stops arriving on time, the tunnel idles out, and the
        // pane would sit on a dead socket until this tab was focused again.
        // Riding viewerTick keeps the keepalive on one clock with everything
        // else, and a standalone pane tab now runs its own copy of all of it.
        lastPingAt = Date.now();
      };

      wireSocket(ws);
    }

    // wireSocket gives the page's connection its handlers: on a fresh dial,
    // and on a warm socket unparked (resumeWarm).
    function wireSocket(sock) {
      sock.onmessage = async (ev) => {
        noteInbound(sock);
        let msg;
        try { msg = JSON.parse(ev.data); } catch { return; }
        await handleMessage(msg);
      };

      sock.onclose = (e) => {
        dbg('ws CLOSE' + (e && e.code ? ' code=' + e.code : ''));
        // Closed before it was live (a wrong PIN, an ended session, a refusal):
        // the screen remembered for it is not shown any longer.
        if (staleScreen) forgetCachedScreen(sessionId);
        cancelDial();
        authed = false;
        agentLive = false;
        ws = null;
        // Stop the heartbeat — re-armed on the next ws.onopen.
          // Drop any pending predictions: the reconnect path will
        // replay scrollback, which would mismatch and trigger an
        // erasure sequence at the wrong cursor position.
        resetPredictionsHard();
        // Arm the in-terminal reconnect line so startSessionFlow
        // prints it after the next successful EKE+resume. Gated on
        // helloShown so we don't print on the very first failed
        // connect attempt (where there's no prior session to
        // "reconnect" to).
        if (helloShown && !manualClose && !fatal) { reconnectMessagePending = true; winStreamsNeedRestart = true; }
        if (manualClose) {
          settleUploadWaiters('left');
          setStatus('Disconnected');
          hideOverlay();
          setBtnConnect();
          return;
        }
        if (fatal) {
          settleUploadWaiters('ended');
          // Status already set by error handler. What the relay said, if it
          // was its word that ended it, stays over the terminal: the socket
          // closing after it is no news.
          if (fatalWords) showOverlay(fatalWords);
          else hideOverlay();
          setBtnConnect();
          return;
        }
        scheduleReconnect();
      };

      sock.onerror = () => {
        dbg('ws ERROR');
        // ws.onclose will run after this; let it drive reconnect logic.
      };
    }

    async function handleMessage(msg) {
      const msgSock = ws; // the connection this message arrived on
      // Behind a handshake answer still being opened: wait for its key,
      // in arrival order (the waits resume in the order they began).
      if (!cryptoKey && keyComing && keyComing.sock === msgSock && (msg.type === 'sealed' || sealable(msg.type))) {
        await keyComing.done;
        if (ws !== msgSock) return;
      }
      if (msg.type === 'sealed') {
        if (!sealState) {
          // The agent seals, but this connection's handshake answer said it
          // did not. Start over.
          if (cryptoKey) {
            dbg('sealed frame without a sealed handshake — reconnecting');
            setStatus('Reconnecting…', 'warn');
            try { ws.close(); } catch (_) {}
          } else {
            // Before there is a key to open it: what the early resume asked
            // for came ahead of the handshake's answer (an owner handshake
            // is answered off the agent's reader), so it is asked for again.
            droppedBeforeKey = msgSock;
          }
          return;
        }
        msg = await unsealIn(msg);
        if (!msg) {
          // The first frames after an unpark not opening under the key the
          // socket kept: not this agent's key any more. Dial afresh.
          if (unparkProbe === msgSock && ws === msgSock) warmFailed('frames do not open');
          return;
        }
        if (unparkProbe === msgSock) unparkProbe = null;
      } else if (sealState && sealable(msg.type)) {
        return; // the unsealed copy the agent writes for viewers that predate sealing
      }
      if (DEBUG) dbg('◀ ' + msg.type + (msg.ex_id ? ' ex=' + String(msg.ex_id).slice(0, 6) : ''));
      switch (msg.type) {
        case 'auth_ok':
          authed = true;
          // The relay is talking to us now; the dial watchdog has done its job.
          cancelDial();
          // Auth succeeded — remember the PIN for this tab so a refresh
          // doesn't bounce the user back to the login form.
          saveCred(sessionId, pinValue);
          // Also add to the persistent localStorage history so the user
          // can pick this session from the Recent dropdown later.
          rememberSession(sessionId, pinValue);
          refreshTabTitle();
          acquireWakeLock();
          // The handshake left with auth (ws.onopen). Only a socket that has
          // none yet starts one here.
          try {
            if (handshakeSock !== ws) await startHandshake();
          } catch (e) {
            setStatus('Handshake failed: ' + e.message, 'error'); forgetCachedScreen(sessionId);
            fatal = true;
            try { ws.close(); } catch (_) {}
          }
          break;
        case 'own_busy':
          // Not a refusal: the machine is over its handshake allowance and has
          // asked us to come back. Waiting is the whole fix — the old silence
          // here read as "you are not an owner". Only an answer to one of our
          // own attempts counts.
          if (cryptoKey || !ownerAttempts.has(msg.ex_id)) break;
          if (ownerBusyTries < OWNER_BUSY_RETRIES) {
            ownerBusyTries++;
            const wait = Math.min(Math.max(Number(msg.retry_ms) || OWNER_RETRY_MS, 250), 5000);
            dbg('own_busy — retry ' + ownerBusyTries + ' in ' + wait + 'ms');
            setStatus('Machine is busy — retrying…', 'warn');
            retryOwnerInit(wait);
          } else {
            ownerGaveUp('busy');
          }
          break;
        case 'own_resp':
          try {
            const done = completeOwner(msg);
            keyComing = { sock: msgSock, done: done.catch(() => {}) };
            await done;
            if (cryptoKey && ws === msgSock) keyExID = String(msg.ex_id || '');
          } catch (e) {
            if (ws !== msgSock) break; // an older connection's answer; this one is unaffected
            if (kexTimer) { clearTimeout(kexTimer); kexTimer = 0; }
            if (ownerTimer) { clearTimeout(ownerTimer); ownerTimer = 0; }
            setStatus('Owner connect failed: ' + e.message, 'error'); forgetCachedScreen(sessionId);
            fatal = true;
            try { ws.close(); } catch (_) {}
            break;
          }
          startSessionFlow();
          break;
        case 'pake_busy': {
          // The relay or the machine is pacing PIN handshakes. Wait as asked,
          // then start a fresh handshake on this socket; a few times at most.
          if (!kexAttempts.has(msg.ex_id) || cryptoKey) break;
          if (kexTimer) { clearTimeout(kexTimer); kexTimer = 0; }
          kexAttempts.delete(msg.ex_id);
          if (++pakeBusyTries > 3) { setStatus('This machine is busy — try again in a few minutes', 'error'); break; }
          const wait = Math.min(Math.max(Number(msg.retry_ms) || 0, 1000), 90000);
          setStatus('Busy — trying again in ' + Math.ceil(wait / 1000) + 's', 'warn connecting');
          const sock = ws;
          setTimeout(() => { if (ws === sock && !cryptoKey) sendKexInit().catch(() => {}); }, wait);
          break;
        }
        case 'pake_resp':
          try {
            const done = completeKex(msg);
            keyComing = { sock: msgSock, done: done.catch(() => {}) };
            await done;
            if (cryptoKey && ws === msgSock) keyExID = String(msg.ex_id || '');
          } catch (e) {
            // AES-GCM tag mismatch lands here. Could be a typo'd PIN
            // (saved creds went stale, recents row points at the
            // wrong session) or an active relay MITM that guessed
            // the PIN wrong. Either way, fatal — reconnecting would
            // just re-run the same broken handshake.
            // Disarm the watchdog first. It fires KEX_TIMEOUT_MS later, sees
            // no session key, and replaces this with "Handshake timed out" —
            // so the one message that says what is actually wrong survived
            // about nine seconds and then became a message about the network.
            if (ws !== msgSock) break; // an older connection's answer; this one is unaffected
            if (kexTimer) { clearTimeout(kexTimer); kexTimer = 0; }
            if (ownerTimer) { clearTimeout(ownerTimer); ownerTimer = 0; }
            setStatus('Handshake failed: PIN mismatch', 'error'); forgetCachedScreen(sessionId);
            fatal = true;
            try { ws.close(); } catch (_) {}
            break;
          }
          startSessionFlow();
          break;
        case 'connected':
        case 'agent_online':
          // The agent just came (back) online. The reattaching agent
          // might be a different process image than the one we
          // originally EKE-ed with — `reminal restart` hot-execs the
          // binary, which keeps the session ID + PIN but generates a
          // fresh sessionKey (see internal/client/agent.go: "session
          // encryption key is NOT carried across the exec"). If we
          // keep our old cryptoKey we'll silently fail to decrypt
          // everything the new process emits, which presents as the
          // terminal "hanging" after Agent reconnected.
          //
          // Always re-EKE on the offline→online edge. Harmless if it
          // turned out to be the same agent — we just refresh our K.
          {
            const wasLive = agentLive;
            // "agent_online" while the agent was thought here: the relay had
            // not noticed its old connection end (a process that died, or a
            // machine that went away, without its socket being seen to close)
            // and a newly authenticated one took its place — possibly
            // another process, with another session key. Keeping the old
            // key, every key typed went out sealed for an agent that is gone
            // and was dropped there, and nothing came back. Treated as the
            // agent coming back: handshake again (for the same process it
            // only refreshes the key).
            const fresh = !wasLive || msg.type === 'agent_online';
            if (fresh) winStreamsNeedRestart = true; // fresh agent process = no live streams
            agentLive = true;
            hideSleepWakeScreen(); // a woken session: the live terminal takes over its saved screen
            connectedAt = Date.now();
            backoffMs = 1000;
            redialNow = true; // up: its first close redials at once
            setStatus(sessionId, 'connected'); // green dot already implies "connected"
            hideOverlay();
            setBtnDisconnect();
            renderRecents();
            if (fresh) {
              // Agent (re)attached — it may be a fresh process, so any session
              // key we hold is stale; drop it and (re)handshake. On "connected"
              // the handshake sent at auth_ok reached this agent and either
              // reply may complete it, so the attempts stay. On "agent_online"
              // the agent was away: an answer to an earlier attempt could only
              // come from the process that left, with a key that is gone, so
              // those attempts are dropped and only a new one counts.
              cryptoKey = null;
              sealState = null;
              // The agent away: the resume sent with the handshake went with it.
              if (msg.type === 'agent_online') { kexAttempts.clear(); ownerAttempts.clear(); kexExID = ''; ownerCheckingSock = null; earlyResume = null; }
              retryWebRTC('agent reattach', 0); // renegotiate P2P with the (possibly new) agent
              // Only when the kex_init auth_ok already sent cannot serve.
              // "connected" is the relay stating the agent was there as we
              // authed, so that kex_init reached it and a second is waste —
              // and waste here is expensive: each one spends a token from the
              // agent's PIN allowance (8, one back per 10s), so at two per
              // connect four reconnections in a row leave a legitimate viewer
              // with silence and a blank screen. "agent_online" is a
              // transition: the agent was away, the relay dropped what we
              // sent, and only a new one can complete the handshake.
              if (msg.type === 'agent_online' || handshakeSock !== ws) {
                try { await startHandshake(); } catch (_) {}
              }
            } else if (!cryptoKey && kexAttempts.size === 0) {
              // First-ever connect on this WS — initiate the EKE.
              try { await startHandshake(); } catch (_) {}
            }
            startSessionFlow();
          }
          break;
        case 'unparked':
          // A warm socket is a viewer again: the screen is asked for as on
          // any connect (startSessionFlow), under the key it kept.
          if (ws !== msgSock || agentLive) break;
          if (unparkTimer) { clearTimeout(unparkTimer); unparkTimer = 0; }
          unparkProbe = ws;
          agentLive = true;
          connectedAt = Date.now();
          backoffMs = 1000;
          redialNow = true; // up: its first close redials at once (as on 'connected')
          setStatus(sessionId, 'connected');
          hideOverlay();
          setBtnDisconnect();
          renderRecents();
          startSessionFlow();
          break;
        case 'park_refused':
          if (ws === msgSock) warmFailed('refused: ' + (msg.error || ''));
          break;
        case 'agent_offline':
          agentLive = false;
          // An owner handshake under way waits for the agent's return, which
          // starts a new one; time it was away is not counted against it.
          if (ownerTimer && !cryptoKey) { clearTimeout(ownerTimer); ownerTimer = 0; ownerAttempts.clear(); ownerSilentTries = 0; }
          if (staleScreen) forgetCachedScreen(sessionId); // not live, and maybe never again
          setStatus('Agent offline — waiting…', 'warn connecting');
          showOverlay('Agent offline — waiting for it to reconnect…');
          // Keep the WS open; the relay will tell us when the agent is back.
          setBtnDisconnect();
          // Arm the "Reconnected" terminal line — the agent_offline
          // path bypasses ws.onclose since the WS itself stays up.
          if (helloShown) reconnectMessagePending = true;
          break;
        case 'download': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            const payload = JSON.parse(new TextDecoder().decode(bytes));
            handleDownloadChunk(payload);
          } catch (e) {
            viewerNote('download failed: ' + e);
          }
          break;
        }
        case 'notify': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            const payload = JSON.parse(new TextDecoder().decode(bytes));
            fireNotification(payload.message || '');
          } catch (_) {}
          break;
        }
        case 'window_notes': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            const payload = JSON.parse(new TextDecoder().decode(bytes));
            onWindowNotes(payload && payload.notes ? payload.notes : {});
          } catch (_) {}
          break;
        }
        case 'window_list': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            const payload = JSON.parse(new TextDecoder().decode(bytes));
            // Notes travel with the list so a reconnect always repopulates
            // them; a push at connect time arrives before we hold the key.
            if (payload && payload.notes) onWindowNotes(payload.notes);
            onWindowList(payload);
          } catch (_) {}
          break;
        }
        case 'app_list': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            const payload = JSON.parse(new TextDecoder().decode(bytes));
            onAppList(payload);
          } catch (_) {}
          break;
        }
        case 'host_info': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            const payload = JSON.parse(new TextDecoder().decode(bytes));
            onHostInfo(payload);
          } catch (_) {}
          break;
        }
        case 'new_session': {
          if (!cryptoKey) break;
          // Only the answer to a request this tab made; the host broadcasts
          // it to every viewer, and an unasked-for one must not connect us.
          if (!creatingSession) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            const payload = JSON.parse(new TextDecoder().decode(bytes));
            onNewSession(payload);
          } catch (_) {}
          break;
        }
        case 'changelog': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            onChangelog(JSON.parse(new TextDecoder().decode(bytes)));
          } catch (_) {}
          break;
        }
        case 'upgrade': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            onUpgrade(JSON.parse(new TextDecoder().decode(bytes)));
          } catch (_) {}
          break;
        }
        case 'window_frame': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            const payload = JSON.parse(new TextDecoder().decode(bytes));
            onWindowFrame(payload, false); // arrived over the WS relay
          } catch (_) {}
          break;
        }
        case 'webrtc_offer': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            await onWebRTCOffer(JSON.parse(new TextDecoder().decode(bytes)));
          } catch (_) {}
          break;
        }
        case 'webrtc_ice': {
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            await onWebRTCIce(JSON.parse(new TextDecoder().decode(bytes)));
          } catch (_) {}
          break;
        }
        case 'resize': {
          // Agent telling us its effective PTY size. We mirror it so
          // cursor-positioning escape codes the shell emits land in
          // the same column on every screen. Without this, a larger
          // viewer (iPad, desktop) keeps rendering at its viewport
          // size while the agent shrinks the PTY to fit a smaller
          // viewer (phone), and absolute-position escapes go
          // misaligned → garbled text.
          //
          // (0, 0) is a sentinel meaning "tell me your size" — sent
          // when the smallest of several viewers just disconnected
          // and the lone remaining viewer needs to re-report its
          // viewport so the PTY can grow back.
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            const payload = JSON.parse(new TextDecoder().decode(bytes));
            const c = parseInt(payload.cols, 10) || 0;
            const r = parseInt(payload.rows, 10) || 0;
          if (c === 0 && r === 0) {
            sizeTrace('pty-ask', 0, 0);
            TermSize.adoptPty(0, 0);
          } else if (c > 0 && r > 0) {
            sizeTrace('pty', c, r);
            TermSize.adoptPty(c, r);
          }
          } catch (_) {}
          break;
        }
        case 'upload_ack': {
          // Auto-paste the resolved absolute path into the shell at the
          // cursor — the part the user is actually here for after
          // uploading from a phone. We only react if the upload_id is
          // one WE submitted: every viewer sees the broadcast but only
          // the originator's pendingUploadIDs set has the match.
          if (!cryptoKey) break;
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            const payload = JSON.parse(new TextDecoder().decode(bytes));
            if (!payload.upload_id || !pendingUploadIDs.has(payload.upload_id)) break;
            pendingUploadIDs.delete(payload.upload_id);
            const waiter = uploadAckWaiters.get(payload.upload_id);
            if (waiter) { uploadAckWaiters.delete(payload.upload_id); waiter(payload.path || ''); }
            const path = payload.path || '';
            if (!path) break;
            // Quote the path if it contains shell-meaningful chars so
            // a `cat /path with spaces/foo.png` actually works without
            // the user having to add quotes themselves.
            const needsQuotes = /[\s"'`$\\!*?(){}[\]<>|&;]/.test(path);
            const quoted = needsQuotes ? `'${path.replace(/'/g, `'\\''`)}'` : path;
            // Land the path in whatever the bar is driving — the focused
            // window if its keyboard is up, otherwise the shell.
            //
            // With a trailing space: uploading several files types one path per
            // ack into the same line, and without a separator they ran together
            // into one unusable word. A trailing space rather than a separator
            // between them because each ack is handled on its own and knows
            // nothing about the others -- and it costs a single upload nothing,
            // since a shell ignores a trailing space and the next word the user
            // types needed one anyway.
            typeToTarget(quoted + ' ');
            // Best-effort: also stash in the phone's clipboard so the
            // user can paste it elsewhere (e.g. into the next message
            // in a different app).
            try { await navigator.clipboard.writeText(path); } catch (_) {}
          } catch (_) {}
          break;
        }
        case 'snapshot_part':
          await onSnapshotPart(msg);
          break;
        case 'data': {
          if (!cryptoKey) break;
          // Dedup by seq to drop accidental duplicates (orphaned WS races
          // on reconnect, late-joiner replay overlapping live stream).
          // BUT: a hot-restart of the source agent rebuilds the seq
          // counter from zero. After such a restart the viewer's
          // lastSeq is way past anything the new agent will ever emit,
          // so naive dedup would silently swallow every incoming
          // message until the new counter happened to climb past
          // lastSeq — could be never. We detect "agent restarted" by
          // seq going dramatically backward (or seq 1 explicitly) and
          // reset lastSeq before processing.
          let staleTagged = false;
          if (typeof msg.seq === 'number') {
            if (msg.seq === 1 || msg.seq + 32 < lastSeq) {
              lastSeq = 0;
            } else if (msg.seq <= lastSeq) {
              // A snapshot is tagged with the seq of the newest entry it
              // covers, so caught-up viewers drop it. But a viewer that just
              // attached to a STREAMING session gets live frames before its
              // snapshot — they advance lastSeq past the snapshot's tag, and
              // this drop then swallowed the one frame that paints history:
              // black screen, only new output, until a resize made the app
              // repaint itself. While a fresh paint is owed, stale-tagged
              // frames go through decrypt so the snapshot (identified by its
              // ED2+ED3 wipe prefix) can land; non-snapshots still drop below.
              if (Date.now() >= awaitingSnapshotUntil) break;
              staleTagged = true;
            }
          }
          try {
            const bytes = await decrypt(cryptoKey, msg.data);
            // A snapshot in parts is being gathered: what comes meanwhile
            // waits for it (onSnapshotPart plays it after the paint).
            if (snapAsm) { snapAsm.held.push({ seq: typeof msg.seq === 'number' ? msg.seq : 0, bytes }); break; }
            if (awaitingSnapshotUntil) {
              if (hasWipeSignature(bytes)) awaitingSnapshotUntil = 0; // the owed paint arrived
              else if (staleTagged) break; // stale and not the snapshot — a true duplicate
            }
            if (typeof msg.seq === 'number' && msg.seq > lastSeq) lastSeq = msg.seq;
            if (snapLive && typeof msg.seq === 'number') keepForSnapshot(msg.seq, bytes);
            // Batch into the per-frame write queue instead of writing
            // each chunk immediately. On refresh the agent dumps the
            // entire 2 MiB scrollback as ~500 separate TypeData frames;
            // calling term.write() once per frame made xterm.js render
            // between each one, producing the multi-second "scrolling
            // through every line" effect. enqueueData coalesces all
            // chunks received within an animation frame into a single
            // term.write() call.
            enqueueData(bytes);
            bytesReceived += bytes.byteLength;
          } catch (_) { /* decrypt failure: skip */ }
          break;
        }
        // The relay's word is said over the terminal, never written into it:
        // written, it landed on whatever row the cursor was on ("session not
        // found or not ready" over an agent's footer).
        case 'closed':
          fatal = true;
          settleUploadWaiters('ended');
          fatalWords = msg.error || 'Session ended';
          clearCred();
          setStatus(fatalWords, 'error');
          showOverlay(fatalWords);
          break;
        case 'error':
          fatal = true;
          settleUploadWaiters('ended');
          fatalWords = msg.error || 'Error';
          clearCred();
          setStatus(fatalWords, 'error');
          showOverlay(fatalWords);
          break;
      }
    }

    // switching: the page goes on to another session at once, whose terminal
    // this would be written into (term.reset() is immediate, a write is not).
    function userDisconnect(opts) {
      const switching = !!(opts && opts.switching);
      cacheScreen(); // before the "Disconnected" line goes over it
      manualClose = true;
      settleUploadWaiters('left');
      cancelReconnect();
      cancelDial();
      clearCred();
      // Switching to another session: this one's socket is kept warm
      // (parkCurrent), for a switch back. Else closed.
      if (switching) parkCurrent();
      if (unparkTimer) { clearTimeout(unparkTimer); unparkTimer = 0; }
      unparkProbe = null;
      // Stop the heartbeat — we null out ws.onclose below to bypass
      // the auto-reconnect path, so its own clear-interval won't fire.
      if (ws) {
        try {
          ws.onopen = null;
          ws.onmessage = null;
          ws.onclose = null;
          ws.onerror = null;
          ws.close();
        } catch (_) {}
        ws = null;
      }
      authed = false;
      agentLive = false;
      dropHeldInput(); // nothing typed now goes anywhere
      // Tear down any window panes — their streams are bound to the WS we
      // just closed. Guarded because userDisconnect can run before the window
      // module has initialised on very early failures.
      if (typeof closeAllPanes === 'function') { try { closeAllPanes(); } catch (_) {} }
      // Any in-flight predictions are moot once the session ends;
      // there will be no echo to reconcile against. Drop them so
      // they can't dim chars into a stale "Disconnected" footer.
      resetPredictionsHard();
      if (helloShown && connectedAt && !switching) {
        const dur = Math.max(1, Math.round((Date.now() - connectedAt) / 1000));
        viewerNote('Disconnected from ' + sessionId + ' after ' + humanDuration(dur) + ' · sent ' + humanBytes(bytesSent) + ' · received ' + humanBytes(bytesReceived));
      }
      setStatus('Disconnected');
      hideOverlay();
      setBtnConnect();
      // Clear "current" highlight in recents now that we're not bound.
      renderRecents();
    }

    // Serialize all outbound keystrokes/keybar presses into a single chain.
    // Without this, two fast onData calls would each `await encrypt()` in
    // parallel and the Web Crypto API doesn't guarantee promise-resolution
    // order matches call order — so the bytes could land on the WS in the
    // wrong sequence and the remote shell would see e.g. "clra" instead of
    // "clear", running a bogus prefix as a command and leaving "ar" typed
    // at the resulting prompt.
    let sendChain = Promise.resolve();
    // Cross-scope state for the Android-Chrome autocorrect defence up
    // near term.open(). Two distinct attack pathways to block here:
    //
    //   (1) `suppressNextDataChunk` — beforeinput insertCompositionText
    //       events are non-cancelable per the Input Events spec, so we
    //       can't preventDefault the autocorrect composition. Instead
    //       we stage the offending composition's data at compositionend
    //       time; sendInput drops the matching call when xterm forwards
    //       the composed text here.
    //
    //   (2) `realBackspacesPending` counter — Samsung Keyboard fires
    //       phantom word-deletions via `input deleteContentBackward`
    //       with NO matching Backspace keydown and NO beforeinput. xterm
    //       sees the textarea shrink and emits a BS char per deleted
    //       char into sendInput. Real Backspace key presses get
    //       counted on keydown (over in the helperTA defence block);
    //       each \x7f going through sendInput consumes one count.
    //       Counter decays after 100ms so phantom keydowns don't leak
    //       budget to subsequent phantom deletions. \x7f calls with no
    //       budget drop silently.
    let suppressNextDataChunk = null;
    let suppressTimer = 0;
    let realBackspacesPending = 0;
    // See sendInput: the last thing sent by a path other than the iOS mirror.
    let lastNonMirrorSend = null;
    let lastNonMirrorSendAt = -Infinity;
    // How long the mirror treats an identical non-mirror send as the same
    // keystroke. Generous next to the gap between a keydown and its edit (a
    // millisecond or two), tight next to two deliberate taps of the same key,
    // which stay correct anyway: each has its own keydown, and each sends.
    const MIRROR_DUP_WINDOW_MS = 250;
    // After a suspicious compositionend, drop ANY multi-char data
    // xterm tries to forward for the next SUSPICIOUS_DROP_MS. xterm's
    // composition-commit path doesn't always call sendInput with the
    // exact composed string — it can send things like "\b\b\bskssiow"
    // (backspace-and-replace) or split the commit across multiple
    // calls. Exact-string suppression misses those; a time-windowed
    // multi-char drop catches them all. Single chars (real typed
    // letters/spaces and backspaces) still pass through.
    let suppressMultiCharUntil = -Infinity;
    const SUSPICIOUS_DROP_MS = 100;
    // Output-layer dedup. A captured Android-Chrome trace showed a
    // single beforeinput+input pair for a "." tap but two "."s
    // arriving at the PTY — xterm.js calls sendInput twice for one
    // typed character via some IME path. 5ms is far below any legit
    // typing interval (autorepeat is ~30ms, fast human typing is
    // ~60ms+), so this filters genuine duplicates only. And only a send
    // with no key or input event of its own: one that came with a keydown
    // or a beforeinput since the last is a key typed again, however fast
    // (a paste of keys, an automation, a key held down on a fast repeat).
    let lastSendInputData = null;
    let lastSendInputAt = -Infinity;
    let inputEventSerial = 0;      // keydowns and beforeinputs seen, anywhere on the page
    let lastSendInputSerial = -1;  // inputEventSerial at the last send
    document.addEventListener('keydown', () => { inputEventSerial++; }, true);
    document.addEventListener('beforeinput', () => { inputEventSerial++; }, true);
    // --- Predictive local echo (mosh-style) ---
    // For high-RTT sessions (phone over LTE → relay → laptop) the
    // typing experience is dominated by the round-trip: keystroke →
    // relay → agent → shell echoes → relay back → render. 200-400ms
    // feels mushy. We can mask most of that for the common case
    // (typing into a shell prompt) by rendering predicted echoes
    // locally and reconciling against the agent's echo when it
    // arrives. Only predict for single-byte printable ASCII so we
    // can't accidentally render escape codes; only predict while
    // predictions are landing (confirmed echo extends the window),
    // so TUI apps (vim, claude code) that don't echo keystrokes
    // turn local echo off automatically.
    const predictions = [];                  // [{ c: charCode, ch: str, t: ms }]
    let predictBlockedUntil = 0;             // cool-down deadline; predict skipped while < this
    const PREDICT_BLOCK_MS = 1500;           // cool-down after a stale/mismatch
    const PREDICT_STALE_MS = 700;            // floor: drop a prediction we never got echo for
    const PREDICT_STALE_CAP_MS = 2500;       // ceiling: bound how long an un-echoed char can linger
    const PREDICT_MAX_QUEUE = 24;            // safety cap
    let predictStaleTimer = 0;
    // Round-trip estimate (keystroke → echo back), EWMA over confirmed
    // echoes. The whole point of predictive echo is high-RTT links, but a
    // FIXED 700ms stale timeout fights exactly those: on a 900ms-RTT link
    // the stale timer wipes the dim run ~150ms BEFORE the echo that would
    // confirm it arrives, yanking the cursor back to the prompt start —
    // the "cursor jumps to the beginning of the dim part" bug. So we scale
    // the stale window to the measured RTT (kept alive ~2.5×) and only fall
    // back to the 700ms floor when we have no measurement yet (a fresh
    // burst / genuine no-echo password prompt, where prompt erasure is the
    // privacy-correct behaviour anyway). Seeded from the WS handshake time
    // so even the first burst on a slow link doesn't flicker.
    let observedRTT = 0;
    function noteRTT(sampleMs) {
      if (sampleMs <= 0 || sampleMs > 10000) return; // ignore clock jumps / outliers
      observedRTT = observedRTT > 0 ? Math.round(observedRTT * 0.7 + sampleMs * 0.3) : sampleMs;
    }
    function effectiveStaleMs() {
      if (observedRTT <= 0) return PREDICT_STALE_MS;
      return Math.min(PREDICT_STALE_CAP_MS, Math.max(PREDICT_STALE_MS, Math.round(observedRTT * 2.5)));
    }
    // The cursor-back+rewrite+cursor-forward sequences we emit only
    // work on the line we're typing on. If predictions would overflow
    // to a wrapped second line, promote/erase math breaks because CUB
    // (\x1b[<n>D) does not cross line boundaries on most terminals.
    function predictWouldWrap(extra) {
      try {
        const buf = term.buffer.active;
        return (buf.cursorX + extra) >= term.cols;
      } catch (_) { return true; }
    }
    // Master switch for predictive local echo. Disabled: the dim/bright
    // "speculative echo" masked latency on high-RTT links, but doing it by
    // juggling cursor escape codes (CUB/CUF + erase-by-spaces) only stays
    // correct when the cursor sits exactly where this layer assumes. Real
    // shells break that constantly — zsh syntax-highlighting rewrites the
    // whole line on each keystroke (so the echo never matches the per-char
    // prediction → flicker), and autosuggestions / RPROMPT / themed prompts
    // emit extra repositioning that drifts the cursor, depositing predicted
    // chars at scattered columns the cleanup then misses (residue on screen).
    // Without modelling the full framebuffer (mosh-style) it can't be made
    // robust, so keystrokes now render only when the shell echoes them —
    // standard SSH behaviour, no gray text, no flicker, no residue. Flip
    // back to true to re-enable the experiment.
    const PREDICT_ENABLED = false;
    function predictLocalEcho(data) {
      if (!PREDICT_ENABLED) return;
      if (staleScreen) return; // the cursor is on a remembered screen, not the live one
      const now = performance.now();
      if (now < predictBlockedUntil) return;
      if (data.length !== 1) return;
      const c = data.charCodeAt(0);
      if (c < 0x20 || c > 0x7e) return;     // printable ASCII only
      if (predictions.length >= PREDICT_MAX_QUEUE) return;
      if (predictWouldWrap(1)) return;
      // The rAF batch holds incoming agent data not yet rendered.
      // If we draw the dim char first and the batch flushes later,
      // those bytes land AFTER our prediction → cursor offsets get
      // wrong and the next promote/erase rewrites the wrong cells.
      // Drain now so cursor sits at the real terminal position before
      // we overlay anything.
      flushPendingChunks();
      // SGR 2 = dim/faint; 22 = normal intensity (un-dim, preserve
      // color). Confirmed echoes promote each cell to normal so
      // typing visually transitions in-flight (dim) → synced (bright).
      // try/catch so a transient xterm.js error doesn't desync our
      // prediction queue from what's actually on screen.
      try { term.write('\x1b[2m' + data + '\x1b[22m'); }
      catch (_) { return; }
      predictions.push({ c, ch: data, t: now });
      armStaleTimer();
    }
    // Erase the visible dim chars for the predictions we still hold.
    // Cursor is at end-of-run; walk back N, write N spaces, walk back
    // N to land at start-of-run. Important: this clears the pending
    // queue too — every caller treats the predictions as gone.
    function erasePendingDimChars() {
      const n = predictions.length;
      if (n === 0) return;
      try { term.write('\x1b[' + n + 'D' + ' '.repeat(n) + '\x1b[' + n + 'D'); }
      catch (_) {}
      predictions.length = 0;
    }
    // Hard-reset for cases where the cursor position is no longer
    // reliable (disconnect, post-reflow on the agent side) — we can't
    // safely emit erasure escape codes because we don't know where
    // the dim chars actually live anymore. Drop the queue and cool
    // down. Any leftover dim chars on screen get overwritten by the
    // next agent redraw (which usually comes within a frame).
    function resetPredictionsHard() {
      predictions.length = 0;
      predictBlockedUntil = performance.now() + PREDICT_BLOCK_MS;
    }
    // Soft-reset for cases where the cursor IS still at end-of-run
    // (just before a local resize / fit that we're driving). Erase
    // the visible dim chars at known-good cursor position, then cool
    // down. Used in preference to the hard form when we can — leaves
    // a clean screen instead of orphaned dim chars that would get
    // double-rendered when the agent's echo finally arrives.
    function resetPredictionsSoft() {
      erasePendingDimChars();
      predictBlockedUntil = performance.now() + PREDICT_BLOCK_MS;
    }
    // Drop predictions older than PREDICT_STALE_MS *and* erase their
    // dim chars from the screen. This is the password-prompt safety
    // net: if the shell isn't echoing, the predicted chars don't
    // linger on display. Runs both from a setTimeout (covers the
    // "user typed then went idle" case) and from reconcile (covers
    // "echo arrived but only partially matches"). All-or-nothing
    // erasure keeps cursor math simple — a partial stale-drop would
    // leave the surviving tail at the wrong column.
    function dropStalePredictions() {
      const now = performance.now();
      if (predictions.length === 0) return false;
      if (now - predictions[0].t <= effectiveStaleMs()) return false;
      erasePendingDimChars();
      predictBlockedUntil = now + PREDICT_BLOCK_MS;
      return true;
    }
    function armStaleTimer() {
      if (predictStaleTimer) return;
      predictStaleTimer = setTimeout(() => {
        predictStaleTimer = 0;
        if (dropStalePredictions() === false && predictions.length > 0) {
          armStaleTimer();
        }
      }, effectiveStaleMs() + 50);
    }
    // Called on every incoming agent data chunk before xterm renders
    // it. Consumes any prefix that matches our predictions queue, so
    // the agent's echo doesn't double-render what we already showed.
    // Returns the leftover bytes (which xterm renders normally).
    function reconcilePredictions(bytes) {
      if (!PREDICT_ENABLED) return bytes;   // nothing predicted → render agent bytes verbatim
      dropStalePredictions();
      const now = performance.now();
      let idx = 0;
      let confirmedChars = '';
      while (predictions.length > 0 && idx < bytes.length && bytes[idx] === predictions[0].c) {
        // First confirmed char of this echo measures the live round-trip;
        // feeds the adaptive stale window so the timer never wipes a dim
        // run that's still in flight on a slow link.
        if (confirmedChars === '') noteRTT(now - predictions[0].t);
        confirmedChars += predictions[0].ch;
        predictions.shift();
        idx++;
      }
      // Promote dim → bright. Cursor is at position P (start of run
      // + total predicted chars drawn so far). Walk back over the
      // entire predicted run, overwrite the confirmed prefix with
      // normal intensity, then walk forward past whatever pending
      // (still-dim) tail remains so the cursor lands back at P.
      if (confirmedChars.length > 0) {
        const remaining = predictions.length;
        const back = confirmedChars.length + remaining;
        let seq = '\x1b[' + back + 'D' + confirmedChars;
        if (remaining > 0) seq += '\x1b[' + remaining + 'C';
        term.write(seq);
      }
      // Tail dim chars don't match what the agent actually sent next:
      // either no match at all (idx === 0, full mismatch — likely a
      // TUI / password prompt where the shell suppressed echo) or a
      // partial match where the prefix matched but the agent's next
      // byte diverges from our remaining predictions (shell aliased a
      // key, autocompletion shortened input, autocorrect rewrite). In
      // BOTH cases the leftover dim chars are wrong — erase them
      // immediately instead of waiting 700ms for the stale-timer, or
      // they'd render as ghost characters on screen alongside the
      // agent's authoritative output.
      if (predictions.length > 0 && idx < bytes.length) {
        erasePendingDimChars();
        predictBlockedUntil = now + PREDICT_BLOCK_MS;
        // erasePendingDimChars leaves the cursor at the START of the
        // just-erased dim run (CUB n · spaces · CUB n). If we returned
        // the leftover to be batched, xterm would paint this frame with
        // the cursor parked at start-of-run and only advance it a frame
        // later when the rAF flush renders the agent's bytes — a visible
        // backward cursor jump to the beginning of the dim part. It
        // fires on EVERY keystroke for shells that rewrite the whole
        // line on echo (zsh-syntax-highlighting, fish, line editors),
        // where the echo never matches our per-char prediction. Render
        // the agent's authoritative bytes synchronously in the SAME turn
        // so xterm only ever paints the final cursor position. Safe to
        // write now (not batch): this branch requires predictions to be
        // non-empty on entry, and a non-empty queue guarantees an empty
        // pendingChunks — so there's nothing queued ahead to reorder.
        const leftover = idx === 0 ? bytes : bytes.subarray(idx);
        if (leftover.byteLength > 0) {
          try { term.write(leftover); } catch (_) {}
        }
        return new Uint8Array(0);
      }
      return idx === 0 ? bytes : bytes.subarray(idx);
    }

    async function sendInput(data, opts) {
      const now = performance.now();
      // iOS field-mirror invalidation: input that didn't come through the
      // mirror (keybar keys, Paste button, an upload's auto-pasted path)
      // changes the shell line behind the mirror's back. Drop the model —
      // and the field text it mirrors — so a later dictation diffs against
      // reality instead of a stale line and can't emit backspaces for
      // characters that are no longer there. (opts.ime marks the mirror's
      // own sends; xterm-originated sends on iOS are line-neutral except
      // Enter, which the keydown handler already resets on.)
      if (APPLE_MOBILE && imeField && !(opts && opts.ime)) {
        imeField = '';
        if (helperTA) { helperTA.value = ''; }
      }
      if (data === lastSendInputData && (now - lastSendInputAt) < 5 && inputEventSerial === lastSendInputSerial) {
        return;
      }
      lastSendInputData = data;
      lastSendInputAt = now;
      lastSendInputSerial = inputEventSerial;
      // Time-windowed multi-char drop. Real user typing arrives one
      // char at a time; autocorrect-spawned composition commits arrive
      // as multi-char strings or backspace-and-replace sequences. If a
      // suspicious compositionend just fired, anything multi-char in
      // the next 100ms is autocorrect noise.
      if (now < suppressMultiCharUntil && data.length > 1) {
        return;
      }
      if (suppressNextDataChunk !== null && data === suppressNextDataChunk) {
        suppressNextDataChunk = null;
        clearTimeout(suppressTimer);
        return;
      }
      if (data === '\x7f' || data === '\x08') {
        if (realBackspacesPending > 0) {
          realBackspacesPending--;
        } else {
          return; // phantom IME-driven backspace; drop
        }
      }
      // Not live yet (the terminal is shown while it connects, its session
      // is still starting, the agent is away or its handshake is being
      // redone): held, and sent once it is. Dropped here, the keys vanished
      // with nothing to say so. Behind keys already held, so nothing typed
      // overtakes them.
      if (window.reminalMoments) try { reminalMoments.input(sessionId, data); } catch (_) {}
      const n = ++inputSerial;
      if (!inputLive() || heldInput.length) {
        holdInput(data, n);
        flushHeldInput();
        return;
      }
      // Predict ONLY after all suppression / drop checks have passed.
      // Predicting earlier would dim-render keystrokes we never sent
      // (autocorrect suppression, phantom IME backspace, offline drop)
      // and they'd then linger on screen until the 700ms stale-timer
      // wiped them — visible noise for the user.
      // What went out by any path other than the mirror, and when. The mirror
      // consults this before sending: on iOS a SHIFTED letter arrives as a real
      // keydown carrying the character (an unshifted one is keyCode 229 with no
      // character at all), so xterm's own keydown handler sends it — and then
      // the edit reaches the mirror, which sends it again. One tap of shift+S
      // put "SS" on the shell, and at a password prompt, where nothing echoes,
      // it looked like the password was simply wrong.
      if (!(opts && opts.ime)) {
        lastNonMirrorSend = data;
        lastNonMirrorSendAt = now;
      }
      predictLocalEcho(data);
      return deliverInput(data, n);
    }
    term.onData(sendInput);

    // Keys typed while the session is not live: held (in the order typed, at
    // most INPUT_HOLD_BYTES) and sent when it is — unless they waited longer
    // than INPUT_HOLD_MS, when sending them would be a surprise; then the
    // terminal says they were not sent.
    const INPUT_HOLD_MS = 30000;
    const INPUT_HOLD_BYTES = 65536;
    let heldInput = []; // [{ n, data }], n ascending
    let heldBytes = 0;
    let heldSince = 0;
    let heldOverflow = false;
    let heldLateTimer = 0;
    let inputSerial = 0;
    // The session a connect is switching to while it awaits the owner key;
    // until it has dialled, the socket still open is the old session's.
    let switchingTo = '';
    let connectGen = 0;
    function abandonSwitch() {
      if (!switchingTo) return;
      switchingTo = '';
      dropHeldInput(); // typed for a session we are not joining
    }
    function inputLive() {
      return !!(!switchingTo && agentLive && authed && cryptoKey && ws && ws.readyState === WebSocket.OPEN) && !wakeHolding();
    }
    function holdInput(data, n) {
      if (heldBytes + data.length > INPUT_HOLD_BYTES) { heldOverflow = true; updateHeldChip(); return; }
      if (!heldInput.length) heldSince = Date.now();
      let i = heldInput.length;
      while (i > 0 && heldInput[i - 1].n > n) i--;
      heldInput.splice(i, 0, { n, data });
      heldBytes += data.length;
      // Held longer than INPUT_HOLD_MS, it is no longer sent: the chip says
      // so then, not only once it connects.
      if (!heldLateTimer) heldLateTimer = setTimeout(() => { heldLateTimer = 0; updateHeldChip(); }, INPUT_HOLD_MS + 50);
      updateHeldChip();
    }
    function dropHeldInput() {
      heldInput = []; heldBytes = 0; heldSince = 0; heldOverflow = false;
      if (heldLateTimer) { clearTimeout(heldLateTimer); heldLateTimer = 0; }
      updateHeldChip();
    }
    // What is held is shown, by its count: keys typed while the session
    // connects, or while its machine has not answered yet, waited for
    // silently — they appeared all at once much later, which read as the
    // machine being seconds behind the keyboard. Counted in characters typed
    // (a paste is as many as it has), a key that is not text (Enter, an
    // arrow, Ctrl-anything) as one.
    // A bracketed paste (\x1b[200~ … \x1b[201~, what a shell that asks for
    // it gets) is counted by what is inside it.
    function heldCount() {
      let k = 0;
      for (const it of heldInput) {
        let d = it.data;
        if (d.startsWith('\x1b[200~')) {
          d = d.slice(6);
          if (d.endsWith('\x1b[201~')) d = d.slice(0, -6);
          k += [...d].length;
          continue;
        }
        k += /^[^\x00-\x1f\x7f]+$/.test(d) ? [...d].length : 1;
      }
      return k;
    }
    const heldChars = (k) => k + (k === 1 ? ' character' : ' characters');
    function updateHeldChip() {
      const chip = document.getElementById('held-chip');
      if (!chip) return;
      const k = heldCount();
      // Keys held while a session wakes wait as long as the wake does (wakeGate).
      const late = k && !wakeGate && Date.now() - heldSince > INPUT_HOLD_MS;
      // Anything dropped is said, even with nothing else held: a paste too
      // large to hold, on an empty queue, went with no word at all.
      const text = !k ? (heldOverflow ? 'A paste too large to hold was not sent' : '') : late
        ? heldChars(k) + ' typed while it was not connected will not be sent: they waited too long'
        : heldChars(k) + (wakeSlow() ? ' typed, held: it is taking long to wake' : wakeGate ? ' typed, waiting for it to wake' : ' typed, waiting for it to connect') + (heldOverflow ? ' (some too many to hold will not be sent)' : '');
      document.getElementById('held-chip-text').textContent = text;
      // Waiting on a woken agent that may never be back (its resume refused,
      // it is gone, it went back to the shell): sending what is held into
      // what is there now is the person's call, never made for them.
      document.getElementById('held-send').hidden = !(k && wakeGate);
      chip.hidden = !text;
    }
    // discardHeld drops what is held. Held for a wake, it also stops waiting
    // for the agent: what is typed next goes to what is there.
    function discardHeld() {
      const k = heldCount(), waking = !!wakeGate;
      if (waking) endWakeGate(false); else dropHeldInput();
      if (k) viewerNote(heldChars(k) + ' you typed while it ' + (waking ? 'woke' : 'connected') + ' ' + (k === 1 ? 'was' : 'were') + ' not sent.');
      term.focus();
    }
    document.getElementById('held-discard').addEventListener('click', () => discardHeld());
    // Send, held for a wake: stop waiting for the agent and send what is held
    // to what is there now.
    document.getElementById('held-send').addEventListener('click', () => { endWakeGate(true); term.focus(); });
    function flushHeldInput() {
      if (!inputLive() || (!heldInput.length && !heldOverflow)) return;
      const items = heldInput, late = Date.now() - heldSince > INPUT_HOLD_MS, over = heldOverflow;
      dropHeldInput();
      if (late || over) {
        viewerNote((late ? 'What you typed while not connected was not sent.' : 'Some of what you typed while not connected was too much to hold and was not sent.'));
        if (late) return;
      }
      dbg('live: sending ' + items.length + ' held keystroke(s)');
      for (const it of items) deliverInput(it.data, it.n);
    }
    // deliverInput sends one keystroke's bytes on the chain that keeps them in
    // order. Gone not live by the time its turn comes: held again, not lost.
    function deliverInput(data, n) {
      const result = sendChain.then(async () => {
        if (!inputLive()) { holdInput(data, n); return; }
        const bytes = new TextEncoder().encode(data);
        let binary = '';
        bytes.forEach(b => binary += String.fromCharCode(b));
        await send({ type: 'data', data: btoa(binary) });
        bytesSent += bytes.byteLength;
        noteKeySent(data.length);
      });
      sendChain = result.catch(() => {}); // keep chain alive on errors
      return result;
    }

    // Mobile soft-key map. Values are the literal byte sequences the
    // remote shell expects — same as what xterm.js would emit for the
    // matching keypress on a hardware keyboard.
    const keyMap = {
      esc:      '\x1b',
      tab:      '\t',
      'ctrl-a': '\x01', // start of line
      'ctrl-c': '\x03', // interrupt
      'ctrl-d': '\x04', // EOF
      'ctrl-e': '\x05', // end of line
      'ctrl-k': '\x0b', // kill to end of line
      'ctrl-l': '\x0c', // clear screen
      'ctrl-o': '\x0f', // SI — readline operate-and-get-next; expand output in
      //                   Claude Code, save in nano. Sent raw like the rest.
      'ctrl-q': '\x11', // XON — resumes output a Ctrl-S paused; quit in some programs
      'ctrl-r': '\x12', // reverse history search
      'ctrl-s': '\x13', // XOFF — save in nano and many editors; with tty flow
      //                   control on, a shell's output pauses until Ctrl-Q.
      //                   Sent raw, as typed on a keyboard.
      'ctrl-u': '\x15', // clear line
      'ctrl-w': '\x17', // delete word back
      'ctrl-x': '\x18', // exit in nano, cut in many editors
      'ctrl-z': '\x1a', // suspend
      pipe:       '|',
      tilde:      '~',
      up:         '\x1b[A',
      down:       '\x1b[B',
      right:      '\x1b[C',
      left:       '\x1b[D',
      backspace:  '\x7f', // DEL — what virtually every shell reads as Backspace
    };
    // Tab close / page hide: send a clean WebSocket close frame so the relay
    // sees us go away immediately, instead of waiting for TCP keepalive /
    // read-deadline to time out. Without this, the agent would think this
    // viewer was still attached for tens of seconds after the user closed
    // the tab — wrong viewer count on the host, late "viewer disconnected"
    // line. pagehide is the reliable signal on mobile Safari where
    // beforeunload often doesn't fire.
    function gracefulClose() {
      if (ws && ws.readyState === WebSocket.OPEN) {
        try { ws.close(1000, 'page closed'); } catch (_) {}
      }
    }
    window.addEventListener('pagehide', gracefulClose);
    window.addEventListener('pagehide', () => { forgetCachedScreen(); closeAllWarm(); });
    // A page hidden a while keeps no warm sockets: a minute, then they go.
    let warmHiddenTimer = 0;
    document.addEventListener('visibilitychange', () => {
      if (warmHiddenTimer) { clearTimeout(warmHiddenTimer); warmHiddenTimer = 0; }
      if (document.hidden && warmSockets.size) warmHiddenTimer = setTimeout(() => { warmHiddenTimer = 0; closeAllWarm(); }, 60000);
    });
    window.addEventListener('beforeunload', gracefulClose);

    // Keybar buttons: single-tap sends one keystroke. Buttons tagged
    // data-repeat (arrows, backspace) hold-to-repeat — phones don't
    // have a key-repeat OS layer, so navigating bash history or
    // deleting a long line is otherwise hopeless. Touch-cancel on
    // significant finger movement so swiping the bar to scroll never
    // starts a repeat.
    const REPEAT_DELAY_MS = 360;
    const REPEAT_INTERVAL_MS = 55;
    const REPEAT_CANCEL_PX = 12;
    // When a window pane's keyboard mode is active, the keybar drives THAT
    // window instead of the shell: arrows/Tab/Esc/⌫ as native keys, ^C-style
    // chords as real Ctrl combos, | and ~ as text. A welcome side effect: ⌫
    // gets the bar's JS-timed hold-to-repeat, which works on iOS where the
    // soft keyboard's own repeat can't be coaxed onto our hidden input.
    const winBarMap = {
      up: { special: 'up' }, down: { special: 'down' },
      left: { special: 'left' }, right: { special: 'right' },
      tab: { special: 'tab' }, esc: { special: 'escape' }, backspace: { special: 'delete' },
      pipe: { text: '|' }, tilde: { text: '~' },
      'ctrl-a': { special: 'ctrl-a' }, 'ctrl-c': { special: 'ctrl-c' }, 'ctrl-d': { special: 'ctrl-d' },
      'ctrl-e': { special: 'ctrl-e' }, 'ctrl-k': { special: 'ctrl-k' }, 'ctrl-l': { special: 'ctrl-l' },
      'ctrl-o': { special: 'ctrl-o' },
      'ctrl-q': { special: 'ctrl-q' }, 'ctrl-s': { special: 'ctrl-s' }, 'ctrl-x': { special: 'ctrl-x' },
      'ctrl-r': { special: 'ctrl-r' }, 'ctrl-u': { special: 'ctrl-u' }, 'ctrl-w': { special: 'ctrl-w' },
      'ctrl-z': { special: 'ctrl-z' },
    };
    const winKbdActive = () => document.body.classList.contains('wkbd-open') && !!focusedPaneId;
    // Route free text (a paste, a snippet, an uploaded path) to whatever the
    // command bar is currently driving: the focused remote window when its
    // keyboard is up, otherwise the terminal. Every command-bar action that
    // injects input goes through here so none of them silently land in the
    // terminal (and steal its focus) while you're typing into a window.
    // Returns true if it went to the window. Callers that own the soft
    // keyboard should refocus wkey/term after — this only sends.
    function typeToTarget(text) {
      if (winKbdActive()) {
        winType(focusedPaneId, text);
        return true;
      }
      Promise.resolve(sendInput(text)).catch(() => {});
      return false;
    }
    document.querySelectorAll('#keybar button').forEach(btn => {
      if (!btn.dataset.key) return; // skip non-key buttons (Upload, etc.)
      const seq = keyMap[btn.dataset.key];
      if (!seq) return;
      const fire = () => {
        if (winKbdActive()) {
          const wk = winBarMap[btn.dataset.key];
          if (wk) sendWin('window_input', { id: focusedPaneId, kind: 'key', ...wk });
          return;
        }
        // The ⌫ button sends a bare \x7f — exactly what sendInput's
        // phantom-backspace gate exists to drop. A button tap is as real as
        // input gets, so fund the counter first (with the same decay the
        // keydown path uses, so an unconsumed credit can't leak to a later
        // phantom). Without this the button does nothing — every press was
        // silently eaten by the gate.
        if (seq === '\x7f') {
          realBackspacesPending++;
          setTimeout(() => { if (realBackspacesPending > 0) realBackspacesPending--; }, 100);
        }
        sendInput(seq);
      };
      if (!('repeat' in btn.dataset)) {
        // No-repeat keys: plain click + refocus whichever input owns the soft
        // keyboard so it stays open after the press. In window-keyboard mode
        // the press must not blur the hidden key input: mousedown is
        // prevented, and on touch we fire from touchend (preventing the
        // synthetic click) so focus never moves at all.
        btn.addEventListener('mousedown', (e) => { if (winKbdActive()) e.preventDefault(); });
        btn.addEventListener('touchend', (e) => {
          if (!winKbdActive()) return;
          e.preventDefault();
          fire();
        });
        btn.addEventListener('click', () => { fire(); (winKbdActive() ? wkey : term).focus(); });
        return;
      }
      let delayTimer, repeatTimer, startX, startY, fired;
      const startRepeat = () => { repeatTimer = setInterval(fire, REPEAT_INTERVAL_MS); };
      const begin = (x, y) => {
        startX = x; startY = y; fired = false;
        fire(); fired = true;
        delayTimer = setTimeout(startRepeat, REPEAT_DELAY_MS);
      };
      const cancel = () => {
        clearTimeout(delayTimer);
        clearInterval(repeatTimer);
        delayTimer = repeatTimer = null;
      };
      btn.addEventListener('touchstart', (e) => {
        e.preventDefault();
        const t = e.touches[0];
        begin(t.clientX, t.clientY);
      }, { passive: false });
      btn.addEventListener('touchmove', (e) => {
        const t = e.touches[0];
        if (Math.abs(t.clientX - startX) > REPEAT_CANCEL_PX ||
            Math.abs(t.clientY - startY) > REPEAT_CANCEL_PX) cancel();
      });
      btn.addEventListener('touchend', cancel);
      btn.addEventListener('touchcancel', cancel);
      // Mouse path for desktop / hybrid devices.
      btn.addEventListener('mousedown', (e) => { e.preventDefault(); begin(e.clientX, e.clientY); });
      btn.addEventListener('mouseup', cancel);
      btn.addEventListener('mouseleave', cancel);
      // Click also fires on touchend/mouseup; swallow it since begin()
      // already fired the first keystroke.
      btn.addEventListener('click', (e) => { if (fired) e.preventDefault(); });
    });
    // ⌨ button: explicitly focus xterm so the soft keyboard opens. iOS
    // sometimes hides the keyboard when you tap a non-input element and
    // there's no easy way to bring it back without a dedicated control.
    const kbFocusBtn = document.getElementById('kb-focus-btn');
    if (kbFocusBtn) kbFocusBtn.addEventListener('click', () => term.focus());

    // fireNotification handles TypeNotify messages from the agent. Falls
    // back gracefully when the browser denied permission, doesn't support
    // the API, or the tab is in the foreground (in which case we still
    // print a dim line + ring the terminal bell so the user notices).
    function fireNotification(message) {
      const text = (message || '').trim();
      if (!text) return;
      // Always an in-page signal too, for those without browser notification
      // permission: over the terminal, never in it (viewerNote: written into
      // the terminal it moved the screen under a program drawing there).
      term.write('\x07'); // the bell: moves nothing on the screen
      viewerNote('reminal notify: ' + text, 8000);
      if (!('Notification' in window)) return;
      const sessionLabel = sessionId || 'reminal';
      const fire = () => {
        try {
          new Notification('reminal · ' + sessionLabel, {
            body: text,
            tag: 'reminal-' + sessionLabel,
            silent: false,
          });
        } catch (_) {}
      };
      // Only when already allowed. This used to ask right here, with no tap
      // behind it — and Chrome answers un-tapped requests with its quiet
      // prompt (or nothing), counts each one as ignored, and after a few
      // blocks the whole site. That is how live.reminal.app ended up blocked
      // for Alerts before anyone pressed its button. Asking now happens only
      // from the Alerts sheet, inside a tap.
      if (Notification.permission === 'granted') fire();
    }

    // Filename extension → MIME for files the browser can render inline.
    // Anything not here falls through to a plain download.
    const PREVIEW_MIME = {
      png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg',
      gif: 'image/gif', webp: 'image/webp', svg: 'image/svg+xml',
      bmp: 'image/bmp', ico: 'image/x-icon', avif: 'image/avif',
      pdf: 'application/pdf',
    };
    function previewMimeFor(name) {
      const m = (name || '').toLowerCase().match(/\.([a-z0-9]+)$/);
      return m ? PREVIEW_MIME[m[1]] : null;
    }

    // showPreview renders an image or PDF in a fullscreen modal with
    // Save / Close. On phones this is what you actually want — fills the
    // screen, pinch-zoom for images, native PDF reader for PDFs, no need
    // to leave the browser to view a file the host just generated.
    let previewObjectURL = null;
    function showPreview(name, blob, mime) {
      if (previewObjectURL) URL.revokeObjectURL(previewObjectURL);
      previewObjectURL = URL.createObjectURL(blob);
      const overlay = document.getElementById('preview');
      const frame = document.getElementById('preview-frame');
      const meta = document.getElementById('preview-meta');
      // Swap the body element based on kind. Images use <img>; PDFs and
      // everything previewable-but-not-image goes through <iframe>.
      frame.innerHTML = '';
      if (mime.startsWith('image/')) {
        const img = document.createElement('img');
        img.src = previewObjectURL;
        img.alt = name;
        frame.appendChild(img);
      } else {
        const iframe = document.createElement('iframe');
        iframe.src = previewObjectURL;
        iframe.title = name;
        frame.appendChild(iframe);
      }
      meta.textContent = name + ' · ' + humanBytes(blob.size);
      overlay.classList.add('visible');
      document.getElementById('preview-save').onclick = () => {
        const a = document.createElement('a');
        a.href = previewObjectURL;
        a.download = name || 'file';
        document.body.appendChild(a);
        a.click();
        a.remove();
      };
    }
    document.getElementById('preview-close').addEventListener('click', () => {
      document.getElementById('preview').classList.remove('visible');
      if (previewObjectURL) { URL.revokeObjectURL(previewObjectURL); previewObjectURL = null; }
    });

    // triggerDownload routes previewable files (images, PDFs) to the modal
    // (better phone UX — no app switch) and everything else to a standard
    // Blob + hidden-anchor download.
    function b64ToBytes(b64) {
      const bin = atob(b64);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      return bytes;
    }

    function triggerDownload(name, b64) {
      triggerDownloadBytes(name, b64ToBytes(b64));
    }

    function triggerDownloadBytes(name, bytes) {
      const mime = previewMimeFor(name);
      if (mime) {
        showPreview(name, new Blob([bytes], { type: mime }), mime);
        viewerNote('previewing ' + name + ' (' + humanBytes(bytes.length) + ')');
        return;
      }
      const url = URL.createObjectURL(new Blob([bytes]));
      const a = document.createElement('a');
      a.href = url;
      a.download = name || 'download';
      document.body.appendChild(a);
      a.click();
      setTimeout(() => { URL.revokeObjectURL(url); a.remove(); }, 1000);
      viewerNote('downloaded ' + (name || 'file') + ' (' + humanBytes(bytes.length) + ')');
    }

    // Chunked-download reassembly, mirroring the Go viewer (viewer.go). The agent
    // sends `reminal send` files as N TypeDownload frames sharing a download_id;
    // small files arrive as a single frame (total<=1) and download immediately.
    // Bounds match the Go side so a hostile host can't OOM the tab.
    const DOWNLOAD_MAX_BYTES = 100 * 1024 * 1024;
    const DOWNLOAD_MAX_CHUNKS = 1600;
    const DOWNLOAD_STALE_MS = 60000;
    const pendingDownloads = new Map(); // download_id -> {name,total,chunks:Map,bytes,timer}

    function handleDownloadChunk(payload) {
      // Single-shot: legacy agents (no download_id) and files that fit in one chunk.
      if (!payload.download_id || !payload.total || payload.total <= 1) {
        triggerDownload(payload.name, payload.content);
        return;
      }
      const total = payload.total;
      if (total > DOWNLOAD_MAX_CHUNKS) {
        viewerNote('download failed: too many chunks (' + total + ')');
        return;
      }
      const idx = payload.index;
      if (typeof idx !== 'number' || idx < 0 || idx >= total) {
        viewerNote('download failed: bad chunk index ' + idx + '/' + total);
        return;
      }
      const id = payload.download_id;
      let dl = pendingDownloads.get(id);
      if (!dl) {
        dl = { name: payload.name, total: total, chunks: new Map(), bytes: 0, timer: null };
        pendingDownloads.set(id, dl);
      }
      if (dl.timer) clearTimeout(dl.timer);
      dl.timer = setTimeout(() => { pendingDownloads.delete(id); }, DOWNLOAD_STALE_MS);
      if (!dl.chunks.has(idx)) {
        const bytes = b64ToBytes(payload.content);
        if (dl.bytes + bytes.length > DOWNLOAD_MAX_BYTES) {
          clearTimeout(dl.timer);
          pendingDownloads.delete(id);
          viewerNote('download aborted: exceeds cap');
          return;
        }
        dl.chunks.set(idx, bytes);
        dl.bytes += bytes.length;
      }
      if (dl.chunks.size !== dl.total) return;
      // Complete — concat in index order.
      clearTimeout(dl.timer);
      pendingDownloads.delete(id);
      const out = new Uint8Array(dl.bytes);
      let off = 0;
      for (let i = 0; i < dl.total; i++) {
        const c = dl.chunks.get(i);
        if (c) { out.set(c, off); off += c.length; }
      }
      triggerDownloadBytes(dl.name, out);
    }

    // ---- Keybar toggle (collapse / expand) ----
    // Some workflows want max terminal area — landscape, reading a long
    // log, etc. Toggle hides every key except the toggle itself; state
    // persists in localStorage. Refit after a change so xterm reclaims
    // the freed space.
    const KEYBAR_HIDDEN_KEY = 'reminal:keybarHidden';
    const keybar = document.getElementById('keybar');
    const keybarToggle = document.getElementById('keybar-toggle');
    function applyKeybarState() {
      const hidden = localStorage.getItem(KEYBAR_HIDDEN_KEY) === '1';
      keybar.classList.toggle('collapsed', hidden);
      keybarToggle.textContent = hidden ? '⌨' : '≡';
      keybarToggle.title = hidden ? 'Show key bar' : 'Hide key bar';
      // Collapsing/expanding the bar changes its height, so the terminal
      // box's height changes too — re-fit. Guard so a throw here cannot
      // abort the rest of the page (Connect, listeners) during bootstrap.
        try { TermSize.schedule(); } catch (_) {}
    }
    keybarToggle.addEventListener('click', (e) => {
      // A dock drag that just released synthesizes a click on this button
      // too — that was the session-switch gesture, not a hide-the-bar tap.
      if (e.timeStamp - sessdockLastDragEnd < 400) return;
      const hiddenNow = keybar.classList.contains('collapsed');
      localStorage.setItem(KEYBAR_HIDDEN_KEY, hiddenNow ? '0' : '1');
      applyKeybarState();
    });
    applyKeybarState();

    // ---- Jump to bottom pill ----
    // When the user scrolls up to read older output, new lines keep
    // arriving at the bottom (good — we don't yank them back). But once
    // they want to follow the live output again, swiping all the way back
    // is annoying on a phone. Show a floating button whenever they're not
    // at the bottom; tap to jump.
    const jumpBtn = document.getElementById('jump-bottom');
    function updateJumpBtn() {
      // term.buffer.active.viewportY is the top line of the viewport;
      // baseY is the top of the last screen. Equal → we're at bottom.
      const buf = term.buffer.active;
      if (buf.viewportY < buf.baseY) jumpBtn.classList.add('visible');
      else jumpBtn.classList.remove('visible');
    }
    term.onScroll(updateJumpBtn);
    term.onLineFeed(updateJumpBtn);
    jumpBtn.addEventListener('click', () => {
      term.scrollToBottom();
      jumpBtn.classList.remove('visible');
      term.focus();
    });

    // ---- Find in scrollback ----
    // xterm-addon-search highlights matches and scrolls them into view.
    // Bound to Cmd/Ctrl+F (desktop) and a "Find" key in the keybar
    // (mobile). Esc closes. Enter / Shift+Enter walk forward / back.
    const findbar = document.getElementById('findbar');
    const findInput = document.getElementById('find-input');
    const findCount = document.getElementById('find-count');
    const findOpts = {
      caseSensitive: false,
      wholeWord: false,
      regex: false,
      decorations: {
        matchBackground: '#1f6feb',
        activeMatchBackground: '#388bfd',
        matchOverviewRuler: '#1f6feb',
        activeMatchColorOverviewRuler: '#388bfd',
      },
    };
    if (searchAddon.onDidChangeResults) {
      searchAddon.onDidChangeResults((e) => {
        findCount.textContent = e.resultCount === 0 ? 'no matches'
          : (e.resultIndex + 1) + '/' + e.resultCount;
      });
    }
    function openFind() {
      findbar.classList.add('visible');
      findInput.focus();
      findInput.select();
    }
    function closeFind() {
      findbar.classList.remove('visible');
      searchAddon.clearDecorations();
      term.focus();
    }
    function findNext(reverse) {
      const q = findInput.value;
      if (!q) return;
      if (reverse) searchAddon.findPrevious(q, findOpts);
      else searchAddon.findNext(q, findOpts);
    }
    // ---- Font size +/− ----
    function setFontSize(px) {
      const clamped = Math.max(FONT_MIN, Math.min(FONT_MAX, px));
      term.options.fontSize = clamped;
      try { localStorage.setItem(FONT_KEY, String(clamped)); } catch (_) {}
      // Font size changes cell metrics but not the box size, so force a
      // fit (the box-unchanged guard would otherwise skip it).
      TermSize.sync();
    }
    document.getElementById('font-up-btn').addEventListener('click', () => {
      setFontSize(term.options.fontSize + 2);
    });
    document.getElementById('font-down-btn').addEventListener('click', () => {
      setFontSize(term.options.fontSize - 2);
    });

    // ---- Dynamic browser tab title ----
    // Without this, every reminal tab in the browser strip reads
    // "reminal" — impossible to tell sessions apart in a multi-tab world.
    // Use the saved name from the recents dropdown if set; otherwise the
    // raw session ID. Resets to bare "reminal" on disconnect.
    function refreshTabTitle() {
      if (!sessionId) {
        document.title = 'reminal';
        return;
      }
      const list = loadHistory();
      const entry = list.find(e => e.id === sessionId.toUpperCase());
      const label = entry?.name ? entry.name : sessionId.toUpperCase();
      document.title = 'reminal · ' + label;
    }

    // ---- Screen Wake Lock ----
    // While connected and watching long output, the phone screen would
    // otherwise dim and lock after the OS idle timeout — the WebSocket
    // also gets paused/killed and the user misses live output. Request
    // a screen wake lock when we connect; release on disconnect.
    // Auto-re-request on visibilitychange because iOS Safari drops the
    // lock when the tab loses visibility, even briefly.
    let wakeLock = null;
    async function acquireWakeLock() {
      if (!('wakeLock' in navigator)) return;
      try {
        wakeLock = await navigator.wakeLock.request('screen');
        wakeLock.addEventListener('release', () => { wakeLock = null; });
      } catch (_) { /* permission denied, low-power mode, etc. */ }
    }
    function releaseWakeLock() {
      if (wakeLock) { wakeLock.release().catch(() => {}); wakeLock = null; }
    }
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') {
        // Coming back to the app (e.g. after opening a session link in a
        // browser window) — re-read localStorage so a connection made in
        // another tab/window shows up in Recent without a manual restart.
        renderRecents();
        if (agentLive && !wakeLock) acquireWakeLock();
      }
    });

    // ---- Share session (QR + creds + pasteable messages) ----
    // Phone-only workflow: a viewer wants to invite a second person (or an
    // AI agent) without walking back to the host to type `reminal info`.
    const shareModal = document.getElementById('share-modal');
    const shareQrEl = document.getElementById('share-qr');
    let shareQrCode = null;
    let sharePinFromAgent = null; // { sid, pin }: asked for by Share, in memory only
    function sharePin() {
      return pinValue || (sharePinFromAgent && sharePinFromAgent.sid === sessionId && sharePinFromAgent.pin) || '';
    }
    function shareJoinURL() {
      const id = (sessionId || '').toUpperCase();
      const base = location.origin + '/?s=' + id;
      const pin = sharePin();
      return pin ? base + '#p=' + pin : base;
    }
    function shareHumanText() {
      const id = (sessionId || '').toUpperCase();
      const pin = sharePin();
      const url = shareJoinURL();
      let t = 'Join this reminal terminal:\n\n' + url;
      if (id && pin) t += '\n\nOr from a terminal:\nreminal connect ' + id + ' ' + pin;
      t += '\n\nAnyone with these details can type in the same shell.';
      return t;
    }
    function shareAgentText() {
      const id = (sessionId || '').toUpperCase();
      const pin = sharePin();
      const url = shareJoinURL();
      const pinLine = pin || '(host has not sent it yet — ask the user to run reminal info)';
      return [
        'You are an AI agent. Connect to this reminal session (a live shared terminal) and drive it.',
        '',
        'If reminal is not installed:',
        '  macOS / Linux:  curl -fsSL https://reminal.app/install.sh | sh',
        '  Windows:        irm https://reminal.app/install.ps1 | iex',
        '',
        'Then run this in a terminal that is NOT already inside reminal:',
        '  reminal connect ' + id + (pin ? ' ' + pin : ''),
        '',
        'That attaches your terminal to the remote shell. Type commands as you would locally. The host and other viewers see the same output.',
        '',
        'To leave without stopping the host, press Ctrl-].',
        '',
        'Browser join (same session):',
        '  ' + url,
        '',
        'Session: ' + id,
        'PIN: ' + pinLine,
        '',
        'Do not share these credentials unless the user asked you to.',
      ].join('\n');
    }
    async function copyShare(text, btn) {
      try {
        await navigator.clipboard.writeText(text);
      } catch (_) {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.left = '-9999px';
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand('copy'); } catch (_) {}
        ta.remove();
      }
      if (btn) {
        const original = btn.textContent;
        btn.textContent = 'Copied';
        setTimeout(() => { btn.textContent = original; }, 1500);
      }
    }
    function fillShare() {
      if (!sessionId) return;
      const id = sessionId.toUpperCase();
      const pin = sharePin();
      const url = shareJoinURL();
      const sessionEl = document.getElementById('share-session');
      const pinEl = document.getElementById('share-pin');
      const urlEl = document.getElementById('share-url-text');
      const pinBtn = document.querySelector('.share-copy-one[data-share="pin"]');
      sessionEl.textContent = id;
      urlEl.textContent = url;
      urlEl.title = url;
      if (pin) {
        pinEl.textContent = pin;
        pinEl.classList.remove('muted');
        if (pinBtn) pinBtn.disabled = false;
      } else {
        pinEl.textContent = 'Asking host…';
        pinEl.classList.add('muted');
        if (pinBtn) pinBtn.disabled = true;
      }
      shareQrEl.innerHTML = '';
      const qrSize = window.innerHeight < 640 ? 128 : (window.innerHeight < 740 ? 156 : 200);
      try {
        shareQrCode = new QRCode(shareQrEl, {
          text: url,
          width: qrSize, height: qrSize,
          colorDark: '#000000', colorLight: '#ffffff',
          correctLevel: QRCode.CorrectLevel.L,
        });
      } catch (e) {
        shareQrEl.textContent = 'QR generation failed: ' + e;
      }
    }
    function openShare() {
      if (!sessionId) return;
      fillShare();
      if (!sharePin() && typeof sendWin === 'function') sendWin('host_info', { pin: true });
      // The share sheet (moments.js) holds the invite beside the pictures and clips.
      if (window.reminalMoments) { reminalMoments.open('home'); return; }
      shareModal.style.display = 'flex';
    }
    function closeShare() {
      shareModal.style.display = 'none';
    }
    document.getElementById('share-btn').addEventListener('click', openShare);
    document.getElementById('share-close').addEventListener('click', closeShare);
    shareModal.addEventListener('click', (e) => { if (e.target === shareModal) closeShare(); });
    document.querySelectorAll('.share-copy-one').forEach((btn) => {
      btn.addEventListener('click', () => {
        const kind = btn.getAttribute('data-share');
        if (kind === 'session') copyShare((sessionId || '').toUpperCase(), btn);
        else if (kind === 'pin') { const p = sharePin(); if (p) copyShare(p, btn); }
        else if (kind === 'url') copyShare(shareJoinURL(), btn);
      });
    });
    document.getElementById('share-copy-human').addEventListener('click', (e) => {
      copyShare(shareHumanText(), e.currentTarget);
    });
    document.getElementById('share-copy-agent').addEventListener('click', (e) => {
      copyShare(shareAgentText(), e.currentTarget);
    });
    // navigator.share invokes the native system share sheet on mobile —
    // AirDrop / Messages / Mail / Notes on iOS, the equivalent on
    // Android. Way more natural than "copy link, switch apps, paste."
    // Hidden by default since desktop browsers without the API would
    // show a broken button.
    if (typeof navigator.share === 'function') {
      const shareNativeBtn = document.getElementById('share-native');
      shareNativeBtn.style.display = 'inline-block';
      shareNativeBtn.addEventListener('click', async () => {
        try {
          await navigator.share({
            title: 'reminal session ' + ((sessionId || '').toUpperCase()),
            text: shareHumanText(),
          });
        } catch (_) { /* user cancelled the sheet — silent */ }
      });
    }

    // ---- Mobile copy / paste ----
    // iOS Safari lets you select text in xterm with long-press but the
    // OS Copy/Paste menu doesn't reach the canvas reliably. These two
    // buttons go through the Web Clipboard API explicitly, which is
    // gated on a user gesture (the tap satisfies that). Falls back to
    // a status line on failure (permission denied, no selection, etc.).
    document.getElementById('copy-btn').addEventListener('click', async () => {
      const sel = term.getSelection();
      if (!sel) {
        viewerNote('nothing selected — long-press to select first');
        return;
      }
      try {
        await navigator.clipboard.writeText(sel);
        viewerNote('copied ' + sel.length + ' chars');
      } catch (e) {
        viewerNote('copy failed: ' + e);
      }
    });
    // Paste routes to the focused window when the window keyboard is up,
    // otherwise to the terminal — and refocuses whichever input owns the
    // soft keyboard. The old handler always sendInput()'d into the terminal
    // and then term.focus()'d, so pasting while typing into a window yanked
    // focus off the window, collapsed the keyboard, and the paste never
    // reached the window. mousedown/touchend mirror the keybar buttons so
    // the tap itself never blurs the hidden window-key input.
    const pasteBtn = document.getElementById('paste-btn');
    const doPaste = async () => {
      let toWindow = winKbdActive();
      try {
        const text = await navigator.clipboard.readText();
        if (text) toWindow = typeToTarget(text);
      } catch (e) {
        viewerNote('paste failed: ' + e);
      }
      (toWindow ? wkey : term).focus();
    };
    pasteBtn.addEventListener('mousedown', (e) => { if (winKbdActive()) e.preventDefault(); });
    pasteBtn.addEventListener('touchend', (e) => { if (!winKbdActive()) return; e.preventDefault(); doPaste(); });
    pasteBtn.addEventListener('click', doPaste);

    document.getElementById('find-btn').addEventListener('click', openFind);
    document.getElementById('find-prev').addEventListener('click', () => findNext(true));
    document.getElementById('find-next').addEventListener('click', () => findNext(false));
    document.getElementById('find-close').addEventListener('click', closeFind);
    findInput.addEventListener('input', () => findNext(false));
    findInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); findNext(e.shiftKey); }
      else if (e.key === 'Escape') { e.preventDefault(); closeFind(); }
    });
    // Desktop keyboard shortcut. xterm.js's customKeyEventHandler runs
    // before the terminal processes the key, so we can intercept Cmd/Ctrl+F.
    term.attachCustomKeyEventHandler((ev) => {
      if (ev.type === 'keydown' && (ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === 'f') {
        ev.preventDefault();
        openFind();
        return false;
      }
      return true;
    });

    // ---- File upload: drag-drop + button picker ----
    // Slices each file into UPLOAD_CHUNK_BYTES pieces, encrypts each
    // chunk independently, and sends as a sequence of TypeUpload frames
    // sharing one upload_id. The host buffers in pendingUploads and
    // writes to ~/Downloads/reminal/ once every chunk arrives.
    // Parse human TTL string ("5m", "1h", "1d", "never") to seconds.
    // 0 means "keep forever". Invalid input → default to keep forever
    // (silent — explicit confirmation already happened in the prompt).
    function parseTTL(input) {
      const s = (input || '').trim().toLowerCase();
      if (!s || s === 'never' || s === '0' || s === 'n') return 0;
      const m = s.match(/^(\d+)([smhd])$/);
      if (!m) return 0;
      const n = parseInt(m[1], 10);
      switch (m[2]) {
        case 's': return n;
        case 'm': return n * 60;
        case 'h': return n * 3600;
        case 'd': return n * 86400;
      }
      return 0;
    }
    // Base64-encode an arbitrary Uint8Array slice without copying.
    // Stride kept small (0x1000 = 4096 args per `apply`) because iOS
    // Safari throws "Maximum call stack size exceeded" once the argument
    // count gets near 2^15 — passing a full 32K-byte view tripped that.
    function bytesToBase64(bytes) {
      let binary = '';
      const stride = 0x1000;
      for (let i = 0; i < bytes.length; i += stride) {
        binary += String.fromCharCode.apply(null, bytes.subarray(i, i + stride));
      }
      return btoa(binary);
    }
    // Chunked upload. Cloudflare Durable Objects cap WS messages at 1 MiB
    // on the wire — after base64 + AES-GCM + JSON envelope, ~256 KB of
    // raw bytes per chunk keeps every frame comfortably under the cap.
    // Each chunk carries the same upload_id; the host reassembles in
    // order once all `total` chunks have arrived.
    const UPLOAD_CHUNK_BYTES = 256 * 1024;
    const UPLOAD_MAX_BYTES = 100 * 1024 * 1024; // 100 MB hard cap
    // An upload's session, as it began: its socket and its key. A file is
    // sent to the session it was dropped on, sealed for it — never, after a
    // switch, the rest of it to the next one, sealed for that one.
    function uploadLink() { return authed && cryptoKey && ws && ws.readyState === WebSocket.OPEN ? { sock: ws, key: cryptoKey } : null; }
    function uploadLinkLive(l) { return !!l && ws === l.sock && cryptoKey === l.key && l.sock.readyState === WebSocket.OPEN; }
    // uploadLinkWhenBack is the session's link for an upload, waited for when
    // it is down: a phone drops it while its file picker is open, and the
    // page is still reconnecting when the picked file comes back. The redial
    // is nudged (fastReconnect) and the same session waited for: told the
    // moment it is live again (startSessionFlow), or that it cannot be — left
    // (a switch away, a disconnect) or ended (refused, closed) — or given up
    // after UPLOAD_RELINK_MS. { link } to send on, or { why } it is not:
    // 'never' (not connected here), 'left', 'ended', 'timeout'.
    const UPLOAD_RELINK_MS = 30000;
    // settleUploadWaiters tells every waiting upload what became of its link:
    // true, it may be live (each checks it is its own session's); 'left' or
    // 'ended', it will not be.
    function settleUploadWaiters(how) {
      for (const w of [...uploadWaiters]) w(how);
    }
    function uploadLinkWhenBack() {
      // A link there now is used at once, as before there was any waiting:
      // only one that is down is waited for.
      const now = uploadLink();
      if (now) return Promise.resolve({ link: now });
      const sid = sessionId;
      if (!sid || !helloShown || manualClose || fatal) return Promise.resolve({ why: 'never' }); // never up here: nothing to wait for
      viewerNote('reconnecting to ' + sid + ' to upload…');
      return new Promise(resolve => {
        const done = (r) => { uploadWaiters.delete(waiter); clearTimeout(timer); resolve(r); };
        const waiter = (how) => {
          if (how !== true) return done({ why: how });
          if (sessionId === sid && inputLive()) done({ link: uploadLink() }); // else not its link yet
        };
        const timer = setTimeout(() => done({ why: 'timeout' }), UPLOAD_RELINK_MS);
        uploadWaiters.add(waiter);
        fastReconnect();
      });
    }
    // The toast at the foot of the terminal while files upload: one for a
    // whole batch. A spinner and a bar while the bytes go out, "Saving" until
    // the machine says it wrote the file, then where it landed. Done fades
    // after 4 s, a stopped upload after 6 s; a file refused for itself (too
    // large) stays until dismissed.
    function uploadToast(count) {
      const box = document.getElementById('upload-toasts');
      const el = document.createElement('div');
      el.className = 'up-toast';
      el.setAttribute('role', 'status');
      el.innerHTML = '<div class="up-ico"><div class="up-spin"></div></div>' +
        '<div class="up-head"><span class="up-title"></span><span class="up-count"></span></div>' +
        '<div class="up-bar"><div class="up-fill"></div></div><div class="up-meta"></div>';
      if (box) box.replaceChildren(el);
      const q = s => el.querySelector(s);
      const of = i => (count > 1 ? (i + 1) + ' of ' + count : '');
      let closed = false;
      const close = () => {
        if (closed) return;
        closed = true;
        el.classList.add('gone');
        setTimeout(() => el.remove(), 400);
      };
      // pct null: a moving bar, for a step with no measure of its own.
      const set = (title, n, meta, pct) => {
        q('.up-title').textContent = title;
        q('.up-count').textContent = n;
        q('.up-meta').textContent = meta;
        const fill = q('.up-fill');
        fill.classList.toggle('wait', pct === null);
        if (pct !== null) fill.style.width = Math.min(100, pct).toFixed(1) + '%';
      };
      const end = (kind, title, meta) => {
        el.classList.add(kind);
        const s = q('.up-skip'); if (s) s.remove(); // the last word says what was skipped
        q('.up-ico').innerHTML = kind === 'ok'
          ? '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="7.5" fill="#238636"/><path d="M4.5 8.2l2.3 2.3 4.7-4.9" stroke="#fff" stroke-width="1.7" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>'
          : '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="7.5" fill="#da3633"/><path d="M8 4.3v4.6M8 11.2v.2" stroke="#fff" stroke-width="1.8" fill="none" stroke-linecap="round"/></svg>';
        set(title, '', meta, 100);
      };
      return {
        reading(name, i) { set('Reading ' + name, of(i), 'preparing…', null); },
        sending(name, i, sent, size, secsLeft) {
          set('Uploading ' + name, of(i),
            humanBytes(sent) + ' of ' + humanBytes(size) + ' · ' + Math.floor(100 * sent / size) + '%' +
              (secsLeft > 1 ? ' · ' + secsLeft + ' s left' : ''),
            100 * sent / size);
        },
        saving(what) { set('Saving ' + what + ' on the machine', '', 'sent · waiting for the machine', null); },
        // A file refused for itself mid-batch, said at once and kept in
        // view while the rest go on.
        skip(text) {
          let s = q('.up-skip');
          if (!s) { s = document.createElement('div'); s.className = 'up-skip'; el.appendChild(s); }
          s.textContent = 'Skipped: ' + text;
        },
        done(title, meta) { end('ok', title, meta); setTimeout(close, 4000); },
        failed(title, meta, stays) {
          end('err', title, meta);
          if (!stays) { setTimeout(close, 6000); return; }
          const x = document.createElement('button');
          x.type = 'button';
          x.className = 'up-dismiss';
          x.textContent = 'Dismiss';
          x.addEventListener('click', close);
          el.appendChild(x);
        },
      };
    }
    // Where an upload landed, said once the batch is done: the file's path,
    // or for several their folder.
    function uploadLanded(paths, count, extra) {
      const dir = p => p.replace(/[\\/][^\\/]*$/, '/');
      const where = paths.length === 1 ? paths[0] : (paths.length ? dir(paths[0]) : '');
      return (where ? 'Saved to ' + where + ' · ' : '') +
        (count === 1 ? 'path' : 'paths') + ' typed at the cursor' + (extra || '');
    }
    // uploadFile sends file to the session of link (the current one when
    // none is given). false: none sent, nor will any more be — that session
    // left, not connected, or a send failed; a batch stops there. A file
    // refused for itself (too large) is said, and the rest go on.
    //
    // ui is the batch's toast ({ toast, i, skipped, paths }); without one the
    // file gets a toast of its own and says it is done itself.
    async function uploadFile(file, ttlSeconds, link, ui) {
      link = link || uploadLink();
      if (!link) {
        alert('Not connected — connect to a session first.');
        return false;
      }
      const solo = !ui;
      if (solo) ui = { toast: uploadToast(1), i: 0, skipped: [], paths: [], acks: [], link };
      const toast = ui.toast;
      if (file.size > UPLOAD_MAX_BYTES) {
        const why = humanBytes(file.size) + '; uploads are capped at ' + humanBytes(UPLOAD_MAX_BYTES) + ' per file.';
        ui.skipped.push(file.name + ' is too large (' + why.replace(/\.$/, '') + ')');
        if (solo) toast.failed(file.name + ' is too large', why, true);
        else toast.skip(ui.skipped.join('; ') + '.');
        return;
      }
      toast.reading(file.name, ui.i);
      const buf = new Uint8Array(await file.arrayBuffer());
      const stopped = (sent, total) => {
        const rest = (sent ? ' stopped after ' + sent + ' of ' + total + ' chunk' + (total === 1 ? '' : 's') : ' not sent');
        toast.failed('Upload stopped: you left the session', file.name + rest + '. Reconnect and upload again.');
      };
      if (!uploadLinkLive(link)) { stopped(0, 0); return false; }
      const total = Math.max(1, Math.ceil(buf.length / UPLOAD_CHUNK_BYTES));
      const uploadId =
        (self.crypto && self.crypto.randomUUID && self.crypto.randomUUID()) ||
        ('u-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2));
      // Remember the upload_id so the agent's upload_ack handler knows
      // we're the originator and can auto-type the path into our shell.
      pendingUploadIDs.add(uploadId);
      // Safety net: if the agent crashes / disconnects between
      // accepting the upload and sending its ack, the set entry
      // would sit forever and leak memory across an unbounded
      // number of failed uploads. Stale-clean after 5 min — well
      // past any legitimate round-trip even on a slow connection.
      setTimeout(() => pendingUploadIDs.delete(uploadId), 5 * 60 * 1000);
      // The machine says it wrote the file (upload_ack): listened for from
      // before the first chunk (a quick machine answers before the last
      // bytes have drained here) and heard in the background, so a batch
      // goes on sending as it always has. null: the session was left, the
      // send failed, or two minutes passed after the last byte.
      let sentAt = 0, watch = 0;
      const landed = new Promise(res => {
        const finish = p => { clearInterval(watch); uploadAckWaiters.delete(uploadId); res(p); };
        uploadAckWaiters.set(uploadId, finish);
        watch = setInterval(() => {
          if (!uploadLinkLive(link) || (sentAt && performance.now() - sentAt > 120000)) finish(null);
        }, 500);
      });
      const dropAck = () => { const f = uploadAckWaiters.get(uploadId); if (f) f(null); };
      // Progress is what has left the browser: the frames handed to the
      // socket less what it still holds (bufferedAmount), not the chunks
      // queued, which would read 100% at once on a slow link.
      const sock = link.sock, held = () => sock.bufferedAmount || 0, held0 = held(), t0 = performance.now();
      let handed = 0, perChunk = 0;
      const showSent = () => {
        if (!perChunk) return;
        const out = Math.max(0, handed - Math.max(0, held() - held0));
        const sent = Math.min(file.size, file.size * out / (perChunk * total));
        const rate = sent / Math.max(0.05, (performance.now() - t0) / 1000);
        toast.sending(file.name, ui.i, sent, Math.max(1, file.size), rate > 0 ? Math.round((file.size - sent) / rate) : 0);
      };
      const ticker = setInterval(showSent, 150);
      try {
        for (let i = 0; i < total; i++) {
          const start = i * UPLOAD_CHUNK_BYTES;
          const end = Math.min(start + UPLOAD_CHUNK_BYTES, buf.length);
          const slice = buf.subarray(start, end);
          const payload = JSON.stringify({
            upload_id: uploadId,
            index: i,
            total,
            name: file.name,
            content: bytesToBase64(slice),
            ttl_seconds: ttlSeconds || 0,
          });
          const ct = await encrypt(link.key, new TextEncoder().encode(payload));
          if (!uploadLinkLive(link)) {
            pendingUploadIDs.delete(uploadId); dropAck();
            stopped(i, total);
            return false;
          }
          const frame = JSON.stringify({ type: 'upload', data: ct });
          link.sock.send(frame);
          handed += frame.length;
          if (!perChunk) perChunk = frame.length;
          showSent();
          // Yield to the event loop between chunks so the UI stays
          // responsive and the WS send buffer can drain.
          if (total > 1 && i < total - 1) await new Promise(r => setTimeout(r, 0));
        }
        // The last bytes out of the socket before "Saving".
        while (uploadLinkLive(link) && held() > held0) await new Promise(r => setTimeout(r, 100));
      } catch (e) {
        // Drop the pending entry: we know the upload won't complete,
        // and waiting for the 5-min stale-clean would leak the slot
        // for that whole window. The auto-paste won't fire anyway —
        // an upload_ack racing in afterwards lands on an empty set.
        pendingUploadIDs.delete(uploadId); dropAck();
        toast.failed('Upload failed: ' + file.name, 'It could not be sent (' + e + '). Try again.');
        return false;
      } finally {
        clearInterval(ticker);
      }
      if (!uploadLinkLive(link)) { pendingUploadIDs.delete(uploadId); dropAck(); stopped(total, total); return false; }
      sentAt = performance.now();
      ui.acks.push({ name: file.name, landed });
      if (solo) uploadBatchDone(ui, 1);
      return true;
    }
    // A batch's last word on its toast, once the machine has said where each
    // file landed (or not): what landed, what was refused, what it never
    // confirmed.
    async function uploadBatchDone(ui, count, extra) {
      const acks = ui.acks;
      if (acks.length) ui.toast.saving(acks.length === 1 ? acks[0].name : acks.length + ' files');
      const paths = await Promise.all(acks.map(a => a.landed));
      ui.paths = paths.filter(Boolean);
      const unconfirmed = paths.filter(p => p === null).length;
      const sent = count - ui.skipped.length;
      if (unconfirmed && !uploadLinkLive(ui.link)) {
        ui.toast.failed('Upload stopped: you left the session',
          (unconfirmed === 1 ? 'A file was' : unconfirmed + ' files were') + ' sent, but the machine had not said it saved ' +
            (unconfirmed === 1 ? 'it' : 'them') + '. Reconnect and check ~/Downloads/reminal/.', ui.skipped.length > 0);
      } else if (ui.skipped.length || unconfirmed) {
        const said = ui.skipped.slice();
        if (unconfirmed) said.push((unconfirmed === 1 ? 'one file was' : unconfirmed + ' files were') + ' sent but not confirmed: check ~/Downloads/reminal/ on the machine');
        const title = ui.skipped.length ? (sent ? sent + ' of ' + count + ' files uploaded' : 'Nothing uploaded') : 'Not confirmed by the machine';
        ui.toast.failed(title, said.join('; ') + '.', ui.skipped.length > 0);
      } else if (count === 1) {
        ui.toast.done('Uploaded ' + acks[0].name, uploadLanded(ui.paths, 1, extra));
      } else {
        ui.toast.done(count + ' files uploaded', uploadLanded(ui.paths, count, extra));
      }
    }
    // Prompt once per upload batch — same TTL applies to every selected file.
    async function uploadBatch(files) {
      if (files.length === 0) return;
      const ttlStr = prompt(
        'Upload ' + files.length + ' file' + (files.length === 1 ? '' : 's') +
          ' to host\'s ~/Downloads/reminal/.\n\nAuto-delete after? (e.g. 5m, 1h, 1d, or "never")',
        '1h'
      );
      if (ttlStr === null) return; // user cancelled
      const ttl = parseTTL(ttlStr);
      // Every file to the session they were dropped on: the batch stops
      // once it has been left. Its link down (a phone's file picker dropped
      // it), it waits for the session to be back.
      const wanted = sessionId;
      const { link, why } = await uploadLinkWhenBack();
      if (!link) {
        if (why === 'left') viewerNote('Not uploaded: you left ' + wanted + '.'); // as asked: no alert over it
        else if (why === 'ended') alert('Not uploaded: the connection to ' + wanted + ' ended.');
        else if (why === 'timeout') alert('Not uploaded: could not reconnect to ' + wanted + '. Try again once it is back.');
        else alert('Not connected — connect to a session first.');
        return;
      }
      const ui = { toast: uploadToast(files.length), i: 0, skipped: [], paths: [], acks: [], link };
      for (const f of files) {
        if ((await uploadFile(f, ttl, link, ui)) === false) return;
        ui.i++;
      }
      uploadBatchDone(ui, files.length);
    }
    const uploadInput = document.getElementById('upload-input');
    document.getElementById('upload-btn').addEventListener('click', () => uploadInput.click());
    uploadInput.addEventListener('change', async () => {
      await uploadBatch(Array.from(uploadInput.files));
      uploadInput.value = '';
    });
    // Desktop drag-and-drop: drop files anywhere on the terminal area.
    const termWrap = document.getElementById('terminal-wrap');
    ['dragenter', 'dragover'].forEach(ev => termWrap.addEventListener(ev, (e) => {
      e.preventDefault();
      termWrap.style.outline = '2px dashed #58a6ff';
    }));
    ['dragleave', 'drop'].forEach(ev => termWrap.addEventListener(ev, () => {
      termWrap.style.outline = '';
    }));
    termWrap.addEventListener('drop', async (e) => {
      e.preventDefault();
      if (!e.dataTransfer) return;
      await uploadBatch(Array.from(e.dataTransfer.files));
    });
    // Paste-to-upload: phone screenshot in the clipboard → paste into the
    // page → file lands on the host. Default 1h TTL (no prompt — would
    // break the paste reflex). Text/non-image pastes go to xterm.js's
    // normal paste handling (this listener only intercepts image kinds).
    document.addEventListener('paste', async (e) => {
      if (!authed || !e.clipboardData) return;
      const imageFiles = [];
      for (const item of e.clipboardData.items) {
        if (item.kind === 'file' && item.type.startsWith('image/')) {
          const f = item.getAsFile();
          if (f) imageFiles.push(f);
        }
      }
      if (imageFiles.length === 0) return; // not an image paste
      e.preventDefault();
      const DEFAULT_PASTE_TTL = 3600; // 1h — phone-pasted screenshots are mostly ephemeral
      const link = uploadLink(); // every image to the session it was pasted into
      if (!link) { viewerNote('pasted image not uploaded: not connected to the session'); return; }
      const ui = { toast: uploadToast(imageFiles.length), i: 0, skipped: [], paths: [], acks: [], link };
      for (const f of imageFiles) {
        // Phone clipboard images often come in as "image.png" with no
        // useful name. Stamp a timestamp so concurrent pastes don't collide.
        const stamped = f.name && f.name !== 'image.png'
          ? f.name
          : 'paste-' + new Date().toISOString().replace(/[:.]/g, '-') + '.png';
        const renamed = new File([f], stamped, { type: f.type });
        if ((await uploadFile(renamed, DEFAULT_PASTE_TTL, link, ui)) === false) return;
        ui.i++;
      }
      uploadBatchDone(ui, imageFiles.length, ' · auto-delete in 1h');
    });

    // ---- Viewport / layout sizing (single source of truth) ----
    // Layout itself is 100% CSS: body is a flex column (header / terminal
    // / keybar) sized to the viewport. JS does exactly two things, and
    // neither can feed back into the other:
    //
    //   1. applyViewportHeight() — iOS only: pin body's height to the
    //      visual viewport (keyboard overlays, 100dvh does not shrink).
    //      Never translateY(offsetTop). That chase is a control loop
    //      against the browser's pan.
    //
    //   2. TermSize.sync() — report the wrap to the agent, then paint
    //      min(wrap, PTY). The box's size is owned entirely by flexbox;
    //      we only size the canvas inside it.
    //
    // That structural guarantee — no second sizing authority, observer
    // output can't become observer input — is what kills the old
    // measure→pin→resize→measure flicker loop for good.
    // applyViewportHeight / IS_IOS live above TermSize so keybar init
    // cannot hit a TDZ. Listeners stay here, after TermSize exists.

    // Viewport changes (keyboard open/close, rotation, desktop resize)
    // update body height and re-sync. visualViewport is the accurate
    // signal on mobile; window.resize covers browsers without it.
    // visualViewport.scroll is NOT a size signal — listening and writing
    // transform/scroll in response is the infinite-scroll loop.
    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', relayout);
    }
    window.addEventListener('scroll', () => {
      if (window.scrollX || window.scrollY) window.scrollTo(0, 0);
    }, { passive: true });
    window.addEventListener('resize', relayout);
    window.addEventListener('orientationchange', relayout);
    // The box changing size (header wrapping, keybar collapse, height
    // re-pin) is the authoritative "re-fit now" signal. Safe to observe
    // the very element we fit, because fit() can't change its size.
    if (typeof ResizeObserver !== 'undefined') {
      new ResizeObserver(scheduleFit).observe(document.getElementById('terminal-wrap'));
    }
    // Safety net for missed viewport events. Android Chrome / Samsung
    // Internet routinely DROP visualViewport.resize after a keyboard
    // CLOSE — the box grows back but xterm's grid stays stuck at the
    // keyboard-open row count, leaving an empty band below the content.
    // The keybar is the reliable tell here: it's the last flex child, so
    // its top edge moves the instant the layout reflows around the
    // keyboard — far more dependable than the viewport resize events. So
    // every tick we re-pin height and refit whenever EITHER the keybar's
    // measured top moved OR the box drifted from xterm's grid. Guarded so
    // it's a no-op when nothing changed (can't churn or loop); it just
    // guarantees the grid matches the visible box within ~0.3s no matter
    // which events fired. Paused while the tab is hidden.
    let lastKbTop = -1;
    setInterval(() => {
      if (document.hidden) return;
      applyViewportHeight();
      const kbTop = keybar ? Math.round(keybar.getBoundingClientRect().top) : 0;
      const wrap = document.getElementById('terminal-wrap');
      const boxDrift = TermSize.boxDrift(wrap);
      if (kbTop !== lastKbTop || boxDrift) {
        lastKbTop = kbTop;
        scheduleSize();
      }
    }, 300);
    // Keyboard open/close toggles focus on xterm's hidden textarea,
    // usually before (or instead of) firing visualViewport.resize on
    // mobile — refit on focus change for a faster response than the poll.
    document.addEventListener('focusout', () => setTimeout(relayout, 100), true);
    document.addEventListener('focusin', () => setTimeout(relayout, 100), true);
    // Initial layout.
    applyViewportHeight();
    requestAnimationFrame(() => TermSize.sync());
    // Once more when the terminal font has loaded: a wrap measured with the
    // fallback font's cell width reports the wrong column count, and the
    // PTY would be laid out for a screen this one is not.
    if (document.fonts && document.fonts.ready) {
      document.fonts.ready.then(() => { try { TermSize.schedule(); } catch (_) {} });
    }

    // Tap-to-focus: on mobile, tapping an empty area of the terminal
    // wrapper should always open the soft keyboard, even if xterm.js
    // didn't see the tap as a cursor-positioning gesture.
    document.getElementById('terminal-wrap').addEventListener('click', (e) => {
      // Skip if user clicked on jump-bottom or findbar controls.
      if (e.target.closest('#jump-bottom, #findbar')) return;
      // Skip the synthetic click that follows a selection gesture (long-press,
      // handle drag, or tap-to-dismiss) — otherwise dismissing a selection
      // would pop the keyboard open.
      if (suppressFocusClick) { suppressFocusClick = false; return; }
      term.focus();
    });

    // ---- Mobile text selection (long-press + draggable handles) ----
    // xterm's built-in selection is mouse-drag only; on a touchscreen you get
    // at most a browser word-select with no way to extend across lines. This
    // adds native-feeling selection: long-press a word to select it and reveal
    // two teardrop grips, drag either grip to grow the selection (auto-scrolling
    // through scrollback near the edges), then Copy. We drive xterm's own
    // selection API so the highlight renders normally and getSelection() (used
    // by the keybar Copy button) keeps working.
    let suppressFocusClick = false;
    (function initTouchSelection() {
      const hStart = document.getElementById('sel-handle-start');
      const hEnd = document.getElementById('sel-handle-end');
      const toolbar = document.getElementById('sel-toolbar');
      const copyBtn = document.getElementById('sel-copy');
      const allBtn = document.getElementById('sel-all');

      // Selecting text must not summon the soft keyboard: you selected text to
      // COPY it, not to type, and on a phone the keyboard covers the output you
      // were reading.
      //
      // Why it came up at all: Android keeps focus on
      // xterm's hidden textarea after you collapse the keyboard, so a long-press
      // blurs a still-focused input and focus is handed back a moment later --
      // and refocusing an input is what re-opens the keyboard. xterm itself is
      // innocent; select() and clearSelection() fire no focus events at all.
      //
      // So drop focus before the selection starts, and only when the keyboard is
      // already down: then there is nothing to hand back and nothing to re-open.
      // With the keyboard up we leave focus alone, since collapsing it would be
      // the same bug in reverse.
      //
      // Known and accepted: the keyboard can still flicker briefly, because the
      // hand-back races this blur. Two attempts to close that race -- a timed
      // no-refocus window, then one tied to the selection's lifetime -- did not
      // remove it and only added state to a file that has plenty, so they were
      // dropped in favour of the small fix that does the useful part.
      const kbdIsOpen = () => {
        const vv = window.visualViewport;
        return !!vv && (window.innerHeight - vv.height) > 150;
      };
      function dropFocusIfKeyboardDown() {
        if (kbdIsOpen()) return;          // keyboard is up: leave focus alone
        const el = document.activeElement;
        if (el && el.classList && el.classList.contains('xterm-helper-textarea')) el.blur();
      }

      let active = false;                 // selection mode on
      let sel = null;                     // ordered {start:{col,row}, end:{col,row}} in ABSOLUTE buffer coords

      const screenEl = () => term.element && term.element.querySelector('.xterm-screen');

      // Cell size in CSS px. Prefer xterm's own measurement; fall back to the
      // screen element's rect / grid so we never divide by a stale value.
      function cellDims() {
        const d = term._core && term._core._renderService && term._core._renderService.dimensions;
        const c = d && d.css && d.css.cell;
        if (c && c.width > 0 && c.height > 0) return { w: c.width, h: c.height };
        const s = screenEl();
        if (s) {
          const r = s.getBoundingClientRect();
          if (r.width > 0) return { w: r.width / term.cols, h: r.height / term.rows };
        }
        return { w: 9, h: 17 };
      }

      // Map a client point to an absolute buffer cell, clamped to the grid.
      function pointToCell(clientX, clientY) {
        const s = screenEl();
        if (!s) return { col: 0, row: term.buffer.active.viewportY };
        const rect = s.getBoundingClientRect();
        const { w, h } = cellDims();
        let col = Math.round((clientX - rect.left) / w);          // round: nearest edge, like a caret
        let vrow = Math.floor((clientY - rect.top) / h);
        col = Math.max(0, Math.min(term.cols, col));
        vrow = Math.max(0, Math.min(term.rows - 1, vrow));
        return { col, row: term.buffer.active.viewportY + vrow };
      }

      // Client pixel of a cell's left edge / top, plus its visible viewport row.
      function cellToPoint(col, row) {
        const s = screenEl();
        const rect = s.getBoundingClientRect();
        const { w, h } = cellDims();
        const vrow = row - term.buffer.active.viewportY;
        return { x: rect.left + col * w, y: rect.top + vrow * h, h, vrow };
      }

      function cellCmp(a, b) { return a.row - b.row || a.col - b.col; }

      // Push sel into xterm so the highlight renders + getSelection() works.
      function applySelection() {
        if (!sel) return;
        if (cellCmp(sel.start, sel.end) > 0) { const t = sel.start; sel.start = sel.end; sel.end = t; }
        let len = (sel.end.row - sel.start.row) * term.cols + (sel.end.col - sel.start.col);
        if (len < 1) len = 1;
        term.select(sel.start.col, sel.start.row, len);
        layout();
      }

      // Position the two grips + the copy bar over the current selection.
      function layout() {
        if (!active || !sel) return;
        const rows = term.rows, vy = term.buffer.active.viewportY;
        const s = cellToPoint(sel.start.col, sel.start.row);
        const e = cellToPoint(sel.end.col, sel.end.row);
        // start grip hangs just ABOVE the first cell; end grip just BELOW the last
        placeHandle(hStart, s, s.vrow >= 0 && s.vrow < rows, -22);
        placeHandle(hEnd, e, e.vrow >= 0 && e.vrow < rows, e.h - 18);
        // The toolbar sits clear of both grips. Centred on the start grip
        // with its bottom edge ten pixels above the row, it covered the grip
        // itself — exactly where a finger lands to move the start — and the
        // start could never be changed. Above the start grip with a gap
        // now; when that would run into the header, below the end grip.
        const GRIP = 22, GAP = 12, HEADER = 48;
        toolbar.classList.add('visible');
        const th = toolbar.offsetHeight || 36;
        // Held inside the screen by its real width (Copy · Select all · Meme
        // is wider than the 120px this once assumed, and ran off the left
        // edge for a selection that starts there), 8px from either side.
        const half = (toolbar.offsetWidth || 120) / 2 + 8;
        const clampX = x => Math.max(half, Math.min(window.innerWidth - half, x));
        const startOn = s.vrow >= 0 && s.vrow < rows, endOn = e.vrow >= 0 && e.vrow < rows;
        if (startOn && s.y - GRIP - GAP - th >= HEADER) {
          toolbar.style.transform = 'translate(-50%, -100%)';
          toolbar.style.left = clampX(s.x) + 'px';
          toolbar.style.top = (s.y - GRIP - GAP) + 'px';
        } else if (endOn) {
          toolbar.style.transform = 'translate(-50%, 0)';
          toolbar.style.left = clampX(e.x) + 'px';
          toolbar.style.top = (e.y + e.h + GRIP + GAP) + 'px';
        } else if (startOn) {
          toolbar.style.transform = 'translate(-50%, 0)';
          toolbar.style.left = clampX(s.x) + 'px';
          toolbar.style.top = (s.y + s.h + GAP) + 'px';
        } else {
          toolbar.classList.remove('visible');
        }
        // And never below the visible screen (the keyboard, the key bar).
        if (toolbar.classList.contains('visible')) {
          const r = toolbar.getBoundingClientRect();
          const vh = window.visualViewport ? window.visualViewport.height : window.innerHeight;
          if (r.bottom > vh - 8) toolbar.style.top = (parseFloat(toolbar.style.top) - (r.bottom - (vh - 8))) + 'px';
        }
      }
      function placeHandle(el, pt, visible, yOff) {
        if (!visible) { el.classList.remove('visible'); return; }
        el.style.left = pt.x + 'px';
        el.style.top = (pt.y + yOff) + 'px';
        el.classList.add('visible');
      }

      // Word (run of non-whitespace) around a cell; single cell if on a space.
      function wordAt(cell) {
        const line = term.buffer.active.getLine(cell.row);
        const text = line ? line.translateToString(false) : '';
        const col = cell.col;
        if (col >= text.length || /\s/.test(text[col] || ' ')) {
          return { start: { col, row: cell.row }, end: { col: col + 1, row: cell.row } };
        }
        let s = col, e = col + 1;
        while (s > 0 && /\S/.test(text[s - 1])) s--;
        while (e < text.length && /\S/.test(text[e])) e++;
        return { start: { col: s, row: cell.row }, end: { col: e, row: cell.row } };
      }

      function enterSelection(cell) {
        active = true;
        sel = wordAt(cell);
        applySelection();
        if (navigator.vibrate) { try { navigator.vibrate(12); } catch (_) {} }
      }
      function clearSelection() {
        active = false; sel = null;
        term.clearSelection();
        hStart.classList.remove('visible');
        hEnd.classList.remove('visible');
        toolbar.classList.remove('visible');
        stopAutoScroll();
      }

      // Keep grips glued to their cells as content scrolls or the layout shifts.
      term.onScroll(layout);
      term.onRender(() => { if (active) layout(); });
      window.addEventListener('resize', () => { if (active) layout(); });

      // --- auto-scroll while dragging a grip near the top/bottom edge ---
      let autoTimer = null, autoDir = 0, lastDragClient = null, dragWhich = null;
      function startAutoScroll(dir) {
        if (autoDir === dir) return;
        autoDir = dir;
        stopAutoScroll(true);
        if (dir === 0) return;
        autoTimer = setInterval(() => {
          term.scrollLines(autoDir);
          if (lastDragClient && dragWhich) updateDragTo(lastDragClient.x, lastDragClient.y);
        }, 60);
      }
      function stopAutoScroll(keepDir) {
        if (autoTimer) { clearInterval(autoTimer); autoTimer = null; }
        if (!keepDir) autoDir = 0;
      }
      function edgeDir(clientY) {
        const s = screenEl(); if (!s) return 0;
        const r = s.getBoundingClientRect();
        const M = 28;                       // edge band
        if (clientY < r.top + M) return -1;
        if (clientY > r.bottom - M) return 1;
        return 0;
      }

      // Move whichever grip is being dragged to the given client point.
      function updateDragTo(clientX, clientY) {
        if (!sel || !dragWhich) return;
        const cell = pointToCell(clientX, clientY);
        if (dragWhich === 'start') sel.start = cell; else sel.end = cell;
        // Allow the grips to cross: if they swap order, hand the drag to the
        // other grip so it keeps following the finger (native behaviour).
        if (cellCmp(sel.start, sel.end) > 0) {
          const t = sel.start; sel.start = sel.end; sel.end = t;
          dragWhich = dragWhich === 'start' ? 'end' : 'start';
        }
        applySelection();
      }

      // Where the finger landed relative to the cell the grip marks. The drag
      // then keeps that offset, so wherever on the (large) grip the finger
      // came down, the selection edge tracks the finger 1:1 instead of jumping.
      let dragOff = { x: 0, y: 0 };
      function beginHandleDrag(which, e) {
        e.preventDefault();
        dropFocusIfKeyboardDown();      // dragging a grip is selection, not typing
        dragWhich = which;
        const cell = which === 'start' ? sel.start : sel.end;
        const pt = cellToPoint(cell.col, cell.row);
        dragOff = { x: pt.x - e.clientX, y: pt.y + pt.h / 2 - e.clientY };
        toolbar.classList.remove('visible');       // out of the way while dragging
        const el = which === 'start' ? hStart : hEnd;
        try { el.setPointerCapture(e.pointerId); } catch (_) {}
      }
      function moveHandleDrag(e) {
        if (!dragWhich) return;
        e.preventDefault();
        const aimX = e.clientX + dragOff.x, aimY = e.clientY + dragOff.y;
        lastDragClient = { x: aimX, y: aimY };
        startAutoScroll(edgeDir(e.clientY));
        updateDragTo(aimX, aimY);
      }
      function endHandleDrag(e) {
        if (!dragWhich) return;
        dragWhich = null; lastDragClient = null;
        stopAutoScroll();
        suppressFocusClick = true;
        layout();                                   // brings the toolbar back
      }
      for (const [el, which] of [[hStart, 'start'], [hEnd, 'end']]) {
        el.addEventListener('pointerdown', (e) => beginHandleDrag(which, e));
        el.addEventListener('pointermove', moveHandleDrag);
        el.addEventListener('pointerup', endHandleDrag);
        el.addEventListener('pointercancel', endHandleDrag);
      }

      // --- long-press on the terminal to start a selection ---
      // LP_MS is deliberately LONGER than a platform long-press (~500ms):
      // in a terminal, touches overwhelmingly mean scroll/tap — a thumb that
      // lands on a word and rests half a beat must not sprout a selection.
      // Selection is press-and-HOLD; the grips do the precision work after.
      const LP_MS = 650, MOVE_CANCEL = 10;
      // Scroll-rescue: a long-press whose follow-up drag is clearly VERTICAL
      // was a scroll the user started from rest (thumb planted while reading,
      // then swiped) — not a selection-extend. The first decisive movement
      // after the press classifies the gesture, however long the rest was:
      //   - vertical-dominant  → dismiss the just-created selection and hand
      //     the drag to the scroller, so a scroll can never be hijacked;
      //   - horizontal-leaning → the drag commits to extending (and may then
      //     move vertically freely, e.g. right-then-down across lines).
      // Same-finger VERTICAL-first extension is deliberately given up — that
      // is what the drag grips are for, and they are untouched by this.
      const RESCUE_RATIO = 2, EXTEND_COMMIT_PX = 24;
      let lpTimer = null, lpStart = null, extending = false, downAt = 0, extendCommitted = false;
      // One-finger swipe scrolls the scrollback. The DOM renderer's .xterm-screen
      // sits ON TOP of the scrollable .xterm-viewport, so a touch-drag never reaches
      // the viewport — inside a full-frame TUI (Claude Code) that means you can't
      // scroll at all. Drive xterm's own scrollLines() from the swipe delta instead.
      let scrolling = false, scrollLastY = 0, scrollAcc = 0;
      // Inertia: velocity is sampled over the drag's last ~100ms; on lift, a
      // rAF loop keeps feeding the same scroll router with exponentially
      // decaying deltas — butter over the exact plumbing the finger used, so
      // normal-screen scrollback and alt-screen apps both behave. A touch
      // during the glide CATCHES it (stops dead, never selects, and the same
      // finger can immediately continue scrolling) — momentum and selection
      // must never fight.
      const GLIDE_START_V = 0.25;   // px/ms of lift velocity needed to glide
      const GLIDE_MIN_V = 0.02;    // px/ms — glide ends below this
      const GLIDE_DECAY = 0.95;    // per-16.7ms decay factor
      const GLIDE_MAX_MS = 3000;   // hard stop — no runaway flicks
      let velSamples = [];          // {t, y} ring from the live drag
      let glideRAF = null, glideV = 0, glideLastT = 0, glideStartT = 0, glideAt = { x: 200, y: 200 };
      function stopGlide() {
        if (glideRAF) { cancelAnimationFrame(glideRAF); glideRAF = null; }
        glideV = 0;
      }
      function startGlide(v, cx, cy) {
        stopGlide();
        glideV = v; glideAt = { x: cx, y: cy };
        glideStartT = glideLastT = performance.now();
        const step = (now) => {
          glideRAF = null;
          const dt = Math.min(50, now - glideLastT);
          glideLastT = now;
          routeScroll(glideV * dt, glideAt.x, glideAt.y);
          glideV *= Math.pow(GLIDE_DECAY, dt / 16.7);
          if (Math.abs(glideV) > GLIDE_MIN_V && now - glideStartT < GLIDE_MAX_MS) {
            glideRAF = requestAnimationFrame(step);
          } else {
            glideV = 0;
          }
        };
        glideRAF = requestAnimationFrame(step);
      }
      // routeScroll: one door for every scroll delta (live finger AND glide) —
      // whole cells to the native scrollback, paced 3-cell wheel notches to an
      // alt-screen app, remainder carried in scrollAcc either way.
      function routeScroll(dy, cx, cy) {
        scrollAcc += dy;
        const h = cellDims().h || 16;
        if (scrollForwardToApp()) {
          const notch = 3 * h;
          const notches = (scrollAcc / notch) | 0;
          if (notches) {
            scrollAcc -= notches * notch;
            queueNotches(notches, notch, cx, cy);
          }
        } else {
          const lines = (scrollAcc / h) | 0;
          if (lines) {
            scrollAcc -= lines * h;
            term.scrollLines(lines);
          }
        }
      }
      // Scroll routing: the app gets the scroll only when it's a full-screen
      // (alternate-buffer) app with mouse reporting on. Inline TUIs on the
      // normal screen scroll the native scrollback instead (see onTouchMove).
      // (The main-screen pointer guard keeps activeProtocol at NONE on the
      // normal buffer, so this reduces to the alt-screen check in practice.)
      function scrollForwardToApp() {
        try {
          return term.buffer.active.type === 'alternate' &&
                 term._core.coreMouseService.activeProtocol !== 'NONE';
        } catch (_) { return false; }
      }

      // Paced dispatch for app-bound scroll notches. Each report makes the
      // app repaint its whole frame; a fast drag crossing several notch
      // boundaries in one move event fired reports back-to-back, so the app
      // repainted 2-3x within milliseconds — visible flicker. Queue the
      // notches and pump one per 45ms (first one immediately): same total
      // scroll, but every repaint gets time to finish before the next tick.
      // The queue is capped so a wild flick can't wind up seconds of
      // after-scroll; the tail past the cap is dropped deliberately.
      let notchQueue = 0, notchPump = null, notchSize = 48, notchAt = { x: 200, y: 200 };
      let notchLastAt = 0;
      function queueNotches(n, notch, cx, cy) {
        notchSize = notch; notchAt = { x: cx, y: cy };
        notchQueue = Math.max(-8, Math.min(8, notchQueue + n));
        if (notchPump) return;
        // Leading edge only when we're outside the pacing window — successive
        // move events each carrying one notch must not bypass the 45ms gap.
        if (Date.now() - notchLastAt >= 45) pumpNotch();
        if (notchQueue) notchPump = setInterval(pumpNotch, 45);
      }
      function pumpNotch() {
        if (!notchQueue) {
          if (notchPump) { clearInterval(notchPump); notchPump = null; }
          return;
        }
        notchLastAt = Date.now();
        const dir = notchQueue > 0 ? 1 : -1;
        notchQueue -= dir;
        const el2 = (term._core && term._core.screenElement) || document.querySelector('.xterm-screen');
        if (el2) {
          el2.dispatchEvent(new WheelEvent('wheel', {
            deltaY: dir * notchSize, deltaMode: 0, bubbles: true, cancelable: true,
            clientX: notchAt.x, clientY: notchAt.y,
          }));
        }
      }

      function cancelLP() { if (lpTimer) { clearTimeout(lpTimer); lpTimer = null; } }

      let touchId = null;                           // identifier of THE gesture finger
      let extendMoved = false;                      // long-press drag actually moved

      // ---- gesture-target pinning ----
      // Touch event streams dispatch to the element the finger LANDED on for
      // the whole gesture. Over text that's a row node inside xterm's DOM
      // renderer — which REPLACES row nodes on every scrolled frame. One line
      // into a scroll the target is detached, detached nodes don't bubble,
      // and the wrap listeners go silent: the drag dies after exactly one
      // line. Events DO keep firing on the detached node though, so we pin
      // per-gesture listeners to the start target and dedupe (the same Event
      // object arrives twice while the node is still attached).
      let gestureNode = null, lastGestureEvent = null;
      function seenGestureEvent(e) {
        if (e === lastGestureEvent) return true;
        lastGestureEvent = e;
        return false;
      }
      function pinGestureTarget(target) {
        unpinGestureTarget();
        if (!target || target === wrap || !(target instanceof Element)) return;
        gestureNode = target;
        gestureNode.addEventListener('touchmove', onTouchMove, { passive: false });
        gestureNode.addEventListener('touchend', onTouchEnd, { passive: true });
        gestureNode.addEventListener('touchcancel', onTouchCancelPinned, { passive: true });
      }
      function unpinGestureTarget() {
        if (!gestureNode) return;
        gestureNode.removeEventListener('touchmove', onTouchMove);
        gestureNode.removeEventListener('touchend', onTouchEnd);
        gestureNode.removeEventListener('touchcancel', onTouchCancelPinned);
        gestureNode = null;
      }
      function onTouchCancelPinned() { onTouchCancelShared(); }
      function onTouchStart(e) {
        if (seenGestureEvent(e)) return;
        if (e.touches.length !== 1) { cancelLP(); return; }
        const t = e.touches[0];
        pinGestureTarget(e.target);
        touchId = t.identifier;
        downAt = Date.now();
        velSamples = [{ t: performance.now(), y: t.clientY }];
        if (glideRAF || Math.abs(glideV) > 0) {
          // Catching a live glide: stop it where it is. This touch is part of
          // the SCROLL, full stop — no long-press timer (a caught scroll must
          // never sprout a selection), no focus click, and the finger can pull
          // straight into more scrolling.
          stopGlide();
          scrolling = true; scrollLastY = t.clientY; scrollAcc = 0;
          lpStart = { x: t.clientX, y: t.clientY, cell: null, wasActive: active };
          suppressFocusClick = true;
          cancelLP();
          return;
        }
        scrolling = false; scrollLastY = t.clientY; scrollAcc = 0;
        lpStart = { x: t.clientX, y: t.clientY, cell: pointToCell(t.clientX, t.clientY), wasActive: active };
        cancelLP();
        lpTimer = setTimeout(() => {
          lpTimer = null; extending = true; extendMoved = false;
          extendCommitted = false;
          dropFocusIfKeyboardDown();     // before the selection can bounce focus
          enterSelection(lpStart.cell);
          dragWhich = 'end';                        // let the same finger extend the end
          suppressFocusClick = true;
        }, LP_MS);
      }
      function onTouchMove(e) {
        if (seenGestureEvent(e)) return;
        // Track OUR finger by identifier — never touches[0]. With a resting
        // palm or an accidental second touch, touches[0] can silently become a
        // DIFFERENT finger; a delta computed across two fingers is a huge
        // phantom jump that scrolls wildly without matching any movement.
        let t = null;
        for (let k = 0; k < e.touches.length; k++)
          if (e.touches[k].identifier === touchId) { t = e.touches[k]; break; }
        if (!t) return;
        if (extending) {                            // we own the gesture now — extend, don't scroll
          e.preventDefault();
          // Scroll-rescue (see RESCUE_RATIO above): classify the first
          // decisive movement, judged on the CUMULATIVE drag from the press
          // point — individual move events often carry only a few px.
          if (!extendCommitted) {
            const rdx = t.clientX - lpStart.x, rdy = t.clientY - lpStart.y;
            const cellH = (cellDims().h || 16);
            if (Math.abs(rdy) > cellH * 1.5 && Math.abs(rdy) > RESCUE_RATIO * Math.abs(rdx)) {
              clearSelection();
              extending = false; dragWhich = null;
              stopAutoScroll();
              scrolling = true;                     // hand the rest of the drag to the scroller
              scrollLastY = lpStart.y; scrollAcc = 0;
              // fall through into the scrolling branch below with THIS event's
              // position, so the movement that triggered the rescue scrolls too
            } else if (Math.abs(rdx) > EXTEND_COMMIT_PX && Math.abs(rdy) <= RESCUE_RATIO * Math.abs(rdx)) {
              extendCommitted = true;               // horizontal lead — it's an extend for good
            }
          }
          if (extending) {
            const aimY = t.clientY - 6;
            lastDragClient = { x: t.clientX, y: aimY };
            // Edge autoscroll only after the long-press drag has actually
            // MOVED. A finger that long-presses near the screen edge and
            // stays put must not start a constant-rate scroll that ignores
            // the finger entirely.
            if (!extendMoved &&
                (Math.abs(t.clientX - lpStart.x) > MOVE_CANCEL || Math.abs(t.clientY - lpStart.y) > MOVE_CANCEL)) {
              extendMoved = true;
            }
            if (extendMoved) startAutoScroll(edgeDir(t.clientY));
            updateDragTo(t.clientX, aimY);
            return;
          }
        }
        if (lpTimer) {                              // still deciding: enough movement => it's a scroll
          if (Math.abs(t.clientX - lpStart.x) > MOVE_CANCEL || Math.abs(t.clientY - lpStart.y) > MOVE_CANCEL) {
            cancelLP();
            scrolling = true;                       // commit to scrolling the scrollback
            // Anchor at the gesture ORIGIN, not the current position: a fast
            // flick's first move event can carry most of the swipe in one big
            // jump — anchoring at "now" silently discarded it, so fast swipes
            // scrolled less than the finger moved while slow ones tracked.
            scrollLastY = lpStart.y; scrollAcc = 0;
          } else {
            return;                                 // not enough movement to decide yet
          }
        }
        if (scrolling && e.touches.length !== 1) {
          // Extra touch (palm) present: keep the anchor synced to OUR finger so
          // its travel during the palm is dropped — never emitted as one burst
          // when the palm lifts.
          scrollLastY = t.clientY;
          return;
        }
        if (scrolling) {                            // one-finger swipe: scroll
          const dy = scrollLastY - t.clientY;       // finger up => positive => scroll toward newer
          scrollLastY = t.clientY;
          velSamples.push({ t: performance.now(), y: t.clientY });
          if (velSamples.length > 8) velSamples.shift();
          // routeScroll picks the destination (see its comment): native
          // scrollback per whole cell, or paced 3-cell wheel notches to an
          // alt-screen app with mouse reporting — pixel-glued either way.
          routeScroll(dy, t.clientX, t.clientY);
          e.preventDefault();                       // we own the vertical drag now
        }
      }
      // Tap a URL to open it. xterm activates a link only after it sees
      // mousemove, mousedown AND mouseup land on that link, and all three are
      // swallowed for touch further down so a scroll can never be read as a
      // selection — so on a phone a link could never activate, and the URL had
      // to be copied out by hand. Synthesising the trio would work, but it
      // would also deliver a real click to any TUI with mouse reporting on, so
      // resolve the link from the buffer instead: the tap stays a tap.
      // The link under a tap comes from the same joiner the mouse path uses
      // (reminalLinks): a URL the terminal wrapped, or one the program broke
      // across rows, opens whole either way. Resolved from the buffer, so
      // the tap stays a tap and no TUI with mouse reporting sees a click.
      function linkAtPoint(clientX, clientY) {
        try {
          const { col, row } = pointToCell(clientX, clientY);
          const buf = term.buffer.active;
          const from = Math.max(0, row - 40), to = Math.min(buf.length - 1, row + 40);
          const rows = [];
          for (let i = from; i <= to; i++) {
            const l = buf.getLine(i);
            rows.push({ text: l ? l.translateToString(true) : '', wrapped: !!(l && l.isWrapped) });
          }
          for (const k of reminalLinks.linksInRows(rows, row - from)) {
            const s = k.start.row + from, e = k.end.row + from;
            if (row < s || row > e) continue;
            if (row === s && col < k.start.col) continue;
            if (row === e && col > k.end.col) continue;
            return k.url;
          }
        } catch (_) {}
        return null;
      }

      function onTouchEnd(e) {
        if (seenGestureEvent(e)) return;
        // Only OUR gesture finger ends the drag. A palm or second touch lifting
        // mid-scroll must not kill the scroll state (that left the rest of the
        // drag dead until the user lifted and re-swiped) — and must not strip
        // the gesture-target pin either, or the drag dies the moment the
        // renderer recycles the pinned node.
        let ourTouch = null;
        for (let k = 0; k < e.changedTouches.length; k++)
          if (e.changedTouches[k].identifier === touchId) { ourTouch = e.changedTouches[k]; break; }
        if (!ourTouch && e.touches.length > 0) return;
        unpinGestureTarget();
        cancelLP();
        if (scrolling) {
          scrolling = false;
          // Flush the tail: the travel between the last move event and the
          // lift (large on fast flicks) would otherwise be dropped.
          const cx = ourTouch ? ourTouch.clientX : 200, cy = ourTouch ? ourTouch.clientY : 200;
          if (ourTouch) {
            routeScroll(scrollLastY - ourTouch.clientY, cx, cy);
            velSamples.push({ t: performance.now(), y: ourTouch.clientY });
          }
          // Lift velocity over the drag's last ~100ms → inertia. Sampling a
          // window (not just the final two events) rides out event jitter.
          const nowT = performance.now();
          const recent = velSamples.filter(s => nowT - s.t <= 100);
          if (recent.length >= 2) {
            const first = recent[0], last = recent[recent.length - 1];
            const dt = last.t - first.t;
            const v = dt > 0 ? (first.y - last.y) / dt : 0; // finger up ⇒ positive ⇒ toward newer
            if (Math.abs(v) >= GLIDE_START_V) startGlide(v, cx, cy);
          }
          velSamples = [];
          return;
        }  // a swipe-scroll: not a tap/selection
        if (extending) {
          extending = false; dragWhich = null;
          stopAutoScroll();
          suppressFocusClick = true;
          layout();
          return;
        }
        // A quick tap (no long-press) while a selection is showing dismisses it.
        const tp = e.changedTouches[0];
        const quickStill = !!(tp && lpStart && Date.now() - downAt < LP_MS &&
          Math.abs(tp.clientX - lpStart.x) < MOVE_CANCEL &&
          Math.abs(tp.clientY - lpStart.y) < MOVE_CANCEL);
        if (active && lpStart) {
          if (quickStill && lpStart.wasActive) { clearSelection(); suppressFocusClick = true; return; }
        }
        // Nothing to dismiss and the finger didn't travel: if that landed on a
        // URL, open it. Runs inside the touchend gesture so the popup is not
        // blocked. Deliberately after the dismiss branch — the first tap out of
        // a selection clears it and does nothing else.
        if (quickStill) {
          const url = linkAtPoint(tp.clientX, tp.clientY);
          if (url) {
            suppressFocusClick = true;
            window.open(url, '_blank', 'noopener,noreferrer');
          }
        }
      }

      // Bind to the wrapper so we catch touches over the whole terminal area.
      // Passive start/end; move is non-passive so we can preventDefault a scroll
      // once we've decided the gesture is a selection-extend.
      const wrap = document.getElementById('terminal-wrap');

      // Fence xterm's built-in MOUSE selection off from touch. Phones follow
      // every unprevented touch with synthesized compatibility mouse events —
      // and xterm's selection engine happily acts on them: a double-tap's
      // dblclick selects the word under the finger (no hold, no intent), and
      // a touch-drag's mousedown+mousemove paints a range. That was the
      // "touching text selects it" bug — none of the long-press machinery
      // below was involved. Swallow mouse events that originate from touch
      // (recent-touch timestamp, plus Chrome's explicit sourceCapabilities
      // signal) BEFORE they reach xterm; `click` flows on untouched — the
      // tap-to-focus handler above depends on it. Real mice are unaffected:
      // they never have a recent touchstart.
      let lastRealTouch = 0;
      const stampTouch = () => { lastRealTouch = Date.now(); };
      wrap.addEventListener('touchstart', stampTouch, { capture: true, passive: true });
      wrap.addEventListener('touchend', stampTouch, { capture: true, passive: true });
      for (const type of ['mousedown', 'mousemove', 'mouseup', 'dblclick']) {
        wrap.addEventListener(type, (e) => {
          const fromTouch = Date.now() - lastRealTouch < 1000 ||
            (e.sourceCapabilities && e.sourceCapabilities.firesTouchEvents);
          if (fromTouch) e.stopPropagation();
        }, { capture: true });
      }

      wrap.addEventListener('touchstart', onTouchStart, { passive: true });
      wrap.addEventListener('touchmove', onTouchMove, { passive: false });
      wrap.addEventListener('touchend', onTouchEnd, { passive: true });
      function onTouchCancelShared() {
        unpinGestureTarget();
        cancelLP();
        scrolling = false;
        stopGlide();
        if (extending) { extending = false; dragWhich = null; stopAutoScroll(); }
      }
      wrap.addEventListener('touchcancel', onTouchCancelShared, { passive: true });

      // Native-style dismiss: a tap or click ANYWHERE outside the selection's
      // own controls (the copy/select-all bar and the two grips) clears it —
      // including taps that land off the terminal (header, page margins) which
      // the wrap-bound touchend above never sees. pointerdown unifies mouse,
      // touch and pen; capture-phase so we run before the grip/toolbar handlers
      // (which we exclude), and before xterm re-selects on a fresh press.
      document.addEventListener('pointerdown', (e) => {
        if (!active) return;
        if (e.target.closest('.wpane')) return; // don't let pane gestures mutate terminal selection
        if (e.target.closest('#sel-toolbar, #sel-handle-start, #sel-handle-end')) return;
        clearSelection();
        // A tap on the terminal itself will fire a follow-up click; swallow it
        // so dismissing the selection doesn't also pop the soft keyboard open.
        if (e.target.closest('#terminal-wrap')) suppressFocusClick = true;
      }, true);

      // --- toolbar actions ---
      copyBtn.addEventListener('click', async (e) => {
        e.preventDefault();
        const txt = term.getSelection();
        if (!txt) { clearSelection(); return; }
        try {
          await navigator.clipboard.writeText(txt);
          copyBtn.textContent = 'Copied!';
          setTimeout(() => { copyBtn.textContent = 'Copy'; }, 1000);
        } catch (_) {
          copyBtn.textContent = 'Copy failed';
          setTimeout(() => { copyBtn.textContent = 'Copy'; }, 1200);
        }
        setTimeout(clearSelection, 300);
      });
      allBtn.addEventListener('click', (e) => {
        e.preventDefault();
        term.selectAll();
        const p = term.getSelectionPosition();
        if (p) {
          // xterm 5.x returns {start:{x,y},end:{x,y}}; older shapes use
          // {startColumn,startRow,...}. Handle both defensively.
          const sx = p.start ? p.start.x : p.startColumn;
          const sy = p.start ? p.start.y : p.startRow;
          const ex = p.end ? p.end.x : p.endColumn;
          const ey = p.end ? p.end.y : p.endRow;
          sel = { start: { col: sx, row: sy }, end: { col: ex, row: ey } };
          active = true;
          layout();
        }
      });
    })();
    connectBtn.addEventListener('click', () => {
      // Three button modes:
      //   - "Connect" (idle/disconnected): start a new connection
      //   - "Cancel" (connecting/reconnecting): abort and stay disconnected
      //   - "Disconnect" (connected): manually close
      if (!ws && !reconnectTimer) {
        startConnect();
      } else {
        userDisconnect();
      }
    });
    sessionInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') startConnect(); });
    pinInput.addEventListener('input', () => { pinTyped = true; });
    pinInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') startConnect(); });

    // Populate the Recent dropdown on load so the user sees their saved
    // sessions even before clicking it.
    renderRecents();
    renderMachines();

    // Re-probe every owned machine's liveness when the Machines panel opens, then
    // keep re-probing on a timer WHILE it stays open — so a machine that comes
    // back, or whose host finishes reconnecting after a sleep/wake, flips from
    // gray to green on its own instead of stranding a stale dot until the user
    // reopens the panel. Poll stops when the panel closes.
    (function () {
      const rec = document.getElementById('recents');
      if (!rec) return;
      let machinesPollTimer = null;
      rec.addEventListener('toggle', () => {
        if (machinesPollTimer) { clearInterval(machinesPollTimer); machinesPollTimer = null; }
        if (!rec.open) closeStatsPortal(); // panel closed — drop any open stats bubble

        if (rec.open) {
          renderMachines();
          machinesPollTimer = setInterval(() => {
            if (!rec.open) { clearInterval(machinesPollTimer); machinesPollTimer = null; return; }
            pollMachines();
          }, 5000);
        }
      });
    })();

    // Keep Recent live across tabs/windows: when another same-origin document
    // (e.g. a session link opened in a browser window) writes the history,
    // the browser fires a 'storage' event here — re-render immediately instead
    // of waiting for the user to reopen the PWA. (e.key is null on clear().)
    window.addEventListener('storage', (e) => {
      if (e.key === HISTORY_KEY || e.key === null) renderRecents();
    });

    // Escape closes the menu the keyboard is in (or the one open while
    // nothing has the keyboard), back on the button that opened it — after
    // anything over it took the key first, and after its search box cleared
    // what was typed in it.
    document.addEventListener('keydown', (e) => {
      if (e.key !== 'Escape' || e.defaultPrevented) return;
      // A sheet opened from a menu (Alerts, What's new) closes first: it
      // takes Escape on the window, after this.
      if (document.querySelector('.cl-sheet:not([hidden])')) return;
      const t = e.target;
      if (t && t.tagName === 'INPUT' && t.value) return;
      const d = (t && t.closest && t.closest('header details[open]')) || (t === document.body && document.querySelector('header details[open]'));
      if (!d) return;
      e.preventDefault();
      d.open = false;
      const s = d.querySelector('summary');
      if (s) s.focus();
    });

    // Close the recents <details> on any outside click. Native <details>
    // doesn't have this behavior — only the summary toggles it — so a
    // dropdown left open after picking a row would shadow the terminal. This
    // runs in capture phase so pane click isolation cannot hide an outside tap.
    document.addEventListener('click', (e) => {
      const recents = document.getElementById('recents');
      if (recents && recents.open && !recents.contains(e.target)) {
        recents.open = false;
      }
      const windows = document.getElementById('windows');
      if (windows && windows.open && !windows.contains(e.target)) {
        windows.open = false;
      }
      const apps = document.getElementById('apps');
      if (apps && apps.open && !apps.contains(e.target)) {
        apps.open = false;
      }
      const host = document.getElementById('host');
      if (host && host.open && !host.contains(e.target)) {
        host.open = false;
      }
    }, true);
    // While the Host dropdown is open, poll the stats live and refresh the
    // panel; stop polling when it closes.
    let hostPollTimer = null;
    document.getElementById('host').addEventListener('toggle', (e) => {
      if (hostPollTimer) { clearInterval(hostPollTimer); hostPollTimer = null; }
      // A finished upgrade shows its green "done" while the panel stays open —
      // that is the moment it is read. Closing the panel is the end of that
      // moment: the next open looks like a fresh page load, checks again, and
      // settles on "up to date" with nothing left over from the upgrade.
      if (!e.target.open && upgradeState && upgradeState.stage === 'done' && !upgradeState.error) upgradeState = null;
      if (e.target.open) {
        // Start the wait clock the first time we open with no data yet.
        if (!lastHostInfo && !hostWaitSince) hostWaitSince = Date.now();
        renderHost();
        if (typeof sendWin === 'function') sendWin('host_info');
        // Ask for a fresh "is there a newer version" while the panel is open.
        // If host_info has not arrived yet the check goes out with the first
        // one that does (see onHostInfo).
        requestUpdateCheck();
        // Poll: re-request the stats AND re-render, so the Loading → "update
        // the host" transition happens off elapsed time even if a timer stalls.
        hostPollTimer = setInterval(() => {
          if (!document.getElementById('host').open) return;
          if (typeof sendWin === 'function') sendWin('host_info');
          if (!lastHostInfo) renderHost();
        }, 1500);
        // Prompt one-shot so the message flips right at the threshold (the poll
        // is the backstop if this ever gets throttled away).
        setTimeout(() => { if (document.getElementById('host').open && !lastHostInfo) renderHost(); }, HOST_WAIT_MS + 50);
      }
    });
    // "+ New session" button lives inside the (re-rendered) host panel, so bind
    // it by delegation. Asks the host to spawn a headless reminal, then connects.
    document.getElementById('host-list').addEventListener('click', (e) => {
      if (e.target.closest('.host-upgrade')) {
        const b = e.target.closest('.host-upgrade');
        if (b.disabled) return;
        // Retrying after a failure starts clean: the old error must not sit
        // under the new attempt's progress.
        // Optimistic first frame: the host's own first message can take a
        // moment on a slow link, and a button that does nothing when pressed
        // reads as broken.
        upgradeState = { stage: 'download', pct: 3, detail: 'Starting\u2026' };
        renderHost();
        sendOwnerAction('upgrade', 'upgrade');
        return;
      }
      if (e.target.closest('.host-tour')) { document.getElementById('host').open = false; if (window.reminalTour) window.reminalTour.start(); return; }
      if (e.target.closest('.host-whatsnew')) { openChangelog(); return; }
      // Every branch below re-renders the panel (renderHost), which detaches the
      // clicked element. The document-level outside-click closer then sees
      // host.contains(e.target)===false and closes the dropdown. Stop the event
      // here so an in-panel control click never reaches that closer.
      if (e.target.closest('.own-copy, .own-reveal, .own-reenroll, .own-done, .host-own, .host-new, .host-desktop')) {
        e.stopPropagation();
      }
      if (e.target.closest('.own-copy')) {
        navigator.clipboard.writeText('sudo reminal add owner ' + ownIdShown)
          .then(() => { ownCopied = true; renderHost(); setTimeout(() => { ownCopied = false; renderHost(); }, 1200); })
          .catch(() => {});
        return;
      }
      if (e.target.closest('.own-reveal')) { ownRevealed = !ownRevealed; renderHost(); return; }
      // Re-enroll: surface the command even though we think we already own this
      // machine (ownership may have been revoked on the host). Loads the device
      // id if needed, then shows the command.
      if (e.target.closest('.own-reenroll')) {
        ownReenroll = true; ownErr = '';
        ownDeviceId()
          .then((id) => { ownIdShown = id; ownRevealed = true; renderHost(); refreshOwnFingerprint(); })
          .catch(() => { ownErr = "This browser can't create a device key. Use the CLI: reminal own"; renderHost(); });
        return;
      }
      // Done: the user just ran the enroll command on the host. Record this
      // machine as owned and refresh the panels in place — no page reload needed.
      if (e.target.closest('.own-done')) {
        getPinnedMachine(sessionId).then(async (b64) => {
          if (b64) { try { await recordOwnedMachineWeb(b64); } catch (_) {} }
          iOwnThisMachine = true; ownReenroll = false; ownRevealed = false; ownCopied = false;
          renderHost();
        }).catch(() => { iOwnThisMachine = true; ownReenroll = false; renderHost(); });
        return;
      }
      if (e.target.closest('.host-own')) {
        ownErr = '';
        ownDeviceId()
          .then((id) => { ownIdShown = id; renderHost(); refreshOwnFingerprint(); })
          .catch(() => {
            ownErr = "This browser can't create a device key (needs Ed25519 support). Use the CLI: reminal own";
            renderHost();
          });
        return;
      }
      // .host-own reuses .host-desktop styling; check it BEFORE .host-desktop.
      if (e.target.closest('.host-desktop')) { openDesktopPanes(); return; }
      if (!e.target.closest('.host-new')) return;
      if (creatingSession) return;
      creatingSession = true; hostNewError = '';
      renderHost();
      if (typeof sendWin === 'function') sendWin('new_session', {});
      hostNewTimer = setTimeout(() => {
        if (!creatingSession) return;
        creatingSession = false; hostNewError = 'Timed out';
        renderHost();
        setTimeout(() => { hostNewError = ''; renderHost(); }, 3000);
      }, HOST_SPAWN_TIMEOUT_MS);
    });

    // ---- Window mirroring: list host windows, stream one, control it -------
    //
    // The dropdown lists the host's on-screen windows (fetched fresh each time
    // it's opened). Picking one opens a pane that shows the window — H.264
    // where both ends support it, JPEG otherwise — and forwards clicks and
    // keystrokes back. Everything rides the same end-to-end-encrypted channel
    // as the terminal, peer-to-peer when a DataChannel forms.
    let winList = [];            // last window list from the agent
    let winHint = '';            // capability warning (e.g. Screen Recording off)
    let winCaptureBlocked = false; // true only when the hint means capture would fail (macOS SR off), vs an informational Wayland caveat
    const winDetails = document.getElementById('windows');
    const winListRoot = document.getElementById('windows-list');
    const appsDetails = document.getElementById('apps');
    const appsListRoot = document.getElementById('apps-list');
    let appList = [];            // installed apps, from app_list
    let pendingLaunch = null;    // { before:Set<id>, appName, until } — auto-open the window a launch creates
    let winListWaiters = [];     // resolvers awaiting the next window_list reply (for a fresh baseline)
    const wkey = document.getElementById('wkey');
    const panes = new Map();     // window id -> { el, img, w, lastUrl }
    let focusedPaneId = null;
    let spawnOffset = 0;         // cascade newly-opened panes
    let pendingRestore = false;  // reopen saved panes on the next window list
    let suppressPaneSave = false; // don't persist layout during bulk open/close
    // Inline SVG icons (inherit currentColor) for a cleaner look than emoji glyphs.
    const ICON_KEYBOARD = '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="2.5" y="6.5" width="19" height="11" rx="2"/><path d="M6 10h0M9.5 10h0M13 10h0M16.5 10h0M6 13.5h0M17.5 13.5h0M9.5 13.5h5"/></svg>';
    const ICON_CLOSE = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>';
    // ⧉ as an SVG so the button's metrics match its SVG siblings — a raw text
    // glyph inherits the page font and renders a taller, mismatched button.
    const ICON_POPOUT = '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><rect x="3.5" y="8.5" width="12" height="12" rx="1.5"/><path d="M8.5 4.5h10a1 1 0 0 1 1 1v10"/></svg>';
    const ICON_INFO = '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9.2"/><path d="M12 11v5"/><path d="M12 7.6h0"/></svg>';
    // Which transport delivered the most recent window frame — shown in the pane
    // (i) popover. Set by onWindowFrame; 'webrtc' means the P2P DataChannel,
    // 'ws' means the relay. Null until the first frame lands.
    let lastFrameVia = null;
    let lastDCSeenAt = 0; // last time ANY message arrived over the DataChannel
    // The label reports which transport is CARRYING the stream — not the
    // transport of the last message processed. With a mixed viewer set the
    // agent legitimately dual-sends (relay copies for WS-only viewers ride the
    // broadcast to everyone, heartbeats have no seq so they bypass the dedup),
    // so "last message wins" flapped webrtc↔relay every heartbeat and every
    // WS-beats-DC arrival race, despite a perfectly healthy P2P stream. A relay
    // message only claims the label once the DataChannel has been silent for a
    // grace window (longer than the 3s heartbeat cadence, so an alive channel
    // always re-stamps itself first).
    const TRANSPORT_CLAIM_MS = 5000;
    function noteTransport(viaDC) {
      const before = lastFrameVia;
      if (viaDC) {
        lastDCSeenAt = performance.now(); lastFrameVia = 'webrtc';
        if (before !== lastFrameVia) for (const p of panes.values()) requestPaneQuality(p);
        return;
      }
      // !lastDCSeenAt = no DataChannel traffic EVER — claim immediately. (The
      // 0 init otherwise reads as "DC activity at page load", which blocked
      // relay frames from labeling for the page's first 5s: the popover sat on
      // "—" while frames were already painting.)
      if (!lastDCSeenAt || performance.now() - lastDCSeenAt > TRANSPORT_CLAIM_MS) lastFrameVia = 'ws';
      if (before !== lastFrameVia) for (const p of panes.values()) requestPaneQuality(p);
    }
    // Describe the current transport for a pane's (i) popover.
    function paneInfoHtml(p) {
      const webrtc = lastFrameVia === 'webrtc';
      let transport, cls;
      if (webrtc && rtcRelayed) { transport = 'WebRTC · TURN relay'; cls = 'turn'; }
      else if (webrtc) { transport = 'WebRTC · direct'; cls = 'p2p'; }
      else if (lastFrameVia === 'ws') { transport = 'Relay · WebSocket'; cls = 'ws'; }
      else { transport = '—'; cls = ''; }
      const enc = webrtc ? 'DTLS (end-to-end)' : 'AES-256-GCM (end-to-end)';
      // Canvas intrinsic pixels are the stream's real capture resolution. The
      // old value was the host window's logical-point size, so the info panel
      // could claim a resolution unrelated to the pixels being decoded.
      const res = (p.gotFrame && p.img.width && p.img.height) ? (p.img.width + '×' + p.img.height) : '—';
      const profile = p.qualityKey ? p.qualityKey.replace(':', '@') : '—';
      // Prune on READ. The rolling window is otherwise only trimmed when a
      // frame ARRIVES, so a stream that stopped kept reporting whatever rate
      // it last had — a frozen pane showing a healthy fps, which is the single
      // most misleading thing this popover could say.
      const tnow = performance.now();
      if (p.frameTimes) while (p.frameTimes.length && tnow - p.frameTimes[0] > 1000) p.frameTimes.shift();
      const fps = p.frameTimes ? p.frameTimes.length : 0;
      // The host floors delivery at 1fps — an idle window re-captures and
      // re-sends once a second — so a healthy stream never reads 0. The old
      // "idle · sent on change" wording papered over change-driven
      // delivery, which proved unreliable; now 0 fps honestly means no frames
      // are arriving (stalled stream, or a host from before the floor existed).
      const rate = fps + ' fps';
      // Capture source, stamped by the agent on each frame: "native" = macOS's
      // ScreenCaptureKit helper, "win32" = Windows' in-process GDI capture
      // (that platform's native path, not a fallback), "shot" = the per-frame
      // screenshot fallback. Surfaced here so "why is it slow" is diagnosable
      // at a glance. Only the true fallback carries a reason — and only where a
      // native path exists to have fallen back from.
      const cap = p.capSrc === 'h264' ? 'Native · H.264 video'
        : p.capSrc === 'native' ? 'Native · ScreenCaptureKit'
        : p.capSrc === 'win32' ? 'Native · Windows GDI'
        : p.capSrc === 'shot' ? ('Screenshot fallback' + (p.capErr ? ' · ' + escapeHtml(p.capErr) : '')) : '\u2014';
      // Video health, shown only once something has actually gone wrong: gaps
      // mean access units were lost in flight, errors mean the decoder itself
      // rejected them. Which of the two it is decides where to look, and a
      // phone has no console to read it from.
      const gaps = p.h264Gaps || 0, derrs = p.h264Errs || 0;
      const health = (gaps || derrs)
        ? '<div class="row"><span class="k">Video</span><span class="v warn">' + gaps + ' gaps · ' + derrs + ' decode errors</span></div>'
        : '';
      return '<div class="row"><span class="k">Transport</span><span class="v ' + cls + '">' + transport + '</span></div>'
        + '<div class="row"><span class="k">Capture</span><span class="v">' + cap + '</span></div>'
        + health
        + '<div class="row"><span class="k">Encryption</span><span class="v">' + enc + '</span></div>'
        + '<div class="row"><span class="k">Resolution</span><span class="v">' + res + '</span></div>'
        + '<div class="row"><span class="k">Requested</span><span class="v">' + profile + '</span></div>'
        + '<div class="row"><span class="k">Frame rate</span><span class="v">' + rate + '</span></div>';
    }

    // sendWin sends a window_* control frame. If obj is provided it's
    // JSON-encoded and encrypted into data; otherwise a bare {type} goes out
    // (used for the window_list request, which carries no payload).
    // ---- typing: one event per burst, not one per letter ---------------------
    //
    // Every character used to be its own window_input, and on macOS each one
    // spawns an osascript to inject it — measured at 40-50ms on an idle
    // machine, more under load. Typing therefore had a ceiling around 16
    // characters a second, and anything faster queued: letters arrived
    // seconds late while the picture kept updating at full rate, because
    // frames come from a different goroutine entirely.
    //
    // Characters that arrive within a frame of each other are sent as one
    // event. The host types a whole string in one call, so a burst of ten
    // costs one injection instead of ten. A lone keypress waits 16ms, which
    // is below what anyone can see.
    const WIN_TYPE_COALESCE_MS = 16;
    let winTypeBuf = '', winTypeId = '', winTypeTimer = 0, winTypeFlushing = false;

    // winTypeFlush sends what has been typed so far. Called before any other
    // window input, so a Return can never overtake the line it ends.
    function winTypeFlush() {
      if (!winTypeBuf) return;
      const text = winTypeBuf, id = winTypeId;
      winTypeBuf = ''; winTypeId = '';
      winTypeFlushing = true;
      try {
        for (let i = 0; i < text.length; i += WIN_TYPE_CHUNK) {
          sendWin('window_input', { id, kind: 'key', text: text.slice(i, i + WIN_TYPE_CHUNK) });
        }
      } finally { winTypeFlushing = false; }
    }

    // winType sends the first character straight away and coalesces only what
    // follows it within the window. Someone typing at human speed therefore
    // waits for nothing — every keypress leaves immediately, as before — while
    // a burst (autorepeat, a paste, a phone's autocomplete inserting a whole
    // word) collapses into one event instead of one per character. Delaying
    // every keypress to catch the bursts would have traded latency everyone
    // feels for a saving that only shows up sometimes.
    function winType(id, text) {
      if (!id || !text) return;
      if (winTypeId && winTypeId !== id) winTypeFlush(); // another pane: order first
      winTypeId = id;
      winTypeBuf += text;
      // A paste is already a burst; send it rather than waiting on a timer.
      if (winTypeBuf.length >= WIN_TYPE_CHUNK) { winTypeFlush(); return; }
      if (!winTypeTimer) {
        // Nothing in flight: this is the start of typing, so it goes now and
        // the window opens behind it for whatever lands next.
        winTypeFlush();
        winTypeTimer = setTimeout(() => { winTypeTimer = 0; winTypeFlush(); }, WIN_TYPE_COALESCE_MS);
      }
    }

    async function sendWin(type, obj) {
      // Anything that is not the buffered text itself goes behind it: a
      // special key, a click, a scroll, a drag. Without this, Return could
      // reach the host before the line it was meant to end.
      if (type === 'window_input' && !winTypeFlushing) winTypeFlush();
      // Interacting with a pane means latency now matters more than smoothness:
      // stop playing buffered frames out and jump to the newest.
      if (type === 'window_input' && obj && obj.id) {
        const ip = panes.get(obj.id);
        if (ip) {
          // Stay live for a beat after input. Paced playout of a relay batch
          // is how "30 fps" became a two-second-old movie: arrivals were
          // counted, then scheduled into the future. A click must see the
          // frame that contains its result, not the one still in the queue.
          ip.liveUntil = performance.now() + 1500;
          dropQueuedFrames(ip);
        }
        // Peer-to-peer input: when the reliable input channel is up, send the
        // event straight to the agent over DTLS instead of through the billed
        // relay. That removes a full Durable-Object round-trip from every click
        // and keystroke — the change that makes a remote window feel local.
        // DTLS already protects it, so no app-layer encryption (matching frames
        // and acks on the other channels). Any failure falls through to WS.
        if (rtcInput && rtcInput.readyState === 'open') {
          try { rtcInput.send(JSON.stringify(obj)); return; } catch (_) {}
        }
      }
      const sock = ws; // as send(): this session's socket, or nothing
      if (!sock || sock.readyState !== WebSocket.OPEN || !cryptoKey) return;
      const frame = { type };
      if (obj !== undefined) frame.data = await encrypt(cryptoKey, JSON.stringify(obj));
      if (ws !== sock || sock.readyState !== WebSocket.OPEN) return;
      sock.send(JSON.stringify(frame));
    }

    // forgetHostState: the session shown is about to change. Its windows'
    // streams went with its connection either way; on another machine (or one
    // not known to be the same) the lists, hints and anything waiting on them
    // are that machine's, so they go too, and the panels say they are loading.
    function forgetHostState(sameMachine) {
      if (typeof closeAllPanes === 'function') { try { closeAllPanes(); } catch (_) {} }
      if (sameMachine) return;
      winList = []; winHint = ''; winCaptureBlocked = false;
      appList = []; pendingLaunch = null;
      // They asked the machine being left: told null (no answer), never the
      // new machine's list, which would launch an app or open panes there.
      const left = winListWaiters; winListWaiters = [];
      left.forEach(fn => fn(null));
      hostDesktopHint = ''; hostDesktopErr = '';
      upgradeState = null; changelog = null;
      winListRoot.innerHTML = '<div class="windows-note">Loading windows…</div>';
      appsListRoot.innerHTML = '<div class="windows-note">Loading apps…</div>';
      if (typeof renderHost === 'function') renderHost();
    }
    // An Apps list left open across a switch is asked for again, as the
    // Windows one is on every connect.
    function refreshOpenApps() { if (appsDetails.open) sendWin('app_list'); }

    function requestWindows() {
      // Show the last known list instantly (no flicker) and refresh in the
      // background; only flash "Loading…" the first time, when we have nothing.
      if (winList.length) renderWindows();
      else winListRoot.innerHTML = '<div class="windows-note">Loading windows…</div>';
      sendWin('window_list');
    }

    // resolveWinListWaiters answers everyone awaiting the next window list
    // (nextWindowList) with the list as it now is — none, when the host cannot
    // list windows or failed to: a caller is never left waiting for a reply
    // that has come.
    function resolveWinListWaiters() {
      if (!winListWaiters.length) return;
      const waiters = winListWaiters; winListWaiters = [];
      waiters.forEach(fn => fn(winList));
    }

    function onWindowList(payload) {
      if (payload.unsupported) {
        winList = [];
        winListRoot.innerHTML = '<div class="windows-note">' + escapeHtml(payload.unsupported) + '</div>';
        resolveWinListWaiters();
        return;
      }
      if (payload.error) {
        winList = [];
        winListRoot.innerHTML = '<div class="windows-note">Couldn\'t list windows: ' + escapeHtml(payload.error) + '</div>';
        resolveWinListWaiters();
        return;
      }
      winList = payload.windows || [];
      winHint = payload.hint || ''; // e.g. macOS Screen Recording is off
      // Fresh agents send capture_blocked explicitly; an old agent that omits it
      // falls back to the previous behaviour (treat any hint as blocking).
      winCaptureBlocked = ('capture_blocked' in payload) ? !!payload.capture_blocked : !!winHint;
      renderWindows();
      // First list after (re)connecting: reopen the panes this session had up.
      // Only when nothing is open, so a mid-session refresh never clobbers the
      // panes you're already using.
      if (pendingRestore) {
        pendingRestore = false;
        if (paneOnly) {
          // This tab exists to show exactly one window — never the saved
          // layout, which belongs to the full viewer.
          const w = winList.find((x) => x.id === paneOnly);
          if (w && !panes.has(w.id)) { openWinView(w); focusPane(w.id); }
        } else if (panes.size === 0) {
          restoreSavedPanes(winList);
        }
      }
      // Resolve anyone awaiting a fresh window list (app-launch baseline), then
      // see if a just-launched app's window has appeared.
      resolveWinListWaiters();
      checkLaunch();
    }

    function escapeHtml(s) {
      return String(s).replace(/[&<>"']/g, c => (
        { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
      ));
    }

    // Render a permission hint, turning the literal `reminal permissions` command
    // into a monospace chip with a copy button — obvious, and one tap to copy on a
    // phone. Returns an HTML string; caller assigns it to innerHTML.
    function permHintHTML(text) {
      const cmd = 'reminal permissions';
      const chip = '<code class="cmd">' + cmd + '</code>' +
        '<button class="copy-cmd" data-cmd="' + cmd + '" title="Copy command">copy</button>';
      return escapeHtml(text).split(cmd).join(chip);
    }

    // Copy button (in permission hints). Capture-phase so it wins before any pane
    // handler can swallow the click.
    document.addEventListener('click', e => {
      const b = e.target.closest && e.target.closest('.copy-cmd');
      if (!b) return;
      e.preventDefault();
      const cmd = b.getAttribute('data-cmd') || '';
      try { navigator.clipboard.writeText(cmd); } catch (_) { /* insecure ctx / denied */ }
      const prev = b.textContent; b.textContent = 'copied';
      setTimeout(() => { b.textContent = prev; }, 1200);
    }, true);

    // The narrow-screen toolbar is a scroll container; keep it from clipping an
    // open dropdown. `toggle` does not bubble, but non-bubbling events still run
    // the capture phase, so one document-level listener catches all of them
    // without disturbing the per-menu handlers already installed.
    document.addEventListener('toggle', () => {
      const sf = document.getElementById('session-form');
      if (sf) sf.classList.toggle('menu-open', !!sf.querySelector('details[open]'));
    }, true);

    // ---- Window notes: what an agent pinned on a window ---------------------
    // The same annotations the desktop badge shows, mirrored here so a phone
    // sees what the Mac does. Machine-scoped, ephemeral, and pushed by the
    // agent — this side only renders.
    let notesByWin = Object.create(null);

    const NOTE_COLORS = {
      attention: '#ff453a', working: '#ffbd21', info: '#2a8cff',
      done: '#33d158', handback: '#bf61f2',
    };
    const NOTE_LABELS = {
      attention: 'Needs you', working: 'Working', info: 'Note',
      done: 'Done', handback: 'Back to agent',
    };

    function onWindowNotes(next) {
      notesByWin = next || Object.create(null);
      // Both surfaces re-read from the same map, so refresh whatever is up.
      if (typeof renderWindows === 'function' && winList.length) renderWindows();
      for (const [id, pane] of panes) refreshPaneNotes(id, pane);
      // Re-render an open popover in place; otherwise acting on a note leaves
      // the old list sitting there looking like nothing happened.
      const open = document.querySelector('.notes-pop');
      if (open && open.dataset.win) {
        const wid = open.dataset.win, still = notesFor(wid);
        const anchor = open._anchor;
        open.remove();
        if (still.length && anchor && document.body.contains(anchor)) openNotesPopover(anchor, still, wid);
      }
    }

    function notesFor(id) { return notesByWin[String(id)] || []; }

    function noteAge(ts) {
      if (!ts) return '';
      const s = Math.max(0, Math.floor(Date.now() / 1000 - ts));
      if (s < 45) return 'just now';
      if (s < 3600) return Math.max(1, Math.round(s / 60)) + 'm ago';
      if (s < 86400) return Math.round(s / 3600) + 'h ago';
      return Math.round(s / 86400) + 'd ago';
    }

    // A compact dots pill. Same read as the badge on the desktop: one dot per
    // note, red first because that is the one that wants you.
    function buildNotesPill(id, onOpen) {
      const list = notesFor(id);
      if (!list.length) return null;
      const pill = document.createElement('button');
      pill.className = 'notes-pill';
      pill.title = list.length + (list.length === 1 ? ' note' : ' notes') + ' on this window';
      const ordered = list.slice().sort((a, b) =>
        (a.status === 'attention' ? -1 : 0) - (b.status === 'attention' ? -1 : 0));
      for (const n of ordered.slice(0, 4)) {
        const d = document.createElement('span');
        d.className = 'notes-dot' + (n.status === 'attention' ? ' pulse' : '');
        d.style.background = NOTE_COLORS[n.status] || NOTE_COLORS.info;
        pill.appendChild(d);
      }
      if (list.length > 4) {
        const more = document.createElement('span');
        more.className = 'notes-more'; more.textContent = '+' + (list.length - 4);
        pill.appendChild(more);
      }
      pill.addEventListener('click', (e) => { e.stopPropagation(); e.preventDefault(); onOpen(list); });
      return pill;
    }

    // The list itself, as a popover anchored to whatever was clicked. Carries
    // the same actions as the desktop badge: Done hands work back to the agent,
    // Dismiss clears one, Dismiss all clears the window.
    function noteAct(windowId, noteId, action) {
      sendWin('window_note_act', { window: String(windowId), id: noteId || '', action });
    }

    // Mirrors the badge's rule: hand-back only where it means something —
    // "working" is the agent's turn — but Dismiss is ALWAYS offered, or a stuck
    // note from a crashed agent could never be cleared.
    function noteButtons(status) {
      if (status === 'attention') return { handback: 'Done', dismiss: 'Dismiss' };
      if (status === 'info') return { handback: null, dismiss: 'Got it' };
      if (status === 'working') return { handback: null, dismiss: 'Dismiss' };
      return { handback: null, dismiss: 'Clear' };
    }

    function openNotesPopover(anchor, list, windowId) {
      document.querySelectorAll('.notes-pop').forEach((el) => el.remove());
      const pop = document.createElement('div');
      pop.className = 'notes-pop';

      if (list.length > 1) {
        const head = document.createElement('div'); head.className = 'notes-head';
        const cnt = document.createElement('span');
        cnt.textContent = list.length + ' notes';
        const all = document.createElement('button');
        all.className = 'notes-btn'; all.textContent = 'Dismiss all';
        all.addEventListener('click', (e) => {
          e.stopPropagation(); noteAct(windowId, '', 'dismiss_all'); pop.remove();
        });
        head.append(cnt, all);
        pop.appendChild(head);
      }

      for (const n of list) {
        const row = document.createElement('div'); row.className = 'notes-row';
        const dot = document.createElement('span'); dot.className = 'notes-dot';
        dot.style.background = NOTE_COLORS[n.status] || NOTE_COLORS.info;
        const txt = document.createElement('div'); txt.className = 'notes-txt';
        const t = document.createElement('div'); t.className = 'notes-title'; t.textContent = n.title || '';
        txt.appendChild(t);
        if (n.body) { const b = document.createElement('div'); b.className = 'notes-body'; b.textContent = n.body; txt.appendChild(b); }
        const meta = document.createElement('div'); meta.className = 'notes-meta';
        meta.textContent = [NOTE_LABELS[n.status] || n.status, n.author, noteAge(n.ts)].filter(Boolean).join(' · ');
        txt.appendChild(meta);

        const btns = noteButtons(n.status);
        const actions = document.createElement('div'); actions.className = 'notes-actions';
        if (btns.handback) {
          const b = document.createElement('button');
          b.className = 'notes-btn notes-btn-primary'; b.textContent = btns.handback;
          b.addEventListener('click', (e) => { e.stopPropagation(); noteAct(windowId, n.id, 'handback'); });
          actions.appendChild(b);
        }
        const d = document.createElement('button');
        d.className = 'notes-btn'; d.textContent = btns.dismiss;
        d.addEventListener('click', (e) => { e.stopPropagation(); noteAct(windowId, n.id, 'dismiss'); });
        actions.appendChild(d);
        txt.appendChild(actions);

        row.append(dot, txt);
        pop.appendChild(row);
      }
      document.body.appendChild(pop);
      const r = anchor.getBoundingClientRect();
      pop.style.top = Math.min(window.innerHeight - pop.offsetHeight - 8, r.bottom + 6) + 'px';
      pop.style.left = Math.max(8, Math.min(window.innerWidth - pop.offsetWidth - 8, r.left)) + 'px';
      pop.dataset.win = String(windowId); pop._anchor = anchor;
      const away = (ev) => {
        if (pop.contains(ev.target) || anchor.contains(ev.target)) return;
        pop.remove(); document.removeEventListener('pointerdown', away, true);
      };
      setTimeout(() => document.addEventListener('pointerdown', away, true), 0);
    }

    // Keep a pane's pill in step when notes change under it.
    function refreshPaneNotes(id, pane) {
      if (!pane || !pane.notesSlot) return;
      pane.notesSlot.innerHTML = '';
      const pill = buildNotesPill(id, (list) => openNotesPopover(pane.notesSlot, list, id));
      if (pill) pane.notesSlot.appendChild(pill);
    }

    // buildFilterList paints a panel as a search box over a list of rows, and
    // redraws the rows on every keystroke. Apps, Windows and (in its own way)
    // Machines all want exactly this; the only things that differ are what a row
    // looks like and what text it matches on, so those are the two callbacks.
    //
    // opts: placeholder, items, haystack(item) → text to match, row(item) → the
    // element to show, empty (text when a query matches nothing), focus (take
    // the caret on open — for a panel you open in order to type).
    function buildFilterList(root, opts) {
      const search = document.createElement('input');
      search.className = 'panel-search';
      // type=search, not text: it brings the browser's own clear (✕) affordance,
      // which is the expected way out of a filter on both desktop and phone.
      search.type = 'search';
      search.placeholder = opts.placeholder;
      search.autocomplete = 'off';
      search.spellcheck = false;
      const rows = document.createElement('div');
      rows.className = 'panel-rows' + (opts.rowsClass ? ' ' + opts.rowsClass : '');
      root.appendChild(search);
      root.appendChild(rows);
      const draw = () => {
        rows.innerHTML = '';
        const f = search.value.trim().toLowerCase();
        const items = f
          ? opts.items.filter(it => opts.haystack(it).toLowerCase().includes(f))
          : opts.items;
        if (items.length === 0) {
          const n = document.createElement('div');
          n.className = 'windows-note';
          n.textContent = opts.empty;
          rows.appendChild(n);
          return;
        }
        for (const it of items) rows.appendChild(opts.row(it));
      };
      search.addEventListener('input', draw);
      draw();
      if (opts.focus) setTimeout(() => search.focus(), 0);
      return search;
    }

    function renderWindows() {
      winListRoot.innerHTML = '';
      if (winHint) {
        const h = document.createElement('div'); h.className = 'windows-hint'; h.innerHTML = permHintHTML(winHint);
        winListRoot.appendChild(h);
      }
      if (winList.length === 0) {
        const n = document.createElement('div'); n.className = 'windows-note';
        n.textContent = 'No open windows found.';
        winListRoot.appendChild(n);
        return;
      }
      // Whole-desktop pseudo-entries ("display:<id>") are opened from the Host
      // panel's "View full desktop" button, not this per-window list.
      const listable = winList.filter(w => !(w.id || '').startsWith('display:'));
      buildFilterList(winListRoot, {
        placeholder: 'Search windows…',
        items: listable,
        empty: 'No window matches that.',
        // App name and title — the two things anyone remembers a window by.
        haystack: w => (w.app || '') + ' ' + (w.title || ''),
        row: w => {
          const row = document.createElement('div');
          row.className = 'recents-row';
          row.title = 'Open and control this window';
          row.appendChild(appListIcon(w));
          const label = document.createElement('div');
          label.className = 'label';
          const nameEl = document.createElement('div');
          nameEl.className = 'name';
          nameEl.textContent = w.app || 'Window';
          const metaEl = document.createElement('div');
          metaEl.className = 'meta';
          metaEl.textContent = (w.title || '(untitled)') + ' · ' + w.w + '×' + w.h;
          // Notes are the machine owner's business: shown only to a viewer that
          // has proved ownership, never to someone holding just the session PIN.
          if (iOwnThisMachine) {
            const pill = buildNotesPill(w.id, (list) => openNotesPopover(pill, list, w.id));
            if (pill) { pill.classList.add('notes-pill-row'); nameEl.appendChild(pill); }
          }
          label.appendChild(nameEl);
          label.appendChild(metaEl);
          row.appendChild(label);
          row.addEventListener('click', () => {
            winDetails.open = false;
            openWinView(w);
          });
          return row;
        },
      });
    }

    // ---- Apps launcher: list installed apps, launch one on the host ----------
    function requestApps() {
      if (appList.length) renderApps();
      else appsListRoot.innerHTML = '<div class="windows-note">Loading apps…</div>';
      sendWin('app_list');
    }

    function onAppList(payload) {
      if (payload.unsupported) {
        appList = [];
        appsListRoot.innerHTML = '<div class="windows-note">' + escapeHtml(payload.unsupported) + '</div>';
        return;
      }
      if (payload.error) {
        appList = [];
        appsListRoot.innerHTML = '<div class="windows-note">Couldn\'t list apps: ' + escapeHtml(payload.error) + '</div>';
        return;
      }
      appList = payload.apps || [];
      renderApps();
    }

    function renderApps() {
      appsListRoot.innerHTML = '';
      if (appList.length === 0) {
        const n = document.createElement('div'); n.className = 'windows-note';
        n.textContent = 'No apps found.';
        appsListRoot.appendChild(n);
        return;
      }
      buildFilterList(appsListRoot, {
        placeholder: 'Search apps…',
        items: appList,
        // A grid of tiles, three across, like the tour shows it.
        rowsClass: 'app-grid',
        // The launcher is something you open in order to type, so take focus.
        focus: true,
        empty: 'No app matches that.',
        haystack: a => a.name || '',
        row: a => {
          const row = document.createElement('div');
          row.className = 'app-tile';
          row.title = 'Launch ' + (a.name || 'this app') + ' on the host';
          row.appendChild(appListIcon(a));
          const nameEl = document.createElement('div'); nameEl.className = 'name';
          nameEl.textContent = a.name || 'App';
          row.appendChild(nameEl);
          row.addEventListener('click', () => launchApp(a));
          return row;
        },
      });
    }

    // An app's icon is the image its machine sent with it (a data: image).
    // The page's policy loads images from nowhere else, so an icon named
    // anywhere else is never asked for: the app shows its letter instead.
    function appListIcon(item) {
      if (item && typeof item.icon === 'string' && /^data:image\//i.test(item.icon)) {
        const img = document.createElement('img');
        img.className = 'app-list-icon'; img.src = item.icon; img.alt = '';
        img.draggable = false;
        return img;
      }
      const fallback = document.createElement('span');
      fallback.className = 'app-list-icon fallback';
      const name = (item && (item.name || item.app)) || '?';
      fallback.textContent = Array.from(name.trim())[0] || '?';
      return fallback;
    }

    // nextWindowList resolves with the window list from the next window_list
    // reply — used to snapshot a fresh baseline right before launching an app.
    function nextWindowList() {
      return new Promise(resolve => { winListWaiters.push(resolve); sendWin('window_list'); });
    }

    // launchApp opens an app on the host and auto-mirrors the window it creates.
    // Robustness: we snapshot the set of open window IDs BEFORE launching, then
    // watch for a window whose ID wasn't there before. We never match on app
    // name or title — so a second window of a same-named app (the classic
    // "click went to the wrong Chrome window") can't be mis-picked; only a
    // genuinely new window ID is ever opened.
    async function launchApp(a) {
      appsDetails.open = false;
      const base = await nextWindowList();               // accurate pre-launch baseline
      if (!base) return;                                 // the session changed meanwhile
      pendingLaunch = {
        before: new Set(base.map(w => w.id)),
        appName: (a.name || '').toLowerCase(),
        until: Date.now() + 9000,                        // give slow/cold apps time to draw a window
      };
      sendWin('app_open', { id: a.id });
      pollLaunch();
    }

    function pollLaunch() {
      if (!pendingLaunch) return;
      if (Date.now() > pendingLaunch.until) { pendingLaunch = null; return; }
      sendWin('window_list');                            // reply → onWindowList → checkLaunch
      setTimeout(pollLaunch, 800);
    }

    // checkLaunch runs on every window list while a launch is pending: if a new
    // window ID has appeared, open it. The ID diff alone guarantees the window
    // is genuinely new; app name is only a tiebreaker on the rare occasion that
    // several windows appear at once, and never picks a pre-existing window.
    function checkLaunch() {
      if (!pendingLaunch) return;
      const fresh = winList.filter(w => !pendingLaunch.before.has(w.id));
      if (!fresh.length) return;
      const name = pendingLaunch.appName;
      const pick = fresh.find(w => {
        const app = (w.app || '').toLowerCase();
        return app && name && (app.includes(name) || name.includes(app));
      }) || fresh[0];
      pendingLaunch = null;
      if (!panes.has(pick.id)) openWinView(pick);
    }

    // ---- Windowed panes: host windows as draggable floating windows ----------
    // Each chosen window becomes a resizable, draggable pane; several stream at
    // once. Tap/click in a pane's image clicks there; the focused pane (blue
    // border) receives keystrokes from the shared #wkey input.
    const WIN_SPECIAL_KEYS = {
      Enter: 'return', Tab: 'tab', Escape: 'escape', Backspace: 'delete',
      Delete: 'forwarddelete', ArrowUp: 'up', ArrowDown: 'down',
      ArrowLeft: 'left', ArrowRight: 'right', ' ': 'space',
      Home: 'home', End: 'end', PageUp: 'pageup', PageDown: 'pagedown',
    };

    function focusPane(id) {
      focusedPaneId = id;
      const p = panes.get(id);
      // Restack so the focused pane sits above the others (real-window behaviour).
      // Reassign z-indices compactly from a fixed base every time — the old
      // monotonic counter saturated at the 49 ceiling, after which every pane
      // shared the same z-index and clicking stopped bringing panes forward.
      const others = [...panes.values()].filter((q) => q !== p)
        .sort((a, b) => (+a.el.style.zIndex || 0) - (+b.el.style.zIndex || 0));
      let z = 31; // keybar is z 49, header 50; panes must stay below both
      for (const q of others) q.el.style.zIndex = Math.min(z++, 47);
      if (p) p.el.style.zIndex = Math.min(z, 48); // focused pane on top
      for (const [pid, q] of panes) q.el.classList.toggle('focused', pid === id);
    }

    // Lowest Y a floating pane may occupy: just under the header. The header is
    // taller on phones (its controls wrap), and it sits above panes in the
    // z-order, so a pane pinned to a fixed small top would slide its draggable
    // title bar under the header — leaving close/drag unreachable. Clamp to the
    // header's live height instead.
    function headerBottom() {
      const h = document.querySelector('header');
      return (h ? Math.ceil(h.getBoundingClientRect().bottom) : 48) + 6;
    }

    // Opens (or re-focuses) a floating pane for window w. Named openWinView so
    // the dropdown row handler stays unchanged.
    // ── close-on-host gesture (pane ✕ button) ───────────────────────────────
    // The ✕ has two jobs, told apart by the gesture — the same idea as the
    // recents/snippets docks (tap the handle vs. drag up from it):
    //   • TAP ✕            → hide the pane here (stop the local mirror). Unchanged.
    //   • DRAG UP from ✕   → raise a small menu; release on "Close on the machine"
    //                        to close the REAL window on the host OS.
    // Kept behind a deliberate drag + explicit release so a stray tap can never
    // close a host window (and lose unsaved work). One item today, room for more.
    let winCloseMenu = null;   // { el, items:[{el, action, rect}] } while dragging
    let winCloseDragEnd = 0;   // timeStamp of the last drag release — swallows the trailing click

    function openWinCloseMenu(btn) {
      closeWinCloseMenu();
      const menu = document.createElement('div');
      menu.className = 'wclose-menu';
      const item = document.createElement('div');
      item.className = 'wclose-item';
      // Distinguish the destructive host-close from the pane's plain hide.
      item.innerHTML = 'Close on the machine<span class="sub">closes the real window, not just this view</span>';
      menu.appendChild(item);
      document.body.appendChild(menu);
      // Anchor above the ✕, right edges aligned, clamped to the viewport; drop
      // below only if there's no room above (a pane docked to the top edge).
      const r = btn.getBoundingClientRect();
      const mw = menu.offsetWidth, mh = menu.offsetHeight;
      const left = Math.max(6, Math.min(r.right - mw, window.innerWidth - mw - 6));
      let top = r.top - mh - 8;
      if (top < 6) top = r.bottom + 8;
      menu.style.left = left + 'px';
      menu.style.top = top + 'px';
      // Measure AFTER positioning so hit-testing rects are final.
      winCloseMenu = { el: menu, items: [{ el: item, action: 'os-close', rect: item.getBoundingClientRect() }] };
    }

    function closeWinCloseMenu() {
      if (winCloseMenu) { winCloseMenu.el.remove(); winCloseMenu = null; }
    }

    function winCloseItemAt(x, y) {
      if (!winCloseMenu) return null;
      for (const it of winCloseMenu.items) {
        const b = it.rect;
        if (x >= b.left && x <= b.right && y >= b.top && y <= b.bottom) return it;
      }
      return null;
    }

    // Highlight whatever item the finger is over as it moves.
    function winCloseHover(x, y) {
      if (!winCloseMenu) return;
      const hit = winCloseItemAt(x, y);
      for (const it of winCloseMenu.items) it.el.classList.toggle('active', it === hit);
    }

    // closeOnHost sends the OS-level close and hides the pane at once, so the
    // window is gone as far as the user can tell while the host closes it in
    // the background. The pane itself stays until the host reports the window
    // gone (payload.closed → closePane): an app that raises a "save changes?"
    // sheet instead of closing is still there after WIN_CLOSE_HIDE_MS (the
    // host's own watch on an asked close), so its pane comes back to be answered.
    const WIN_CLOSE_HIDE_MS = 10000;
    function closeOnHost(id) {
      sendWin('window_close', { id });
      const p = panes.get(id);
      if (!p) return;
      p.el.classList.add('closing');
      if (focusedPaneId === id) { focusedPaneId = null; p.el.classList.remove('focused'); }
      clearTimeout(p.closingTimer);
      p.closingTimer = setTimeout(() => p.el.classList.remove('closing'), WIN_CLOSE_HIDE_MS);
    }

    function wireCloseButton(btn, id) {
      let g = null;
      btn.addEventListener('pointerdown', (e) => {
        e.stopPropagation();
        g = { startY: e.clientY, tried: false, pid: e.pointerId };
        try { btn.setPointerCapture(e.pointerId); } catch (_) {}
      });
      btn.addEventListener('pointermove', (e) => {
        if (!g) return;
        e.stopPropagation();
        if (!g.tried && g.startY - e.clientY > 8) { g.tried = true; openWinCloseMenu(btn); }
        if (g.tried) winCloseHover(e.clientX, e.clientY);
      });
      const finish = (e) => {
        if (!g) return;
        e.stopPropagation();
        try { btn.releasePointerCapture(g.pid); } catch (_) {}
        const dragged = g.tried;
        g = null;
        if (!dragged) return; // a plain tap — the click handler below hides the pane
        winCloseDragEnd = e.timeStamp;
        const hit = winCloseItemAt(e.clientX, e.clientY);
        closeWinCloseMenu();
        if (hit && hit.action === 'os-close') closeOnHost(id);
      };
      btn.addEventListener('pointerup', finish);
      btn.addEventListener('pointercancel', (e) => {
        if (g) { e.stopPropagation(); try { btn.releasePointerCapture(g.pid); } catch (_) {} g = null; }
        closeWinCloseMenu();
      });
      // Tap = hide the pane locally (the ✕'s original job). Suppressed for a beat
      // after a drag so the release that picked a menu item doesn't also hide it.
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        if (e.timeStamp - winCloseDragEnd < 400) return;
        closePane(id);
      });
    }

    function openWinView(w, geom) {
      if (panes.has(w.id)) { focusPane(w.id); return; }
      const el = document.createElement('div');
      el.className = 'wpane';
      // geom (from a restored layout) reuses the last position/size; otherwise
      // cascade a fresh pane so stacked ones don't hide each other.
      const minTop = headerBottom();
      if (geom && Number.isFinite(geom.left)) {
        el.style.width = (geom.width || Math.min(window.innerWidth * 0.72, 480)) + 'px';
        el.style.left = Math.max(0, Math.min(geom.left, window.innerWidth - 80)) + 'px';
        el.style.top = Math.max(minTop, geom.top || minTop) + 'px';
      } else {
        el.style.width = Math.min(window.innerWidth * 0.72, 480) + 'px';
        el.style.left = (24 + spawnOffset) + 'px';
        el.style.top = (minTop + spawnOffset) + 'px';
        spawnOffset = (spawnOffset + 28) % 140;
      }

      const bar = document.createElement('div'); bar.className = 'wpane-bar';
      const title = document.createElement('span'); title.className = 'wpane-title';
      title.textContent = (w.app || 'Window') + (w.title ? ' — ' + w.title : '');
      const spacer = document.createElement('span'); spacer.className = 'wpane-spacer';
      const popBtn = document.createElement('button'); popBtn.className = 'wpane-btn wpane-pop'; popBtn.innerHTML = ICON_POPOUT; popBtn.title = 'Open in its own browser window';
      const infoBtn = document.createElement('button'); infoBtn.className = 'wpane-btn wpane-info-btn'; infoBtn.innerHTML = ICON_INFO; infoBtn.title = 'Connection details';
      const kbdBtn = document.createElement('button'); kbdBtn.className = 'wpane-btn wpane-kbd'; kbdBtn.innerHTML = '<span class="wkbd-knob">' + ICON_KEYBOARD + '</span>';
      kbdBtn.setAttribute('role', 'switch'); kbdBtn.setAttribute('aria-pressed', 'false');
      kbdBtn.title = 'Tap to type: off';
      const closeBtn = document.createElement('button'); closeBtn.className = 'wpane-btn wpane-close'; closeBtn.innerHTML = ICON_CLOSE; closeBtn.title = 'Close';
      // Slot rather than a built pill: notes arrive and change independently of
      // the pane, so the pane keeps a mount point and re-fills it.
      const notesSlot = document.createElement('span'); notesSlot.className = 'wpane-notes';
      bar.append(title, spacer, notesSlot, popBtn, infoBtn, kbdBtn, closeBtn);

      const body = document.createElement('div'); body.className = 'wpane-body';
      // Frames render onto a <canvas>, not an <img>: drawImage is atomic and
      // keeps the previous frame until the next draw, so there's no blank flash
      // between frames (an <img> src-swap flickers on iOS Safari at high fps).
      const img = document.createElement('canvas'); img.className = 'wpane-img';
      // Intrinsic size reserves the correct aspect (CSS scales it to the pane
      // width) so the pane doesn't open as a 0-height bar and jump on first frame.
      if (w.w && w.h) { img.width = w.w; img.height = w.h; }
      const ctx = img.getContext('2d', { alpha: false, desynchronized: true });
      const cursorEl = document.createElement('div'); cursorEl.className = 'wpane-cursor';
      const stallEl = document.createElement('div'); stallEl.className = 'wpane-stall';
      const stallMsg = document.createElement('div'); stallMsg.className = 'pill';
      stallEl.append(stallMsg);
      // Loading spinner shown until the first frame lands.
      const loadEl = document.createElement('div'); loadEl.className = 'wpane-load';
      const loadMsg = document.createElement('div'); loadMsg.className = 'wpane-loadmsg'; loadMsg.textContent = 'Connecting…';
      loadEl.append(document.createElement('div'), loadMsg); loadEl.firstChild.className = 'wpane-spin';
      body.append(img, cursorEl, stallEl, loadEl);
      const rz = document.createElement('div'); rz.className = 'wpane-resize';
      // Transport/details popover (toggled by the (i) button).
      const infoEl = document.createElement('div'); infoEl.className = 'wpane-info';
      el.append(bar, body, infoEl, rz);
      document.body.appendChild(el);

      const pane = { el, img, ctx, body, cursorEl, notesSlot, w: Object.assign({}, w), lastUrl: null, cursor: null, placeCursor: null, zoom: 1, panX: 0, panY: 0, lastFrameAt: performance.now(), lastAliveAt: performance.now(), stallMsg, loadMsg, gotFrame: false, errorMsg: null, frameTimes: [], infoEl, infoShown: false, kbdMode: false };
      panes.set(w.id, pane);
      refreshPaneNotes(w.id, pane);
      paneDrag(pane, bar);
      paneResize(pane, rz);
      paneInput(pane, img);
      // A pane is its own gesture surface. Stop bubbling pointer/touch/wheel
      // events into page-level docks, terminal selection and scroll handlers.
      // Target handlers still run first; this fence is on the pane's bubble
      // phase, while the capture-phase globals explicitly ignore .wpane above.
      el.addEventListener('pointerdown', (e) => { e.stopPropagation(); focusPane(w.id); });
      for (const type of ['pointermove', 'pointerup', 'pointercancel', 'click', 'dblclick', 'contextmenu', 'wheel']) {
        el.addEventListener(type, (e) => e.stopPropagation());
      }
      for (const type of ['touchstart', 'touchmove', 'touchend', 'touchcancel']) {
        el.addEventListener(type, (e) => {
          e.stopPropagation();
          // The pane currently owns every touch gesture. If native scrollable
          // or selectable controls are added inside it later, exempt those
          // targets here before preventing touchmove's browser default.
          if (type === 'touchmove') e.preventDefault();
        }, { passive: false });
      }
      for (const type of ['gesturestart', 'gesturechange', 'gestureend']) {
        el.addEventListener(type, (e) => { e.preventDefault(); e.stopPropagation(); }, { passive: false });
      }
      wireCloseButton(closeBtn, w.id);
      popBtn.addEventListener('click', (e) => { e.stopPropagation(); popOutPane(pane); });
      // One owner of the toggle's state and its chrome, so the tap path and the
      // restore path cannot drift apart.
      function setKbdMode(on, raiseNow) {
        pane.kbdMode = on;
        kbdBtn.setAttribute('aria-pressed', on ? 'true' : 'false');
        kbdBtn.title = on ? 'Tap to type: on' : 'Tap to type: off';
        // Arming by hand is itself the request to type, so raise the keyboard
        // then. Disarming puts it away: pane taps preventDefault, which
        // suppresses the browser's own tap-elsewhere blur, so nothing else would.
        if (on && raiseNow) { focusPane(w.id); wkey.focus(); }
        else if (!on && document.activeElement === wkey) wkey.blur();
      }
      kbdBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        setKbdMode(!pane.kbdMode, true);
        savePaneLayout();   // remembered like the window's position and size
      });
      // Reopening a remembered layout restores the mode ARMED BUT SILENT: the
      // next tap raises the keyboard, where raising it here would throw it up
      // over a page the user has only just loaded and never asked to type into.
      if (geom && geom.kbd) setKbdMode(true, false);
      infoBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        pane.infoShown = !pane.infoShown;
        if (pane.infoShown) infoEl.innerHTML = paneInfoHtml(pane);
        infoEl.classList.toggle('show', pane.infoShown);
      });
      infoEl.addEventListener('pointerdown', (e) => e.stopPropagation()); // don't drag/focus when interacting

      focusPane(w.id);
      sendPaneStart(pane);
      announcePaneInterest();
      savePaneLayout();
    }

    // paneAspect: the mirrored window's TRUE aspect. The canvas bitmap is the
    // authority — the agent stamps frames with the window's LISTING-time w/h,
    // which goes stale the moment the window is resized on the host (the bitmap
    // itself is always current; Retina scaling changes both axes equally, so the
    // ratio is unaffected). Listing dims are only a fallback before any frame.
    function paneAspect(pane) {
      if (pane.img && pane.img.width && pane.img.height) return pane.img.width / pane.img.height;
      if (pane.w.w && pane.w.h) return pane.w.w / pane.w.h;
      return 1.5;
    }

    // frameRect: the sub-rectangle the FRAME actually occupies inside a canvas
    // element (viewport coords). A standalone pane (?pane=) fills its tab with
    // object-fit:contain, which letterboxes when the tab aspect differs — so
    // the drawn image is centered within the element and smaller than it. The
    // in-page canvas has box aspect == frame aspect (width:100%/height:auto),
    // so this returns the full box unchanged.
    function frameRect(r, iw, ih) {
      if (!iw || !ih || !r.width || !r.height) return { left: r.left, top: r.top, width: r.width, height: r.height };
      const boxA = r.width / r.height, imgA = iw / ih;
      let dw, dh;
      if (imgA > boxA) { dw = r.width; dh = r.width / imgA; }
      else { dh = r.height; dw = r.height * imgA; }
      return { left: r.left + (r.width - dw) / 2, top: r.top + (r.height - dh) / 2, width: dw, height: dh };
    }

    // ---- Pop-out: the pane as a viewer of its own ---------------------------
    // Pop out = navigate a new tab to this same viewer in standalone pane
    // mode. That tab owns its socket, decoder, acks and timers, so nothing it
    // depends on lives in a context the platform may throttle or freeze. The
    // previous design drove a canvas inside the popup's document from here,
    // which behaved differently on every platform and failed outright on
    // Android, where window.open yields a TAB and this page stops executing
    // the moment you look at the other one.
    //
    // The URL carries NO PIN. An owner connects PIN-free; otherwise the new
    // tab inherits a copy of this tab's sessionStorage credential, which the
    // spec hands to a window.open child. A link pasted onto another device
    // therefore leaks nothing and simply prompts.
    function popOutPane(pane) {
      const url = new URL(location.href);
      // KEEP the hash: the PIN lives there and authenticates end-to-end.
      // Stripping it left the popup permanently on "Session + PIN required",
      // which reads as a window that opened and froze.
      url.search = '?s=' + encodeURIComponent((sessionId || '').toUpperCase()) +
                   '&pane=' + encodeURIComponent(pane.w.id);
      // Size to the source aspect where the platform honours it (desktop);
      // Android ignores this and opens a tab, which is now fine.
      const ar = paneAspect(pane);
      let pw = Math.max(320, Math.min(Math.round(pane.w.w || 900), Math.round((screen.availWidth || 1280) * 0.85)));
      let ph = Math.round(pw / ar);
      const maxH = Math.round((screen.availHeight || 800) * 0.85);
      if (ph > maxH) { ph = maxH; pw = Math.round(ph * ar); }
      let pop = null;
      try {
        pop = window.open(url.toString(), 'reminal-pane-' + pane.w.id,
                          'popup=yes,width=' + pw + ',height=' + ph);
      } catch (_) {}
      if (!pop) {
        pane.infoEl.innerHTML = '<div style="padding:8px 10px">Pop-up blocked — allow pop-ups for this site, then press \u29C9 again.</div>';
        pane.infoEl.classList.add('show');
        pane.infoShown = true;
        return;
      }
      try { pop.focus(); } catch (_) {}
      // That tab streams this window itself now. Hand the stream over rather
      // than stopping it — the host keeps exactly one stream per window, so
      // the popup adopts this one instead of racing to restart it.
      closePane(pane.w.id, true);
    }

    // handOff tears the pane down locally but leaves the host streaming, for
    // pop-out: the popup adopts the running stream. Sending stop here raced the
    // popup's start — one stream exists per window and start is a no-op when it
    // is already running, so a stop landing after the popup's start killed the
    // feed and nothing asked again. The stream is not orphaned if the popup
    // never arrives: the viewer-count and ack-idle paths already stop a stream
    // nobody is watching.
    function closePane(id, handOff) {
      const p = panes.get(id);
      if (!p) return;
      clearTimeout(p.closingTimer);
      if (!handOff) sendWin('window_ctl', { action: 'stop', id, viewer: viewerId });
      if (p.lastUrl) URL.revokeObjectURL(p.lastUrl);
      try { if (p.vdec && p.vdec.state !== 'closed') p.vdec.close(); } catch (_) {}
      if (p.renderRAF) { cancelAnimationFrame(p.renderRAF); p.renderRAF = 0; }
      while (p.renderQ && p.renderQ.length) { try { p.renderQ.shift().f.close(); } catch (_) {} }
      p.el.remove();
      panes.delete(id);
      if (focusedPaneId === id) focusedPaneId = null;
      announcePaneInterest();
      savePaneLayout();
    }

    function closeAllPanes() {
      // Bulk close (session switch / disconnect) — don't rewrite the saved
      // layout to empty; we want to reopen these on reconnect.
      suppressPaneSave = true;
      for (const id of [...panes.keys()]) closePane(id);
      suppressPaneSave = false;
    }

    // Pause window streaming while this tab is backgrounded. A hidden tab can't
    // show frames, so continuing to stream them only burns relay requests (every
    // frame and its ack is a billed Durable Object request). Ask the agent to
    // stop each open pane's stream when we're hidden and restart when we return;
    // the last frame stays painted, so panes look frozen — not blank — meanwhile.
    // Panes aren't closed, so the layout and inputs are untouched.
    let winStreamsPaused = false;
    function pauseWinStreams() {
      if (winStreamsPaused || !panes.size) return;
      winStreamsPaused = true;
      for (const [id, p] of panes) {
        sendWin('window_ctl', { action: 'stop', id, viewer: viewerId });
      }
    }
    // A restarted stream numbers frames from zero again, so every cursor that
    // remembers the OLD numbering has to be cleared — otherwise the dedup
    // guards drop the whole new stream as stale/duplicate and the pane stays
    // blank. JPEG needs lastSeq; video also needs its own sequence and sync
    // state, which is easy to forget separately.
    function resetPaneStreamCursors(p) {
      p.lastSeq = null;
      p.h264NextSeq = null;
      p.h264Synced = false;
      p.h264Hold = null; // a new stream renumbers from zero; held units are orphans
    }

    // Re-arm every open pane. The host starts a NEW stream, numbering from
    // zero, so a pane still holding cursors from the old one discards
    // everything that arrives as already-seen — a frozen picture that the
    // overlay reports as healthy, because frames really are coming in and only
    // fail further down. Both callers need the same reset, so neither writes
    // its own: restartWinStreams used to reset the dedup cursor alone and
    // leave the decoder's, which froze every pane on a reconnect for good.
    function rearmWinStreams() {
      winStreamsPaused = false;
      for (const [id, p] of panes) {
        resetPaneStreamCursors(p);
        sendPaneStart(p);
      }
    }
    function resumeWinStreams() {
      if (!winStreamsPaused) return; // returning to a tab that never paused
      rearmWinStreams();
    }
    // Re-arm every open pane's stream (popped-out ones included — their
    // agent-side streams died with the drop just the same).
    function restartWinStreams() { rearmWinStreams(); }
    document.addEventListener('visibilitychange', () => {
      if (document.hidden) { pauseWinStreams(); return; }
      resumeWinStreams();
      fastReconnect();
    });

    // ---- WebRTC peer-to-peer frame transport --------------------------------
    // Window frames are the heaviest traffic and the relay bills per forwarded
    // message, so when a DataChannel to the agent can be opened, frames + acks
    // go directly peer-to-peer (DTLS-encrypted) instead of through the relay.
    // Signaling rides the existing encrypted channel (see sendWin); the agent
    // is the offerer and owns the "frames" channel. If the connection never
    // forms we transparently stay on the WS path — nothing else changes.
    const RTC_ICE = [{ urls: 'stun:stun.l.google.com:19302' }];
    // Stable for the life of this tab. rtcPeerId is re-minted on every
    // negotiation attempt, so keying the host's viewer records by it left one
    // stale record per retry — fine for "can anyone decode h264", useless for
    // counting viewers.
    let rtcPc = null, rtcDc = null, rtcPeerId = null, rtcPendingIce = [];
    // The "ctl" channel: acks, keyframe requests and heartbeats, on their own
    // unreliable+unordered stream. They used to ride the frame channel, where
    // SCTP queued them behind every buffered access unit — so the moment a
    // bitrate spike backed the channel up, the acks that tell the host we're
    // alive were stuck behind exactly the backlog they needed to report. The
    // host read that as a departed viewer, demoted the transport, then killed
    // the stream, and the pane said the host was asleep. Kept separate so a
    // congested picture can never mute the conversation about it.
    let rtcCtl = null;
    // The "input" channel: mouse/keyboard/scroll events, viewer→agent. Reliable
    // and ordered (unlike frames/ctl), because a dropped or reordered input
    // event leaves the remote in a wrong state — a stuck modifier, a click that
    // lands before the focus it followed. It exists so input skips the billed WS
    // relay: an event used to make a full round-trip through the Cloudflare
    // Durable Object, and that hop was the single biggest reason typing and
    // clicking felt remote. Falls back to WS whenever it isn't open.
    let rtcInput = null;
    let rtcRetryAt = 0;  // don't (re)attempt P2P before this time (ms epoch)
    let rtcHelloAt = 0;  // when the current webrtc_hello was sent (to time out silence)
    let rtcRelayed = false; // is the P2P path going through a TURN relay (vs direct)?
    let lastCapAt = 0;      // last capability announcement (relay-only viewers)
    // Can this browser hardware-decode H.264 via WebCodecs? Declared in the
    // webrtc_hello: the agent then sends compressed video AUs over the channel
    // instead of JPEGs (measured ~10× less bandwidth at the same quality). The
    // stream is Annex-B (SPS/PPS inline on keyframes), so no description blob
    // is needed. A runtime decode error clears this and renegotiates — the
    // agent falls back to JPEG, nothing breaks.
    let h264Decode = false;
    const H264_CODEC = 'avc1.4d0028'; // Main profile (what the host encodes), level 4.0
    const h264DecodeReady = (async () => {
      try {
        // ?h264=0 forces the JPEG path for a side-by-side comparison on a real
        // device — the fastest way to tell a decoder problem from everything
        // else, with no rebuild and nothing to install.
        if (/[?&]h264=0/.test(location.search)) return;
        if (typeof VideoDecoder !== 'undefined' && VideoDecoder.isConfigSupported) {
          const c = await VideoDecoder.isConfigSupported({ codec: H264_CODEC, optimizeForLatency: true });
          h264Decode = !!(c && c.supported);
        }
      } catch (_) { h264Decode = false; }
    })();
    function rtcActive() { return !!(rtcDc && rtcDc.readyState === 'open'); }
    function teardownWebRTC() {
      try { if (rtcDc) rtcDc.close(); } catch (_) {}
      try { if (rtcCtl) rtcCtl.close(); } catch (_) {}
      try { if (rtcInput) rtcInput.close(); } catch (_) {}
      try { if (rtcPc) rtcPc.close(); } catch (_) {}
      rtcPc = null; rtcDc = null; rtcCtl = null; rtcInput = null; rtcPeerId = null; rtcPendingIce = []; rtcRelayed = false;
    }
    function rtcCtlOpen() { return !!(rtcCtl && rtcCtl.readyState === 'open'); }
    // Inspect the live ICE candidate pair to tell a direct P2P path from a TURN
    // relay — a 'relay' candidate on either end means we're going through TURN.
    async function refreshRtcRoute() {
      if (!rtcPc) return;
      try {
        const stats = await rtcPc.getStats(); const c = {}; let sel = null;
        stats.forEach((r) => {
          if (r.type === 'local-candidate' || r.type === 'remote-candidate') c[r.id] = r;
          if (r.type === 'candidate-pair' && r.state === 'succeeded' && (r.nominated || r.selected)) sel = r;
        });
        if (sel) {
          const l = c[sel.localCandidateId], rm = c[sel.remoteCandidateId];
          rtcRelayed = (l && l.candidateType === 'relay') || (rm && rm.candidateType === 'relay');
        }
      } catch (_) {}
    }
    // Tear down and back off before retrying. We ALWAYS prefer P2P and keep
    // retrying (see the interval below), so this just avoids hammering a network
    // where it can't connect; on a deliberate re-try (network changed) pass
    // cooldownMs 0 to reconnect immediately. The agent keeps frames on WS until
    // a channel proves it can carry them, so a failed attempt never stalls video.
    function retryWebRTC(reason, cooldownMs) {
      if (DEBUG) dbg('rtc retry (' + reason + ')');
      teardownWebRTC();
      rtcRetryAt = Date.now() + (cooldownMs || 0);
    }
    // ?p2p=0 keeps WebRTC off entirely so everything rides the relay — the
    // only way to exercise the relay video path from a device that would
    // otherwise connect peer-to-peer straight away.
    const NO_P2P = /[?&]p2p=0/.test(location.search);
    async function ensureWebRTC() {
      if (NO_P2P) return;
      if (!cryptoKey || !agentLive || rtcPeerId || Date.now() < rtcRetryAt || typeof RTCPeerConnection === 'undefined') return;
      await h264DecodeReady; // settled in ms at load; never send a hello with a stale capability
      rtcPeerId = ((crypto.randomUUID && crypto.randomUUID()) || String(Math.random()).slice(2)).slice(0, 12);
      rtcHelloAt = Date.now();
      if (DEBUG) dbg('▶ webrtc_hello ' + rtcPeerId + (h264Decode ? ' +h264' : ''));
      // v2 asks the host for the split-channel transport: a separate ctl
      // channel, and time-limited unordered frame delivery. Declared rather
      // than assumed because the host is the offerer — an older viewer must
      // keep getting exactly one reliable ordered channel, or it would bind
      // its frame handler to whichever channel happened to open last.
      try { await sendWin('webrtc_hello', { peer: rtcPeerId, viewer: viewerId, h264: h264Decode, v2: true, panes: panes.size }); } catch (_) { rtcPeerId = null; }
    }
    // Capability announcement for viewers that will never open a DataChannel
    // (P2P disabled, or blocked by the network). The agent tracks decode
    // support per viewer to decide whether it can put video on the relay, and
    // it learns it from this same hello — no peer connection required. Re-sent
    // periodically so the record stays fresh (see viewerCapTTL).
    async function announceCaps() {
      if (!cryptoKey || !agentLive) return;
      await h264DecodeReady;
      // caps_only: a plain hello makes the host build a PeerConnection and an
      // offer, so this could not be repeated without leaving a peer to reap
      // each time. panes tells the host whether we are asking for frames at
      // all — a tab with none must not be counted as a viewer needing the
      // relay copy, which is what held a popped-out window at half rate.
      try {
        await sendWin('webrtc_hello', {
          peer: viewerId, viewer: viewerId, h264: h264Decode,
          panes: panes.size, caps_only: true,
        });
      } catch (_) {}
    }
    // Report whenever what we want changes, so the host reacts to a pop-out or
    // a closed pane at once rather than on the next periodic refresh.
    // Matches winCapAnnounceInterval on the host, which sizes its record
    // lifetime around it.
    const WIN_CAP_ANNOUNCE_MS = 25000;
    function announcePaneInterest() { lastCapAt = Date.now(); announceCaps(); }
    async function onWebRTCOffer(payload) {
      if (!payload || payload.peer !== rtcPeerId || rtcPc) return;
      // Use the ICE servers the agent sent (STUN + any TURN it's configured
      // with) so both peers try the same relays; fall back to STUN-only.
      const pc = new RTCPeerConnection({ iceServers: (payload.ice && payload.ice.length) ? payload.ice : RTC_ICE });
      rtcPc = pc;
      pc.onicecandidate = (e) => {
        if (!e.candidate) return;
        sendWin('webrtc_ice', { peer: rtcPeerId, candidate: e.candidate.candidate, mid: e.candidate.sdpMid, line: e.candidate.sdpMLineIndex });
      };
      pc.onconnectionstatechange = () => {
        if (DEBUG) dbg('rtc ' + pc.connectionState);
        // Only terminal states trigger a teardown+retry. 'disconnected' is
        // transient (missed ICE keepalives — routine on phones during WiFi
        // power-save naps) and normally self-heals to 'connected' in seconds;
        // retrying on it tore down a healthy connection on every radio nap and
        // flapped the transport webrtc↔relay with no real network change. If a
        // blip doesn't heal, the browser escalates to 'failed' and we land here.
        if (rtcPc === pc && (pc.connectionState === 'failed' || pc.connectionState === 'closed')) retryWebRTC('conn ' + pc.connectionState, 12000);
      };
      pc.ondatachannel = (e) => {
        const ch = e.channel;
        // Two channels now arrive, so route by label rather than by whichever
        // opened last. 'ctl' carries only small JSON (heartbeats in this
        // direction); everything else is the frame channel.
        if (ch.label === 'ctl') {
          rtcCtl = ch;
          ch.onmessage = (ev) => {
            try { onWindowFrame(JSON.parse(typeof ev.data === 'string' ? ev.data : new TextDecoder().decode(ev.data)), true); } catch (_) {}
          };
          ch.onopen = () => { if (DEBUG) dbg('rtc ctl channel open'); };
          ch.onclose = () => { if (rtcCtl === ch) rtcCtl = null; };
          return;
        }
        // The input channel carries events the other way (viewer→agent) and the
        // agent never sends on it, so there's no onmessage — sendWin just writes
        // window_input events here instead of the relay while it's open.
        if (ch.label === 'input') {
          rtcInput = ch;
          ch.onopen = () => { if (DEBUG) dbg('rtc input channel open'); };
          ch.onclose = () => { if (rtcInput === ch) rtcInput = null; };
          return;
        }
        rtcDc = ch;
        ch.binaryType = 'arraybuffer';
        ch.onmessage = (ev) => {
          // Binary messages starting with 0xF2/0xF3 are H.264 frames (see
          // onWindowFrameH264); everything else on the channel is JSON.
          if (typeof ev.data !== 'string') {
            const u = new Uint8Array(ev.data);
            if (u.length > 2 && (u[0] === 0xF2 || u[0] === 0xF3)) { try { onWindowFrameH264(u); } catch (_) {} return; }
          }
          try { onWindowFrame(JSON.parse(typeof ev.data === 'string' ? ev.data : new TextDecoder().decode(ev.data)), true); } catch (_) {}
        };
        ch.onopen = () => { if (DEBUG) dbg('rtc frames channel open'); };
        ch.onclose = () => { if (rtcDc === ch) rtcDc = null; };
      };
      try {
        await pc.setRemoteDescription({ type: 'offer', sdp: payload.sdp });
        for (const c of rtcPendingIce) { try { await pc.addIceCandidate(c); } catch (_) {} }
        rtcPendingIce = [];
        const answer = await pc.createAnswer();
        await pc.setLocalDescription(answer);
        await sendWin('webrtc_answer', { peer: rtcPeerId, sdp: answer.sdp });
      } catch (e) { if (DEBUG) dbg('rtc offer err ' + e); retryWebRTC('offer error', 12000); }
    }
    async function onWebRTCIce(payload) {
      if (!payload || payload.peer !== rtcPeerId) return;
      const cand = { candidate: payload.candidate, sdpMid: payload.mid, sdpMLineIndex: payload.line };
      if (!rtcPc || !rtcPc.remoteDescription) { rtcPendingIce.push(cand); return; }
      try { await rtcPc.addIceCandidate(cand); } catch (_) {}
    }
    // Ack over the SAME transport the frame arrived on. A DataChannel frame
    // acked over the channel is the agent's proof that big frames actually
    // traverse it (small acks alone can't fake it) — so it only then switches
    // frames onto P2P. A WS frame is acked over WS.
    function sendWindowAck(id, seq, viaDC) {
      // Preferred road: the ctl channel. It carries the `dc` flag so the host
      // still learns that a full frame traversed the FRAME channel — that is
      // what confirmation has always meant, and an ack arriving on a channel
      // of its own proves nothing about the other one on its own.
      // Relay-video acks ride ctl too when it is up: same pacing, no Durable
      // Object hop, so they are both faster and free.
      if (rtcCtlOpen()) { try { rtcCtl.send(JSON.stringify({ id, seq, dc: !!viaDC })); return; } catch (_) {} }
      if (viaDC && rtcActive()) { try { rtcDc.send(JSON.stringify({ id, seq })); return; } catch (_) {} }
      sendWin('window_ack', { id, seq });
    }

    // ---- Remember open panes per session ------------------------------------
    // Reconnecting (e.g. via the Recent dropdown) reopens the windows you had
    // up. Keyed by session id; matched back to live windows by id first, then
    // app+title if the agent came back with reassigned window ids.
    const PANES_KEY_PREFIX = 'reminal:panes:';
    function panesKey() { return sessionId ? PANES_KEY_PREFIX + sessionId.toUpperCase() : ''; }
    function savePaneLayout() {
      if (suppressPaneSave || paneOnly) return; // a pane tab owns no layout
      const key = panesKey();
      if (!key) return;
      const arr = [...panes.values()].map((p) => {
        const o = {
          id: p.w.id, app: p.w.app || '', title: p.w.title || '',
          left: Math.round(p.el.offsetLeft), top: Math.round(p.el.offsetTop),
          width: Math.round(p.el.offsetWidth),
        };
        // Written only when armed, so layouts saved before this existed read
        // back as off with no migration.
        if (p.kbdMode) o.kbd = 1;
        return o;
      });
      try {
        if (arr.length) localStorage.setItem(key, JSON.stringify(arr));
        else localStorage.removeItem(key);
      } catch (_) {}
    }
    function restoreSavedPanes(list) {
      const key = panesKey();
      if (!key || !Array.isArray(list) || !list.length) return;
      let saved;
      try { saved = JSON.parse(localStorage.getItem(key) || '[]'); } catch (_) { saved = []; }
      if (!Array.isArray(saved) || !saved.length) return;
      suppressPaneSave = true;
      const used = new Set();
      for (const s of saved) {
        // Exact id match (session still alive) preferred; else same app+title.
        let w = list.find((x) => x.id === s.id && !used.has(x.id));
        if (!w) w = list.find((x) => !used.has(x.id) && (x.app || '') === s.app && (x.title || '') === s.title);
        if (!w) continue;
        used.add(w.id);
        openWinView(w, s);
      }
      suppressPaneSave = false;
      savePaneLayout();
    }

    // Decide what (if anything) a pane's overlay should say: an explicit capture
    // error from the host, a "no video yet" for a pane that never got a frame, or
    // a "waiting" for one whose frames stopped (screen locked/asleep). Turns a
    // silent freeze into a clear, actionable cue. "Alive" counts heartbeats too
    // (an idle window sends 0 frames but heartbeats every ~3s), so STALL_MS sits
    // above the heartbeat interval — an idle window never trips the overlay.
    const STALL_MS = 7000;
    function updatePaneOverlay(p) {
      const stale = performance.now() - (p.lastAliveAt || 0) > STALL_MS;
      let html = null, isErr = false, loading = false;
      if (p.errorMsg) {
        html = '<span class="dot"></span><span>' + escapeHtml(p.errorMsg) + '</span>';
        isErr = true;
      } else if (!p.gotFrame) {
        // No frame ever → show the spinner; after a while, hint at why.
        loading = true;
        p.loadMsg.innerHTML = stale
          ? 'No video yet <span class="sub">the window may be minimized, or capture is blocked</span>'
          : 'Connecting…';
      } else if (stale) {
        html = '<span class="dot"></span><span>Waiting for host… <span class="sub">screen may be locked or asleep</span></span>';
      }
      p.el.classList.toggle('loading', loading);
      if (html) {
        p.stallMsg.innerHTML = html;
        p.el.classList.toggle('stall-error', isErr);
        p.el.classList.add('stalled');
      } else {
        p.el.classList.remove('stalled');
      }
      if (p.infoShown) p.infoEl.innerHTML = paneInfoHtml(p); // keep the popover live
    }
    // The pane/transport housekeeping tick. Named rather than inline because a
    // popped-out pane must be able to drive it from ITS window: everything here
    // lives in the opener, and a browser throttles a hidden tab's timers to
    // roughly once a minute — so with the main window minimized and only a
    // popup on screen, reconnection and re-negotiation would crawl.
    let lastTickAt = 0;
    function viewerTick() {
      const tnow = Date.now();
      if (tnow - lastTickAt < 500) return; // two clocks may drive this; run once
      lastTickAt = tnow;
      // Keepalive. Riding this tick rather than its own interval is what lets
      // a visible popup hold the socket open while the opener is hidden and
      // throttled. Byte-identical to what the relay auto-answers, so it still
      // costs nothing (see setWebSocketAutoResponse in the Worker).
      if (ws && ws.readyState === WebSocket.OPEN && tnow - lastPingAt >= 30000) {
        lastPingAt = tnow;
        try { ws.send(JSON.stringify({ type: 'ping' })); } catch (_) {}
      }
      // Warm sockets too, on the same cadence (answered by the relay without
      // waking anything: setWebSocketAutoResponse).
      for (const e of warmSockets.values()) {
        if (e.ws && e.ws.readyState === WebSocket.OPEN && tnow - e.lastPingAt >= 30000) {
          e.lastPingAt = tnow;
          e.ws.sendRaw(JSON.stringify({ type: 'ping' }));
        }
      }
      // Always prefer P2P: whenever we're not connected/negotiating and the
      // cooldown has passed, (re)attempt WebRTC — so WS upgrades to P2P as soon
      // as it becomes viable (network improved) with no reconnect needed. If a
      // hello went unanswered, clear it so the next tick retries.
      if (rtcPeerId && !rtcPc && Date.now() - rtcHelloAt > 8000) rtcPeerId = null;
      if (!rtcPc) ensureWebRTC();
      // Keep the record alive only while this tab has nothing open. That is the
      // only case the host needs repeated: it subtracts viewers wanting nothing
      // before deciding whether a relay copy of every frame is needed, and a
      // lapsed record makes an idle tab look like one that needs the relay
      // again — which is what dropped a popped-out window back to the capped
      // path shortly after it was handed over. A tab WITH panes needs no
      // repetition: not being in the idle set is already the safe default, so
      // saying so costs relay messages to change nothing.
      // Every one of these is billed, so the cadence is as slow as the host's
      // record lifetime allows rather than as fast as would be convenient.
      // Two reasons to repeat ourselves, and they are not the same reason.
      //
      // Without a live channel this has always run: it is how a viewer that
      // will never open one keeps its decode capability on record, and every
      // host in the field depends on it.
      //
      // With a live channel and nothing open, it exists only to keep the pane
      // count fresh — and it is only safe against a host that RECORDS a
      // caps-only hello and stops. A host that predates that answers every one
      // by building a PeerConnection and an offer, reaped only on a handshake
      // timeout, so repeating them at an older host would have it constructing
      // peers forever for a tab showing nothing. Ask only where it is inert.
      const capStale = !lastCapAt || Date.now() - lastCapAt > WIN_CAP_ANNOUNCE_MS;
      const capsOnlyOK = !!(lastHostInfo && lastHostInfo.caps_only);
      if (capStale && (!rtcActive() || (panes.size === 0 && capsOnlyOK))) {
        lastCapAt = Date.now();
        announceCaps();
      }
      if (rtcActive()) refreshRtcRoute(); // keep direct-vs-TURN current for the (i)
      if (!panes.size) return;
      for (const p of panes.values()) {
        updatePaneOverlay(p);
        // Resolve a reorder hold whose stream went quiet mid-hole: arrivals
        // normally drive this, and there are none to drive it with.
        sweepHeldAUs(p);
        // Self-heal a stream the host gave up on. It ends one whose acks dried
        // up (streamAckIdleTimeout) and sends nothing to say so — the comment
        // there assumes "a returning viewer re-sends window_ctl start", which
        // a POPPED-OUT pane never does: it is exempt from the pause/resume
        // path, so nothing re-requests it and it sits on "Waiting for host"
        // until someone closes and reopens it by hand. Do that automatically.
        const staleFor = performance.now() - (p.lastAliveAt || 0);
        if (p.gotFrame && staleFor > 9000 && (!p.reqAt || performance.now() - p.reqAt > 10000)) {
          p.reqAt = performance.now();
          resetPaneStreamCursors(p);
          sendPaneStart(p);
        }
        // Safety net for playout: if frames are queued and overdue, rAF is not
        // running (every window holding this pane is hidden, or the browser
        // throttled it). Drain to the newest so the pane resumes on its own
        // instead of sitting frozen until something else wakes it.
        if (p.renderQ && p.renderQ.length && performance.now() - p.renderQ[0].at > 500) {
          while (p.renderQ.length > 1) p.renderQ.shift().f.close();
          const last = p.renderQ.shift();
          paintVideoFrame(p, last.f); last.f.close();
          p.nextShowAt = 0;
        }
      }
    }
    setInterval(viewerTick, 1000);

    // What a capture error means, in words a person can act on. macOS reports a
    // stuck screen-capture service (replayd) as "The user declined TCCs …"
    // even when reminal is allowed to record; the host restarts that service
    // itself and the pane recovers on its next retry.
    function paneErrorText(raw) {
      const s = String(raw || '');
      if (/declined TCCs/i.test(s)) {
        return "macOS refused screen capture. If Screen Recording is on for reminal, it is restarting macOS's capture service and this view will recover in a few seconds. If it doesn't, run reminal permissions on that machine.";
      }
      return s;
    }
    function onWindowFrame(payload, viaDC) {
      const p = panes.get(payload.id);
      if (!p) return;
      if (payload.closed) { closePane(payload.id); return; } // window closed on the host
      if (payload.error) { // host could list but not capture this window
        p.errorMsg = paneErrorText(payload.error);
        updatePaneOverlay(p);
        return;
      }
      if (payload.hb) { // liveness heartbeat — host alive, screen just unchanged (0 fps)
        p.errorMsg = null;
        p.lastAliveAt = performance.now();
        noteTransport(viaDC);
        updatePaneOverlay(p);
        return;
      }
      // Dedup: while a DataChannel is being probed the agent sends over WS AND
      // the channel, so the same seq can arrive twice. Render once — but still
      // ack a DataChannel copy so the agent learns the channel carries frames.
      if (p.lastSeq != null && payload.seq != null && payload.seq <= p.lastSeq && (p.lastSeq - payload.seq) < 1000) {
        if (viaDC) { noteTransport(true); sendWindowAck(payload.id, payload.seq, true); }
        return;
      }
      // Batched video over the relay: one message carries several H.264 access
      // units, because the relay bills per message. Same decoder and canvas as
      // the peer-to-peer path — only the container differs.
      if (payload.batch) { onWindowBatch(p, payload, viaDC); return; }
      if (payload.seq != null) p.lastSeq = payload.seq;
      p.errorMsg = null;
      p.gotFrame = true;
      p.lastFrameAt = performance.now();
      p.lastAliveAt = p.lastFrameAt; // frames count as liveness too
      noteTransport(viaDC); // for the (i) transport popover
      if (payload.cap) { p.capSrc = payload.cap; p.capErr = payload.cap_err || null; }
      (p.frameTimes || (p.frameTimes = [])).push(p.lastFrameAt); // rolling 1s window → fps
      while (p.frameTimes.length && p.lastFrameAt - p.frameTimes[0] > 1000) p.frameTimes.shift();
      updatePaneOverlay(p);
      const bin = atob(payload.img);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      const blob = new Blob([bytes], { type: 'image/jpeg' });
      // A changed band: only the part of the picture that changed, a bw×bh
      // JPEG to draw at bx,by over the fw×fh frame `base` left on the canvas.
      // Over any other picture it would be wrong (a frame lost or overtaken
      // on the way, a resize in between), so then nothing is drawn and the
      // host is asked for a whole frame.
      // Any band field makes it a band; bx/by are left out when 0.
      const band = ['bx', 'by', 'bw', 'bh', 'fw', 'fh', 'base'].some((k) => payload[k] !== undefined) ? {
        x: payload.bx === undefined ? 0 : payload.bx, y: payload.by === undefined ? 0 : payload.by,
        w: payload.bw, h: payload.bh, fw: payload.fw, fh: payload.fh, base: payload.base,
      } : null;
      // A band is drawn only if it is whole numbers, lies inside its frame,
      // and decodes to exactly the size it says: anything else is refused
      // like a band against the wrong picture.
      const bandSound = (b, sw, sh) => ['x', 'y', 'w', 'h', 'fw', 'fh', 'base'].every((k) => Number.isInteger(b[k]))
        && b.x >= 0 && b.y >= 0 && b.w > 0 && b.h > 0 && b.x + b.w <= b.fw && b.y + b.h <= b.fh
        && sw === b.w && sh === b.h;
      // Paint onto the canvas: drawImage is atomic and keeps the previous frame
      // until the new one is drawn, so no per-frame blank flicker. createImage-
      // Bitmap decodes off the main thread; fall back to <img> decode → canvas
      // where it's missing (still flicker-free — the canvas is what matters). The
      // ack fires after the draw, keeping the host's frame-pacing backpressure.
      const paint = (src, sw, sh) => {
        const cv = p.img;
        // A band refused touches nothing: not the canvas, not the pane's
        // size, not which frame the canvas holds.
        if (band && (!bandSound(band, sw, sh) || p.canvasSeq !== band.base || cv.width !== band.fw || cv.height !== band.fh)) {
          requestH264Key(p); // the same "send me a whole frame" ask
          if (payload.seq != null) sendWindowAck(payload.id, payload.seq, viaDC);
          return;
        }
        // Keep the logical size current BEFORE the popped-out fit/aspect hooks
        // below — they derive the aspect from p.w, and running them against the
        // previous window's shape is exactly how a host-side resize ended up
        // stretched (new bitmap displayed in CSS sized for the old ratio, with
        // no follow-up frame to heal it — a static window only heartbeats).
        p.w.w = payload.w; p.w.h = payload.h; // keeps click mapping current
        // Setting the canvas's intrinsic size is all the letterbox needs — the
        // CSS max-width/max-height (popup) or width:100%/height:auto (in-page)
        // re-fits to the new aspect automatically, never stretched. We do NOT
        // resize the popup's OS window on a host-side resize: the in-page pane
        // doesn't reshape either (it just letterboxes), and auto-growing the
        // window fights the user's own placement. It only matches aspect once,
        // at open (see popOutPane), then CSS owns it.
        if (band) {
          p.ctx.drawImage(src, band.x, band.y, band.w, band.h);
        } else {
          if (cv.width !== sw || cv.height !== sh) { cv.width = sw; cv.height = sh; }
          p.ctx.drawImage(src, 0, 0, sw, sh);
        }
        p.canvasSeq = payload.seq; // the frame now on the canvas: what a band is cut against
        if (p.placeCursor && p.cursor) p.placeCursor();
        if (payload.seq != null) sendWindowAck(payload.id, payload.seq, viaDC);
      };
      // Ack even if the frame can't be decoded: the ack means "received seq N and
      // kept up", which drives the host's frame pacing AND its liveness check —
      // withholding it on a rare bad frame makes lastAck go stale, so the host can
      // wrongly conclude the viewer vanished (streamAckIdleTimeout) and END a live
      // stream. paint() acks on success; these handle the decode-failure paths.
      const ackDropped = () => { if (payload.seq != null) sendWindowAck(payload.id, payload.seq, viaDC); };
      // Decoded side by side, painted one after another in the order they
      // came: a small frame decodes sooner than a big one ahead of it, and
      // painting whichever is ready first put a band UNDER the frame it was
      // cut against.
      const decoded = window.createImageBitmap
        ? createImageBitmap(blob).then((bmp) => ({ src: bmp, w: bmp.width, h: bmp.height, done: () => bmp.close() }))
        : new Promise((resolve, reject) => {
          const url = URL.createObjectURL(blob);
          const im = new Image();
          im.onload = () => resolve({ src: im, w: im.naturalWidth, h: im.naturalHeight, done: () => URL.revokeObjectURL(url) });
          im.onerror = () => { URL.revokeObjectURL(url); reject(new Error('jpeg')); };
          im.src = url;
        });
      p.paintChain = (p.paintChain || Promise.resolve()).then(() => decoded).then(
        (d) => { try { paint(d.src, d.w, d.h); } finally { d.done(); } }, ackDropped,
      ).catch(() => {});
    }

    // ---- H.264 frames over the DataChannel (WebCodecs) ----------------------
    // Binary framing (agent→viewer): [0]=0xF2 magic, [1]=flags (bit0 key AU,
    // bit1 more chunks follow), [2]=id length L, [3..3+L]=window id, then
    // seq (BE u64), w (BE u32), h (BE u32), then one Annex-B access unit (or a
    // chunk of one — chunks of an AU share a seq and arrive in order on the
    // reliable channel). Decoded with a per-pane VideoDecoder into the same
    // canvas the JPEG path paints; acks ride the channel per AU after the
    // decoded frame is drawn, so the host's pacing loop is codec-agnostic.
    function onWindowFrameH264(u) {
      const v2 = u[0] === 0xF3;
      const flags = u[1];
      // v2 inserts a chunk index and count between the flags and the id.
      const chunkIdx = v2 ? u[2] : 0, chunkCount = v2 ? u[3] : 0;
      const base = v2 ? 5 : 3;
      const idLen = u[base - 1];
      const hdrLen = base + idLen + 8 + 4 + 4;
      if (u.length < hdrLen) return;
      let id = '';
      for (let i = 0; i < idLen; i++) id += String.fromCharCode(u[base + i]);
      const dv = new DataView(u.buffer, u.byteOffset + base + idLen, 16);
      const seq = Number(dv.getBigUint64(0));
      const w = dv.getUint32(8), h = dv.getUint32(12);
      const p = panes.get(id);
      if (!p) return;
      const chunk = u.subarray(hdrLen);

      let au = chunk;
      if (v2) {
        // The v2 channel is unordered and drops what it can't deliver in time,
        // so chunks of one access unit may arrive in any order and some may
        // never arrive at all. Slot each by its stated index and only decode
        // once the set is complete; an incomplete one is simply abandoned when
        // a newer AU supersedes it, and the resulting sequence gap makes
        // submitAU ask for a keyframe — the same recovery a lost single-message
        // AU already gets.
        if (chunkCount > 1) {
          if (!p.h264Parts || p.h264PartsSeq !== seq) {
            // Only ever hold one AU's worth: a partial set for an older seq is
            // already useless, because decoding cannot skip it and resume.
            p.h264Parts = new Array(chunkCount);
            p.h264PartsSeq = seq;
            p.h264PartsHave = 0;
          }
          if (p.h264Parts[chunkIdx] === undefined) { p.h264Parts[chunkIdx] = chunk; p.h264PartsHave++; }
          if (p.h264PartsHave < chunkCount) return; // still waiting on the rest
          let total = 0;
          for (const c of p.h264Parts) total += c.length;
          au = new Uint8Array(total);
          let off = 0;
          for (const c of p.h264Parts) { au.set(c, off); off += c.length; }
          p.h264Parts = null;
        }
      } else {
        // v1: a reliable ordered channel, so arrival position IS the index.
        if (flags & 2) { // more chunks of this AU follow
          if (!p.h264Parts || p.h264PartsSeq !== seq) { p.h264Parts = []; p.h264PartsSeq = seq; }
          p.h264Parts.push(chunk);
          return;
        }
        if (p.h264Parts && p.h264PartsSeq === seq && p.h264Parts.length) {
          let total = 0;
          for (const c of p.h264Parts) total += c.length;
          au = new Uint8Array(total + chunk.length);
          let off = 0;
          for (const c of p.h264Parts) { au.set(c, off); off += c.length; }
          au.set(chunk, off);
        }
        p.h264Parts = null;
      }

      const key = !!(flags & 1);
      // Shared counter with the JPEG path. Kept monotonic because an unordered
      // channel can deliver an older unit after a newer one, and letting this
      // walk backwards would make the dedup guard discard live frames.
      if (p.lastSeq == null || seq > p.lastSeq) p.lastSeq = seq;
      noteVideoFrame(p, w, h, true);
      markFrameTime(p);
      updatePaneOverlay(p);
      // Acked individually here (one AU per message); the relay path acks once
      // per batch instead, so it passes no ack sequence.
      submitAU(p, au, key, seq, seq);
    }

    // noteVideoFrame marks a pane live and current for either video path —
    // peer-to-peer or batched over the relay. The two differ only in container,
    // so everything downstream of "a frame arrived" belongs here.
    function noteVideoFrame(p, w, h, viaDC) {
      p.errorMsg = null;
      p.gotFrame = true;
      p.lastAliveAt = p.lastFrameAt = performance.now();
      noteTransport(viaDC);
      p.capSrc = 'h264';
      p.capErr = null;
      p.w.w = w; p.w.h = h; // keeps click mapping current
    }

    // markFrameTime keeps the rolling one-second window the (i) popover reads
    // as a frame rate. Called per access unit, so a batch counts as the several
    // frames it actually carries rather than one.
    function markFrameTime(p) {
      p.lastFrameAt = performance.now();
      (p.frameTimes || (p.frameTimes = [])).push(p.lastFrameAt);
      while (p.frameTimes.length && p.lastFrameAt - p.frameTimes[0] > 1000) p.frameTimes.shift();
    }

    // onWindowBatch unpacks a relay batch: [uint32 len][flag][Annex-B AU]... ,
    // base64'd once for the whole group. Access units are consecutive, so the
    // first sequence number plus position identifies each one, which keeps gap
    // detection working across batches. Only the last is acked — one ack per
    // message is the point, since acks are billed too.
    function onWindowBatch(p, payload, viaDC) {
      if (!h264Decode) {
        // We can't decode this. Tell the host so it drops back to JPEG for
        // everyone (the relay is a broadcast — it has no per-viewer choice).
        if (!p.h264Refused) { p.h264Refused = true; h264Decode = false; announceCaps(); }
        return;
      }
      let blob;
      try {
        const bin = atob(payload.batch);
        blob = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) blob[i] = bin.charCodeAt(i);
      } catch (_) { return; }

      noteVideoFrame(p, payload.w, payload.h, viaDC);
      p.h264Batches = (p.h264Batches || 0) + 1; // relay messages — the billed unit

      let off = 0, i = 0;
      const seq0 = payload.seq0 != null ? payload.seq0 : payload.seq;
      while (off + 4 <= blob.length) {
        const len = ((blob[off] << 24) | (blob[off + 1] << 16) | (blob[off + 2] << 8) | blob[off + 3]) >>> 0;
        off += 4;
        if (len < 1 || off + len > blob.length) break;
        submitAU(p, blob.subarray(off + 1, off + len), blob[off] === 2, seq0 + i);
        off += len;
        i++;
        markFrameTime(p);
      }
      p.lastSeq = seq0 + Math.max(0, i - 1);
      p.h264BatchAck = payload.seq != null ? payload.seq : p.lastSeq;
      updatePaneOverlay(p);
      // Ack once for the whole batch, over the transport it arrived on.
      if (p.h264BatchAck != null) sendWindowAck(p.w.id, p.h264BatchAck, viaDC);
    }

    // Reorder tolerance for the v2 frame channel, which delivers unordered and
    // abandons what it can't get through in time. A sequence number running
    // ahead of what we expect no longer means the missing unit is lost — most
    // often it is a retransmit still in flight, arriving just behind the frames
    // that overtook it. Hold early arrivals this long before calling it a gap.
    // The relay path and v1 channels deliver in order, match the expected
    // sequence every time and never enter the buffer, so they pay nothing for
    // this. H264_REORDER_MAX is the parallel bound: a hole that far behind the
    // newest arrival is not coming back, whatever the clock says.
    const H264_REORDER_MS = 80;
    const H264_REORDER_MAX = 12;

    // submitAU feeds one access unit to the pane's decoder, applying the
    // ordering, gap and resync rules. ackSeq is undefined when the caller acks
    // for a whole batch.
    function submitAU(p, au, key, seq, ackSeq) {
      if (!ensureH264Decoder(p)) return;
      // While a DataChannel is proving itself the agent feeds BOTH paths, so
      // the same access unit can arrive twice — and an unordered channel can
      // deliver one we have already moved past. Either way it is spent.
      if (p.h264NextSeq != null && seq < p.h264NextSeq) return;
      if (key) {
        // A keyframe depends on nothing before it, so it is always a valid
        // entry point: take it now, and discard only what it supersedes.
        // Units held BEYOND it are still wanted — dropping those too would
        // manufacture the very gap this keyframe just closed.
        p.h264Synced = true;
        p.h264NextSeq = seq;
        pruneHeldAUs(p, seq);
      } else if (!p.h264Synced) {
        // No entry point yet. Ask for one: joining a stream mid-flight is the
        // normal case for a pane opened on an already-running window (a
        // pop-out adopts one), and without this the pane waits for whatever
        // keyframe the encoder happens to emit next. Throttled inside.
        requestH264Key(p);
        return; // a delta on its own is undecodable
      } else if (p.h264NextSeq == null) {
        p.h264NextSeq = seq;
      }
      if (seq > p.h264NextSeq) { holdAU(p, au, key, seq, ackSeq); return; }
      decodeAU(p, au, key, seq, ackSeq);
      p.h264NextSeq = seq + 1;
      drainHeldAUs(p);
    }

    // holdAU parks an access unit that arrived ahead of the one still missing.
    function holdAU(p, au, key, seq, ackSeq) {
      const hold = p.h264Hold || (p.h264Hold = { at: performance.now(), items: new Map() });
      if (!hold.items.has(seq)) hold.items.set(seq, { au, key, seq, ackSeq });
      if (hold.items.size > H264_REORDER_MAX) { giveUpOnGap(p); return; }
      sweepHeldAUs(p);
    }

    // drainHeldAUs decodes whatever the just-decoded unit unblocked.
    function drainHeldAUs(p) {
      const hold = p.h264Hold;
      if (!hold) return;
      for (;;) {
        const it = hold.items.get(p.h264NextSeq);
        if (!it) break;
        hold.items.delete(it.seq);
        decodeAU(p, it.au, it.key, it.seq, it.ackSeq);
        p.h264NextSeq = it.seq + 1;
      }
      if (!hold.items.size) p.h264Hold = null;
      else hold.at = performance.now(); // progress made — restart the patience
    }

    // pruneHeldAUs forgets held units the decoder can no longer use.
    function pruneHeldAUs(p, below) {
      const hold = p.h264Hold;
      if (!hold) return;
      for (const s of [...hold.items.keys()]) if (s < below) hold.items.delete(s);
      if (!hold.items.size) p.h264Hold = null;
    }

    // sweepHeldAUs converts a hole we have waited long enough on into a real
    // gap. Driven by arrivals (fine-grained at 30-60fps) and by viewerTick, so
    // a pane whose frames stop entirely mid-hold still resolves.
    function sweepHeldAUs(p) {
      if (p.h264Hold && performance.now() - p.h264Hold.at > H264_REORDER_MS) giveUpOnGap(p);
    }

    // giveUpOnGap declares the missing unit lost: drop what was waiting on it
    // and ask the host for a self-contained frame to restart from.
    function giveUpOnGap(p) {
      p.h264Hold = null;
      p.h264Gaps = (p.h264Gaps || 0) + 1;
      p.h264Synced = false;
      requestH264Key(p);
    }

    // h264Seqs pairs each submitted access unit with what to do when its frame
    // comes back, by position: one decoder output per decode() call. If the
    // decoder ever accepts a chunk and emits nothing without raising an error,
    // that pairing shifts by one and never recovers — every later frame is then
    // acked with another frame's sequence, which walks the host's delivery
    // accounting backwards and throttles the stream, and is painted with
    // another frame's timing. Silent, permanent, and it looks like plain lag.
    // In normal operation this holds a handful of entries, so a queue this
    // long means the pairing is already lost: drop it and resync on a keyframe
    // rather than carry a corrupt mapping for the life of the pane. 64 was
    // ~2s at 30fps — enough to make clicks feel dead while the counter
    // still read 30. A dozen is past any healthy decoder burst.
    const H264_PENDING_MAX = 12;

    // decodeAU hands one in-order access unit to the decoder.
    function decodeAU(p, au, key, seq, ackSeq) {
      p.h264AUs = (p.h264AUs || 0) + 1;
      // ackSeq is set only on the per-message path; a batch is acked once by
      // its caller, and its frames are the ones that need paced playout.
      (p.h264Seqs || (p.h264Seqs = [])).push({ ack: ackSeq !== undefined ? ackSeq : null, paced: ackSeq === undefined })
      if (p.h264Seqs.length > H264_PENDING_MAX) {
        p.h264Seqs = [];
        p.h264Synced = false;
        requestH264Key(p);
        return;
      }
      try {
        p.vdec.decode(new EncodedVideoChunk({
          type: key ? 'key' : 'delta',
          timestamp: seq * 33333, // us; synthetic but monotonic — nothing paces on it
          data: au,
        }));
      } catch (e) {
        p.h264Seqs.pop();
        h264Fail(p, e);
      }
    }

    // paintVideoFrame draws one decoded frame onto the pane's canvas. Shared by
    // the immediate and the paced paths, and deliberately identical to what the
    // JPEG path does so pop-out aspect and the cursor overlay behave the same.
    function paintVideoFrame(p, frame) {
      const cv = p.img, sw = frame.displayWidth, sh = frame.displayHeight;
      if (cv.width !== sw || cv.height !== sh) { cv.width = sw; cv.height = sh; }
      p.ctx.drawImage(frame, 0, 0, sw, sh);
      p.canvasSeq = null; // not a picture any band is cut against
      if (p.placeCursor && p.cursor) p.placeCursor();
      // Inter-paint spacing is what motion actually looks like, as distinct
      // from how many frames arrived — the two came apart badly once batching
      // landed, so it's measured.
      const now = performance.now();
      if (p.lastPaintAt) (p.paintGaps || (p.paintGaps = [])).push(now - p.lastPaintAt);
      if (p.paintGaps && p.paintGaps.length > 120) p.paintGaps.shift();
      p.lastPaintAt = now;
    }

    // queueVideoFrame schedules a batched frame for its turn on screen instead
    // of painting it instantly. Spacing matches arrival rate so a 200ms relay
    // batch does not flash as one frame — but the schedule is hard-capped so
    // a decoder burst cannot turn into a 2s backlog. Extra frames are late
    // already; showing them in order is how 30fps became a delayed movie.
    const LIVE_QUEUE_MS = 33;
    const LIVE_QUEUE_MAX = 2;
    function queueVideoFrame(p, frame) {
      const now = performance.now();
      const rate = (p.frameTimes || []).length || 30;
      let spacing = 1000 / rate;
      if (p.renderQ && p.renderQ.length > 2) spacing *= 0.8;
      spacing = Math.max(8, Math.min(33, spacing));
      if (!p.nextShowAt || p.nextShowAt < now) p.nextShowAt = now;
      (p.renderQ || (p.renderQ = [])).push({ f: frame, at: p.nextShowAt });
      p.nextShowAt += spacing;
      if (p.nextShowAt - now > LIVE_QUEUE_MS || p.renderQ.length > LIVE_QUEUE_MAX) {
        while (p.renderQ.length > 1) p.renderQ.shift().f.close();
        p.renderQ[0].at = now;
        p.nextShowAt = now + spacing;
      }
      pumpRenderQ(p);
    }

    // pumpRenderQ paints whatever is due on each animation frame. Anything that
    // came due within the same tick is superseded — only the newest is drawn,
    // since the display can show exactly one.
    function pumpRenderQ(p) {
      if (p.renderRAF) return;
      // Schedule on the window that OWNS the canvas. A popped-out pane draws
      // into the popup's document while this script keeps running in the
      // opener — and a browser throttles (or pauses) rAF in a backgrounded
      // tab, which the opener becomes the moment you focus the popup. Using
      // the opener's clock would freeze exactly the window you are looking at.
      p.renderRAF = requestAnimationFrame(() => {
        p.renderRAF = 0;
        const now = performance.now();
        let due = null;
        while (p.renderQ && p.renderQ.length && p.renderQ[0].at <= now) {
          if (due) due.close(); // superseded within this tick; only one can show
          due = p.renderQ.shift().f;
        }
        if (due) { paintVideoFrame(p, due); due.close(); }
        if (p.renderQ && p.renderQ.length) pumpRenderQ(p);
      });
    }

    // dropQueuedFrames abandons pending playout and shows the newest frame at
    // once. Called on input: while you are watching, smooth motion wins; the
    // moment you interact, latency does.
    function dropQueuedFrames(p) {
      if (!p.renderQ || !p.renderQ.length) return;
      while (p.renderQ.length > 1) p.renderQ.shift().f.close();
      p.renderQ[0].at = 0;
      p.nextShowAt = 0;
      pumpRenderQ(p);
    }

    // ensureH264Decoder lazily builds the pane's VideoDecoder. Output frames
    // paint onto the SAME canvas as JPEG frames — pop-out aspect hooks, cursor
    // overlay, and the ack-after-draw contract all behave identically.
    function ensureH264Decoder(p) {
      if (p.vdec && p.vdec.state !== 'closed') return true;
      if (typeof VideoDecoder === 'undefined') return false;
      p.h264Synced = false;
      p.h264Seqs = [];
      p.h264Hold = null;
      try {
        p.vdec = new VideoDecoder({
          output: (frame) => {
            const e = (p.h264Seqs && p.h264Seqs.length) ? p.h264Seqs.shift() : null;
            // Frames that arrived one-per-message are already spaced by the
            // network — paint on arrival, lowest latency. Frames unpacked from
            // a relay batch all land within a millisecond of each other, so
            // they must be played OUT over time or the display (one refresh per
            // 16ms) only ever shows the last of each group: honest 30fps by the
            // counter, visibly 5fps to a human.
            if (e && e.paced && !(p.liveUntil && performance.now() < p.liveUntil)) queueVideoFrame(p, frame);
            else { paintVideoFrame(p, frame); frame.close(); }
            if (e && e.ack != null) sendWindowAck(p.w.id, e.ack, true);
          },
          error: (e) => h264Fail(p, e),
        });
        p.vdec.configure({ codec: H264_CODEC, optimizeForLatency: true });
        return true;
      } catch (e) {
        h264Fail(p, e);
        return false;
      }
    }

    // h264Fail tears down the decode path and renegotiates as a JPEG viewer:
    // clear the capability, drop the peer, and re-hello — the agent sees a
    // peer without h264 and streams JPEGs. The pane keeps its last picture
    // until they arrive; nothing is lost but compression.
    // Ask the host for an immediate keyframe (rides the ack channel, which the
    // agent already routes to the right stream). Rate-limited: a burst of
    // gapped frames must not become a burst of IDR requests, each of which
    // costs ~4x a delta on the wire.
    function requestH264Key(p) {
      const now = performance.now();
      if (p.h264KeyAskAt && now - p.h264KeyAskAt < 250) return;
      p.h264KeyAskAt = now;
      // Out-of-band by preference: a resync request is the one message that
      // must NOT wait behind the undecodable backlog that prompted it.
      if (rtcCtlOpen()) { try { rtcCtl.send(JSON.stringify({ id: p.w.id, seq: p.lastSeq || 0, key: true, dc: true })); return; } catch (_) {} }
      if (rtcActive()) { try { rtcDc.send(JSON.stringify({ id: p.w.id, seq: p.lastSeq || 0, key: true })); return; } catch (_) {} }
      sendWin('window_ack', { id: p.w.id, seq: p.lastSeq || 0, key: true });
    }

    function h264Fail(p, e) {
      p.h264Errs = (p.h264Errs || 0) + 1;
      if (DEBUG) dbg('h264 decode failed: ' + e);
      try { if (p.vdec && p.vdec.state !== 'closed') p.vdec.close(); } catch (_) {}
      p.vdec = null;
      p.h264Synced = false;
      p.h264Seqs = [];
      p.h264Hold = null;
      if (h264Decode) {
        h264Decode = false;
        retryWebRTC('h264 decode error', 0);
      }
    }

    // Drag a pane by its title bar (pointer events cover mouse + touch).
    function paneDrag(pane, bar) {
      let s = null;
      bar.addEventListener('pointerdown', (e) => {
        if (e.target.closest('.wpane-btn')) return;
        e.preventDefault();
        focusPane(pane.w.id);
        const r = pane.el.getBoundingClientRect();
        s = { px: e.clientX, py: e.clientY, left: r.left, top: r.top };
        try { bar.setPointerCapture(e.pointerId); } catch (_) {}
      });
      bar.addEventListener('pointermove', (e) => {
        if (!s) return;
        pane.el.style.left = (s.left + e.clientX - s.px) + 'px';
        pane.el.style.top = Math.max(headerBottom(), s.top + e.clientY - s.py) + 'px';
      });
      const end = () => { if (s) { s = null; savePaneLayout(); } };
      bar.addEventListener('pointerup', end);
      bar.addEventListener('pointercancel', end);
    }

    // Match capture pixels to what the browser can actually display. Network
    // Information is only a hint (and absent in Safari), so transport and pane
    // size remain the primary signals. Requests are host-clamped and debounced.
    function paneQualityProfile(pane) {
      const conn = navigator.connection || navigator.mozConnection || navigator.webkitConnection;
      const effective = conn && conn.effectiveType || '';
      const save = !!(conn && conn.saveData);
      const downlink = conn && Number(conn.downlink) || 0;
      const rtt = conn && Number(conn.rtt) || 0;
      const fr = frameRect(pane.img.getBoundingClientRect(), pane.img.width, pane.img.height);
      const displayPx = Math.ceil(Math.max(1, fr.width) * Math.max(1, window.devicePixelRatio || 1));
      let maxWidth = Math.max(1100, Math.min(2880, displayPx));
      let quality = 68;
      // A confirmed direct WebRTC path can use a higher quality ceiling, but
      // pane size still chooses the resolution tier. A small pane should not
      // restart capture at 2880 merely because its route is peer-to-peer.
      // Live connection stats can move this tier later; Network Information
      // remains only a coarse fallback and Save-Data stays authoritative.
      if (lastFrameVia === 'webrtc' && !save) {
        quality = maxWidth >= 2400 ? 80 : maxWidth >= 1600 ? 76 : 72;
      }
      else if (save || effective === 'slow-2g' || effective === '2g' || (downlink > 0 && downlink < 1.5)) { maxWidth = 800; quality = 40; }
      else if (effective === '3g' || (downlink > 0 && downlink < 4) || rtt > 300) { maxWidth = Math.min(maxWidth, 1100); quality = 48; }
      // A relay route is not evidence of a slow network — NAT can prevent P2P
      // on an otherwise excellent link. This deployment is user-owned, so let
      // a good relay connection use the high-detail tier too.
      else if (lastFrameVia !== 'webrtc') { maxWidth = Math.min(maxWidth, 2560); quality = 74; }
      else if (maxWidth >= 2400) quality = 80;
      else if (maxWidth >= 1600) quality = 72;
      // Stable buckets prevent a one-pixel resize from restarting capture.
      maxWidth = maxWidth <= 800 ? 800 : maxWidth <= 1100 ? 1100 : maxWidth <= 1440 ? 1440 : maxWidth <= 1920 ? 1920 : maxWidth <= 2560 ? 2560 : 2880;
      return { max_width: maxWidth, quality };
    }
    function requestPaneQuality(pane, immediate) {
      if (!pane) return;
      const apply = () => {
        pane.qualityTimer = null;
        const q = paneQualityProfile(pane);
        const key = q.max_width + ':' + q.quality;
        if (pane.qualityKey === key) return;
        pane.qualityKey = key;
        if (pane.infoShown) pane.infoEl.innerHTML = paneInfoHtml(pane);
        sendWin('window_ctl', { action: 'quality', id: pane.w.id, viewer: viewerId, ...q });
      };
      if (immediate) {
        if (pane.qualityTimer) clearTimeout(pane.qualityTimer);
        apply();
      } else if (!pane.qualityTimer) {
        // Throttle (rather than debounce) resize negotiation. During a long
        // drag this applies the newest size every 150ms, while qualityKey means
        // only actual 800/1100/1440/1920/2560/2880 tier crossings reach the
        // Agent and restart its capture pipeline.
        pane.qualityTimer = setTimeout(apply, 150);
      }
    }
    function sendPaneStart(pane) {
      const q = paneQualityProfile(pane);
      pane.qualityKey = q.max_width + ':' + q.quality;
      sendWin('window_ctl', { action: 'start', id: pane.w.id, viewer: viewerId, ...q, bands: 1 });
    }
    const netInfo = navigator.connection || navigator.mozConnection || navigator.webkitConnection;
    if (netInfo && netInfo.addEventListener) netInfo.addEventListener('change', () => {
      for (const p of panes.values()) requestPaneQuality(p);
    });
    window.addEventListener('resize', () => {
      for (const p of panes.values()) requestPaneQuality(p);
    });

    // Resize from the bottom-right handle; width drives height via image aspect.
    function paneResize(pane, rz) {
      let s = null;
      rz.addEventListener('pointerdown', (e) => {
        e.preventDefault(); e.stopPropagation();
        focusPane(pane.w.id);
        s = { px: e.clientX, w: pane.el.getBoundingClientRect().width };
        try { rz.setPointerCapture(e.pointerId); } catch (_) {}
      });
      rz.addEventListener('pointermove', (e) => {
        if (!s) return;
        pane.el.style.width = Math.max(160, s.w + e.clientX - s.px) + 'px';
        if (pane.placeCursor) pane.placeCursor();
        requestPaneQuality(pane);
      });
      const end = () => { if (s) { s = null; savePaneLayout(); requestPaneQuality(pane, true); } };
      rz.addEventListener('pointerup', end);
      rz.addEventListener('pointercancel', end);
    }

    // Pointer input inside a pane image.
    //  • Mouse (desktop): native feel — an instant click (the agent counts
    //    rapid clicks so double/triple-click work), and mouse-down + move + up
    //    sends a real drag (text selection, sliders, dragging files).
    //  • Touch (mobile): one finger = a floating trackpad cursor (drag to move,
    //    tap = click at the cursor); TWO fingers = pinch-zoom + pan of the pane
    //    (the pane's image is CSS-transformed; clicks still map correctly since
    //    we read the transformed rect).
    function paneInput(pane, img) {
      const GAIN = 1.5, MAX_ZOOM = 6;
      const ptrs = new Map();   // active pointerId -> {x, y}
      let pinch = null;         // {d, mx, my, z, px, py} captured at pinch start
      let downAt = 0, moved = false, sx = 0, sy = 0, lastX = 0, lastY = 0, path = null;
      let longPress = null, dragMode = false, dragPath = null; // touch drag-and-drop
      let mouseBtn = 0;                                        // button on current mouse press (0 left, 2 right)
      let lastClickAt = 0, lastClickFx = 0, lastClickFy = 0, clickRun = 0; // double-click run tracking
      const clamp01 = v => Math.max(0, Math.min(1, v));
      const dist = (a, b) => Math.hypot(a.x - b.x, a.y - b.y);

      function fracAt(cx, cy) {
        const fr = frameRect(img.getBoundingClientRect(), img.width, img.height);
        return { fx: clamp01((cx - fr.left) / fr.width), fy: clamp01((cy - fr.top) / fr.height) };
      }
      function frac(e) { return fracAt(e.clientX, e.clientY); }
      // Live drag. A drag used to be buffered whole and sent once the pointer
      // lifted, then replayed host-side at scripted speed — so nothing moved
      // under your finger and the playback had no relation to how you moved.
      // Now the press, the movement and the release each go as they happen.
      // Points still batch on the same timer as scrolling, because the host
      // pays a process spawn per event and flooding it just rebuilds the
      // backlog that made scrolling overshoot.
      // Only when the host says it understands the phases: a host expecting
      // the batched form would press and release once per chunk.
      function dragPhasesOK() { return !!(lastHostInfo && lastHostInfo.drag_phases); }
      let dragAcc = [], dragTimer = null, dragLive = false;
      // The host spawns a process per point, so a batch has to stay within the
      // interval that produced it or the backlog just rebuilds — a 120Hz
      // pointer emits twice what a 60Hz one does for the same finger movement.
      // Thin to at most this many, always keeping the last: an app interpolates
      // between drag points anyway, so the path's shape survives while the
      // per-batch cost stays bounded by the sender's rate, not the device's.
      const DRAG_MAX_PER_BATCH = 3;
      function thinDragPoints(pts) {
        if (pts.length <= DRAG_MAX_PER_BATCH) return pts;
        const out = [], step = (pts.length - 1) / (DRAG_MAX_PER_BATCH - 1);
        for (let i = 0; i < DRAG_MAX_PER_BATCH - 1; i++) out.push(pts[Math.round(i * step)]);
        out.push(pts[pts.length - 1]);
        return out;
      }
      function flushDragMoves() {
        dragTimer = null;
        if (!dragLive || !dragAcc.length) return;
        sendWin('window_input', { id: pane.w.id, kind: 'drag', phase: 'move', path: thinDragPoints(dragAcc) });
        dragAcc = [];
      }
      function dragBegin(p) {
        dragLive = true; dragAcc = [];
        sendWin('window_input', { id: pane.w.id, kind: 'drag', phase: 'begin', x: p.fx, y: p.fy });
      }
      function dragMove(p) {
        if (!dragLive) return;
        dragAcc.push([p.fx, p.fy]);
        if (!dragTimer) dragTimer = setTimeout(flushDragMoves, 50);
      }
      function dragEnd(p) {
        if (!dragLive) return;
        if (dragTimer) { clearTimeout(dragTimer); dragTimer = null; }
        const tail = thinDragPoints(dragAcc); dragAcc = []; dragLive = false;
        // The release carries whatever movement hadn't been flushed, so the
        // gesture can't end at a stale point.
        sendWin('window_input', { id: pane.w.id, kind: 'drag', phase: 'end', x: p.fx, y: p.fy, path: tail });
      }

      // Batch rapid wheel / two-finger scroll deltas into one send per frame so
      // the host isn't flooded (each scroll is an osascript/xdotool call).
      let scrollAcc = { dx: 0, dy: 0 }, scrollAt = { fx: 0.5, fy: 0.5 }, scrollTimer = null;
      function flushScroll() {
        scrollTimer = null;
        if (!scrollAcc.dx && !scrollAcc.dy) return;
        sendWin('window_input', { id: pane.w.id, kind: 'scroll', x: scrollAt.fx, y: scrollAt.fy, dx: scrollAcc.dx, dy: scrollAcc.dy });
        scrollAcc = { dx: 0, dy: 0 };
      }
      function queueScroll(fx, fy, dx, dy) {
        scrollAt = { fx, fy };
        scrollAcc.dx += dx; scrollAcc.dy += dy;
        if (!scrollTimer) scrollTimer = setTimeout(flushScroll, 50);
      }
      function applyTransform() {
        img.style.transform = 'translate(' + pane.panX + 'px,' + pane.panY + 'px) scale(' + pane.zoom + ')';
        place();
      }
      function clampPan() {
        if (pane.zoom <= 1.001) { pane.zoom = 1; pane.panX = 0; pane.panY = 0; return; }
        const maxX = img.clientWidth * (pane.zoom - 1) / 2, maxY = img.clientHeight * (pane.zoom - 1) / 2;
        pane.panX = Math.max(-maxX, Math.min(maxX, pane.panX));
        pane.panY = Math.max(-maxY, Math.min(maxY, pane.panY));
      }
      function place() {
        const c = pane.cursor;
        if (!c) { pane.cursorEl.style.display = 'none'; return; }
        const fr = frameRect(img.getBoundingClientRect(), img.width, img.height);
        const br = pane.body.getBoundingClientRect();
        pane.cursorEl.style.display = 'block';
        pane.cursorEl.style.left = (fr.left - br.left + c.fx * fr.width) + 'px';
        pane.cursorEl.style.top = (fr.top - br.top + c.fy * fr.height) + 'px';
      }
      pane.placeCursor = place;
      function flash() { pane.cursorEl.classList.add('tapped'); setTimeout(() => pane.cursorEl.classList.remove('tapped'), 150); }
      // Count rapid same-spot left clicks (1→2→3, wrapping) so the host gets
      // native double/triple-clicks. We time the run HERE at the source, so it's
      // immune to network jitter — the host trusts the count we send.
      function nextCount(f) {
        const now = Date.now();
        if (now - lastClickAt < 450 && Math.hypot(f.fx - lastClickFx, f.fy - lastClickFy) < 0.04) clickRun = clickRun % 3 + 1;
        else clickRun = 1;
        lastClickAt = now; lastClickFx = f.fx; lastClickFy = f.fy;
        return clickRun;
      }
      function click(f, button) {
        const m = { id: pane.w.id, kind: 'click', x: f.fx, y: f.fy };
        if (button === 'right') {
          m.button = 'right';
          lastClickAt = 0; clickRun = 0;       // a right-click breaks any double-click run
        } else {
          const c = nextCount(f);
          if (c > 1) m.count = c;
        }
        sendWin('window_input', m);
      }
      // Ends a touch grab. Returns 'drag' if a real drag was sent, 'tap' if it
      // was grabbed but never moved (treat as a click), or false if not grabbed.
      function endDrag(send) {
        if (longPress) { clearTimeout(longPress); longPress = null; }
        if (!dragMode) return false;
        pane.cursorEl.classList.remove('grabbing');
        dragMode = false;
        const dp = dragPath; dragPath = null;
        if (dragLive) {
          // Already streaming — just release, wherever the finger ended.
          const last = (dp && dp.length) ? dp[dp.length - 1] : (pane.cursor || { fx: 0.5, fy: 0.5 });
          dragEnd(last);
          return (dp && dp.length >= 2) ? 'drag' : 'tap';
        }
        if (send && dp && dp.length >= 2) { sendWin('window_input', { id: pane.w.id, kind: 'drag', path: dp.map(p => [p.fx, p.fy]) }); return 'drag'; }
        return 'tap';
      }

      img.addEventListener('pointerdown', (e) => {
        e.preventDefault();
        focusPane(pane.w.id);
        ptrs.set(e.pointerId, { x: e.clientX, y: e.clientY });
        try { img.setPointerCapture(e.pointerId); } catch (_) {}
        if (ptrs.size === 2 && e.pointerType !== 'mouse') {
          const [a, b] = [...ptrs.values()];
          const cx = (a.x + b.x) / 2, cy = (a.y + b.y) / 2, d0 = dist(a, b);
          // Both pinch and parallel movement enter one deterministic viewport
          // mode. Midpoint movement pans while room remains; any movement the
          // pan bounds cannot consume chains into remote scrolling. At 1×
          // there is no pan room, so a parallel swipe scrolls immediately.
          pinch = { d: d0 || 1, mx: cx, my: cy, z: pane.zoom, startD: d0, lastCx: cx, lastCy: cy, mode: null, scaled: false, t: Date.now() };
          downAt = 0; path = null; endDrag(false);
          return;
        }
        downAt = Date.now(); moved = false; sx = lastX = e.clientX; sy = lastY = e.clientY;
        if (e.pointerType === 'mouse') { mouseBtn = e.button; path = e.button === 0 ? [frac(e)] : null; return; }
        if (!pane.cursor) pane.cursor = { fx: 0.5, fy: 0.5 };
        place();
        dragMode = false; dragPath = null;
        // Long-press (finger held still ~450ms) grabs at the cursor; then moving
        // the finger drags — touch drag-and-drop. A cursor ring turns red to show
        // the grab, with a haptic tick where supported.
        longPress = setTimeout(() => {
          longPress = null; dragMode = true; dragPath = [{ fx: pane.cursor.fx, fy: pane.cursor.fy }];
          // The grab IS the press — send it now rather than reconstructing it
          // from the path after the finger lifts.
          if (dragPhasesOK()) dragBegin(pane.cursor);
          pane.cursorEl.classList.add('grabbing');
          if (navigator.vibrate) navigator.vibrate(25);
        }, 450);
      });

      img.addEventListener('pointermove', (e) => {
        if (!ptrs.has(e.pointerId)) return;
        ptrs.set(e.pointerId, { x: e.clientX, y: e.clientY });
        if (pinch && ptrs.size >= 2) {
          const [a, b] = [...ptrs.values()];
          const d = dist(a, b), cx = (a.x + b.x) / 2, cy = (a.y + b.y) / 2;
          const fingerDX = cx - pinch.lastCx, fingerDY = cy - pinch.lastCy;
          const scaleMoved = Math.abs(d - pinch.startD) > 16;
          const midpointMoved = Math.hypot(cx - pinch.mx, cy - pinch.my) > 16;
          if (!pinch.mode) {
            if (scaleMoved || midpointMoved) pinch.mode = 'view';
          }
          if (scaleMoved) pinch.scaled = true;
          if (pinch.mode === 'view') {
            // Once a real distance change wins the threshold, keep the whole
            // gesture local: combined pinch+pan must not accidentally scroll.
            if (pinch.scaled) pane.zoom = Math.max(1, Math.min(MAX_ZOOM, pinch.z * (d / pinch.d)));
            const beforeX = pane.panX, beforeY = pane.panY;
            pane.panX += fingerDX;
            pane.panY += fingerDY;
            clampPan(); applyTransform();
            if (!pinch.scaled) {
              // Nested-scroll chaining: consume midpoint travel as local pan
              // first, then send only the remainder once an axis reaches its
              // edge. Incremental pan avoids a dead zone when reversing there.
              const residualX = fingerDX - (pane.panX - beforeX);
              const residualY = fingerDY - (pane.panY - beforeY);
              if (Math.abs(residualX) > 0.25 || Math.abs(residualY) > 0.25) {
                const rr = img.getBoundingClientRect();
                const scaleX = (pane.w.w || rr.width) / rr.width, scaleY = (pane.w.h || rr.height) / rr.height;
                const f = fracAt(cx, cy);
                queueScroll(f.fx, f.fy, -residualX * scaleX, -residualY * scaleY);
              }
            }
          }
          pinch.lastCx = cx; pinch.lastCy = cy;
          return;
        }
        if (!downAt) return;
        if (Math.abs(e.clientX - sx) > 6 || Math.abs(e.clientY - sy) > 6) moved = true;
        if (e.pointerType === 'mouse') {
          if (path && path.length < 300) path.push(frac(e));
          // A mouse has no long-press grab: the drag starts the moment the
          // button is down and the pointer has actually moved.
          if (mouseBtn === 0 && moved && dragPhasesOK()) {
            const f = frac(e);
            if (!dragLive) dragBegin(path && path.length ? path[0] : f);
            // Include the threshold-crossing movement itself. Previously the
            // first move only pressed the button; a quick release then had no
            // dragged event, so many apps treated it as a click, not a drag.
            dragMove(f);
          }
          return;
        }
        if (!pane.cursor) return;
        if (moved && longPress) { clearTimeout(longPress); longPress = null; } // moved first → cursor move, not a grab
        const r = img.getBoundingClientRect();
        pane.cursor.fx = clamp01(pane.cursor.fx + (e.clientX - lastX) * GAIN / r.width);
        pane.cursor.fy = clamp01(pane.cursor.fy + (e.clientY - lastY) * GAIN / r.height);
        lastX = e.clientX; lastY = e.clientY;
        place();
        if (dragMode && dragPath && dragPath.length < 400) dragPath.push({ fx: pane.cursor.fx, fy: pane.cursor.fy });
        if (dragMode && dragPhasesOK()) dragMove(pane.cursor);
      });

      function endPointer(e) {
        ptrs.delete(e.pointerId);
        try { img.releasePointerCapture(e.pointerId); } catch (_) {}
        if (pinch) {
          if (ptrs.size < 2) {
            // Two fingers down-and-up quickly, with no pinch/scroll movement, is a
            // two-finger tap → right-click at the cursor (distinct from long-press
            // drag and two-finger scroll/zoom).
            if (pinch.mode === null && Date.now() - pinch.t < 400) {
              click(pane.cursor || fracAt(pinch.mx, pinch.my), 'right'); flash();
            }
            pinch = null; clampPan(); applyTransform();
            // getBoundingClientRect now reflects the zoomed display size.
            // Re-evaluate capture pixels so zooming does not merely magnify
            // the old low-resolution source frame.
            requestPaneQuality(pane);
          }
          downAt = 0; return;
        }
        if (!downAt) return;
        const dur = Date.now() - downAt; downAt = 0;
        // A TAP never takes keyboard focus. Re-asserting focus here (which the
        // old rule did whenever #wkey already had it) is what made the soft
        // keyboard impossible to dismiss: every tap aimed at getting rid of it
        // put focus straight back and raised it again. Tapping a canvas does
        // not blur the input either, so typing continues to work after a tap —
        // the keyboard simply stops fighting you. The keyboard button owns it
        // on touch. A mouse has no soft keyboard to raise, so it still focuses
        // freely and you can click then type.
        if (e.pointerType === 'mouse') wkey.focus();
        if (e.pointerType === 'mouse') {
          if (mouseBtn === 2) click(frac(e), 'right');                  // right button → context click
          else if (dragLive) dragEnd(frac(e));
          else if (mouseBtn === 0 && moved && path && path.length > 1) sendWin('window_input', { id: pane.w.id, kind: 'drag', path: path.map(p => [p.fx, p.fy]) });
          else if (mouseBtn === 0 && dur <= 600) click(frac(e));
          path = null;
          return;
        }
        const g = endDrag(true);
        if (g === 'drag') return;               // a real drag-and-drop completed
        // A quick tap, or a long-press that never dragged, is a click.
        if (g === 'tap' || (!moved && dur <= 600)) {
          click(pane.cursor || { fx: 0.5, fy: 0.5 });
          flash();
          // Armed, a tap means "put me in this field": the click above already
          // went to the remote window, and the soft keyboard comes up, so
          // tapping a text box leaves you ready to type into it.
          //
          // Disarmed, a tap is only a click and puts the keyboard away, the way
          // tapping outside a text field does everywhere else. That has to be
          // explicit: pane taps preventDefault, which suppresses the browser's
          // own tap-elsewhere blur. Scrolls and drags are excluded above, so
          // this only fires on a deliberate tap.
          if (pane.kbdMode) wkey.focus();
          else if (document.activeElement === wkey) wkey.blur();
        }
      }
      img.addEventListener('pointerup', endPointer);
      img.addEventListener('pointercancel', endPointer);
      // Desktop: mouse wheel / trackpad scroll over the pane scrolls the window.
      img.addEventListener('wheel', (e) => {
        e.preventDefault(); e.stopPropagation();
        focusPane(pane.w.id);
        const f = frac(e);
        const m = e.deltaMode === 1 ? 16 : (e.deltaMode === 2 ? img.getBoundingClientRect().height : 1);
        queueScroll(f.fx, f.fy, e.deltaX * m, e.deltaY * m);
      }, { passive: false });
      // Suppress the browser's long-press image menu ("save/share image") and
      // any native image drag — we handle long-press ourselves.
      img.addEventListener('contextmenu', (e) => e.preventDefault());
      img.addEventListener('dragstart', (e) => e.preventDefault());
    }

    // Shared keyboard → focused pane. Physical keys via keydown; soft-keyboard
    // text via input; backspace via either.
    // iOS's soft keyboard implements backspace auto-repeat as repeated TEXT
    // DELETIONS, not repeated key events — an empty capture input gives it
    // nothing to delete, so press-and-hold used to produce exactly one
    // Backspace (taps only). Keep a sacrificial run of filler characters in
    // the input instead: held backspace eats filler at the OS repeat rate (one
    // remote Backspace per eaten character via the input-event diff below),
    // typed text appends past it, and the field is silently refilled with the
    // caret re-parked after every event.
    // Keyboard-mode state for the pane buttons' active styling. The input is
    // kept EMPTY: a sentinel-filler experiment for iOS backspace auto-repeat
    // (parked) put 64 x's in here — which Android's keyboard promptly turned
    // into an "xxxxxx…" suggestion strip. Held-backspace lives on the command
    // bar's ⌫ instead, whose JS-timed repeat works everywhere.
    wkey.addEventListener('focus', () => document.body.classList.add('wkbd-open'));
    wkey.addEventListener('blur', () => { document.body.classList.remove('wkbd-open'); wkey.value = ''; });
    // Respect the OS keyboard-dismiss gesture (Android back, iOS "done"): the
    // IME collapses WITHOUT blurring the input, so the page still considers the
    // user "typing" and the next pane tap re-summons the keyboard. Detect the
    // dismissal by the visual viewport growing back while our input holds
    // focus, and release the focus to match. Touch devices only — on desktop a
    // window resize mid-typing must not eat keyboard focus.
    if (window.visualViewport && matchMedia('(any-pointer: coarse)').matches) {
      let vvHeight = visualViewport.height;
      visualViewport.addEventListener('resize', () => {
        const grewBy = visualViewport.height - vvHeight;
        vvHeight = visualViewport.height;
        if (grewBy > 120 && document.activeElement === wkey) wkey.blur();
      });
    }
    // Bounds one typed message. Large enough that ordinary input and most
    // pastes go in a single call, small enough that the host never builds an
    // unwieldy script argument out of one.
    const WIN_TYPE_CHUNK = 256;
    wkey.addEventListener('keydown', (e) => {
      if (!focusedPaneId) return;
      const sp = WIN_SPECIAL_KEYS[e.key];
      // A shortcut (cmd/ctrl/alt held) is forwarded to the focused remote window
      // as a chord — cmd+C, ctrl+shift+T, cmd+left — and the browser's own copy /
      // paste / zoom is suppressed so the keystroke lands on the remote, not here.
      // (Shift alone is not a shortcut: Shift+a is just an uppercase character.)
      const shortcut = e.metaKey || e.ctrlKey || e.altKey;
      if (shortcut && (sp || e.key.length === 1)) {
        e.preventDefault();
        const mods = [];
        if (e.metaKey) mods.push('cmd');
        if (e.ctrlKey) mods.push('ctrl');
        if (e.altKey) mods.push('alt');
        if (e.shiftKey) mods.push('shift');
        sendWin('window_input', { id: focusedPaneId, kind: 'key', special: mods.join('+') + '+' + (sp || e.key.toLowerCase()) });
        return;
      }
      if (sp) { e.preventDefault(); sendWin('window_input', { id: focusedPaneId, kind: 'key', special: sp }); return; }
      if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        winType(focusedPaneId, e.key);
      }
    });
    wkey.addEventListener('input', (e) => {
      const text = wkey.value; wkey.value = '';
      if (!focusedPaneId) return;
      if (e.inputType === 'deleteContentBackward') { sendWin('window_input', { id: focusedPaneId, kind: 'key', special: 'delete' }); return; }
      // Send the text as text, not as a stream of single characters. The host
      // types whatever it is given in one call, and that call is an osascript
      // costing ~100ms whether it carries one character or a hundred — so a
      // per-character send turned a 200-character paste into 200 messages and
      // twenty seconds of host work, against an input queue that holds 64 and
      // DROPS what it cannot fit. The tail of a paste simply vanished.
      // Chunked only to keep any single script argument sane.
      winType(focusedPaneId, text);
    });

    // Fetch a fresh window list each time the dropdown is opened.
    winDetails.addEventListener('toggle', () => {
      if (winDetails.open) requestWindows();
    });
    // Fetch the installed-app list each time the Apps launcher is opened.
    appsDetails.addEventListener('toggle', () => {
      if (appsDetails.open) requestApps();
    });

    // If the QR code provided both session and PIN, just connect. If only a
    // session is given, connect PIN-free when this browser is a potential owner
    // (the owner handshake runs first and falls back to a PIN prompt if the
    // machine doesn't recognise it); otherwise wait for the PIN.
    if (initialSession && initialPin) {
      startConnect();
    } else if (initialSession) {
      loadDeviceKeyPair().then(dk => { if (dk) startConnect(); else pinInput.focus(); });
    }
    // Compute this browser's device fingerprint for the Host panel (no-op → null
    // if there's no device key yet).
    refreshOwnFingerprint();

    // If the page is closed/refreshed, close the WS cleanly.
    window.addEventListener('beforeunload', () => {
      manualClose = true;
      if (ws) try { ws.close(); } catch (_) {}
    });

    // ── Guided tour ─────────────────────────────────────────────────────────
    // A first-visit walk through every part of the UI: a spotlight that glides
    // between controls, a card that follows it, Back / Next / Skip, arrow keys
    // and swipes. Steps describe what they point at rather than where it is,
    // and each one is skipped when its target is not on screen, so the same
    // list serves a phone (key bar, ⚙ replay) and a desktop (Host panel
    // replay), connected or not.
    //
    // Menus are never opened for real. Someone taking the tour usually has
    // nothing in them yet, so each menu step drops an example panel, filled
    // with made-up machines and windows and labelled as an example, under the
    // control it describes.
    (function initTour() {
      const DONE_KEY = 'reminal-tour-v1';
      const $ = (id) => document.getElementById(id);
      const svg = (body, extra) => '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"' + (extra || '') + '>' + body + '</svg>';
      // One line weight, no fills, no colour: the same drawing language as
      // reminal.app and the header icons above.
      const I = {
        term: svg('<rect x="3" y="4.5" width="18" height="15" rx="2"/><path d="m7.5 9.5 3 2.5-3 2.5M12.5 14.5h4"/>'),
        pulse: svg('<path d="M3 12h4l2.5-6 5 12L17 12h4"/>'),
        mach: svg('<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/>'),
        bell: svg('<path d="M6 9a6 6 0 0 1 12 0c0 6.5 2.5 8 2.5 8h-17S6 15.5 6 9z"/><path d="M10.3 20.5a2 2 0 0 0 3.4 0"/>'),
        win: svg('<rect x="3" y="3" width="7.5" height="7.5" rx="1.5"/><rect x="13.5" y="3" width="7.5" height="7.5" rx="1.5"/><rect x="3" y="13.5" width="7.5" height="7.5" rx="1.5"/><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.5"/>'),
        app: svg('<path d="M4.5 16.5c-1.5 1.26-2 5-2 5s3.74-.5 5-2c.71-.84.7-2.13-.09-2.91a2.18 2.18 0 0 0-2.91-.09z"/><path d="m12 15-3-3a22 22 0 0 1 2-3.95A12.88 12.88 0 0 1 22 2c0 2.72-.78 7.5-6 11a22.35 22.35 0 0 1-4 2z"/><path d="M9 12H4s.55-3.03 2-4c1.62-1.08 5 0 5 0M12 15v5s3.03-.55 4-2c1.08-1.62 0-5 0-5"/>'),
        host: svg('<path d="M3 3v18h18"/><path d="m7 15 4-4 3 3 6-6"/>'),
        share: svg('<circle cx="18" cy="5" r="3"/><circle cx="6" cy="12" r="3"/><circle cx="18" cy="19" r="3"/><path d="m8.6 10.5 6.8-4M8.6 13.5l6.8 4"/>'),
        exit: svg('<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="m16 17 5-5-5-5M21 12H9"/>'),
        plug: svg('<path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4"/><path d="m10 17 5-5-5-5M15 12H3"/>'),
        key: svg('<rect x="2" y="5" width="20" height="14" rx="2"/><path d="M6 9h.01M10 9h.01M14 9h.01M18 9h.01M6 13h.01M18 13h.01M8 16h8"/>'),
        tools: svg('<path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"/>'),
        snip: svg('<circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M20 4 8.12 15.88M14.47 14.48 20 20M8.12 8.12 12 12"/>'),
        dock: svg('<path d="m7 15 5-5 5 5"/><path d="M4 20h16"/><path d="M12 10V3"/>'),
        arrowR: svg('<path d="M5 12h14M13 6l6 6-6 6"/>'),
        info: svg('<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>'),
        search: svg('<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>'),
        check: svg('<path d="m5 12.5 4.5 4.5L19 7.5"/>'),
        moon: svg('<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>'),
        spark: svg('<path d="M12 3v4M12 17v4M3 12h4M17 12h4M6 6l2.5 2.5M15.5 15.5 18 18M18 6l-2.5 2.5M8.5 15.5 6 18"/>'),
      };
      // Small inline glyphs for naming controls inside a sentence, drawn as
      // key caps instead of emoji so they match the rest of the UI.
      const G = {
        plus: svg('<path d="M12 5v14M5 12h14"/>'),
        pen: svg('<path d="M4 20h4L19 9l-4-4L4 16z"/>'),
        x: svg('<path d="M6 6l12 12M18 6 6 18"/>'),
        bksp: svg('<path d="M9 5h12v14H9l-6-7z"/><path d="m12 9 6 6M18 9l-6 6"/>'),
        gear: svg('<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>'),
        snip: svg('<circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M20 4 8.1 15.9M14.5 14.5 20 20M8.1 8.1 12 12"/>'),
        menu: svg('<path d="M4 7h16M4 12h16M4 17h16"/>'),
        bell: svg('<path d="M6 9a6 6 0 0 1 12 0c0 6.5 2.5 8 2.5 8h-17S6 15.5 6 9z"/><path d="M10.3 20.5a2 2 0 0 0 3.4 0"/>'),
        moon: svg('<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>'),
      };
      const k = (name) => '<span class="tour-k">' + G[name] + '</span>';

      const connected = () => document.body.classList.contains('connected');
      const touchUI = () => {
        const kb = $('keybar');
        return !!kb && getComputedStyle(kb).display !== 'none';
      };
      function shown(el) {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        if (r.width < 2 || r.height < 2) return false;
        const cs = getComputedStyle(el);
        return cs.visibility !== 'hidden' && cs.display !== 'none';
      }
      const closeMenus = () => ['recents', 'windows', 'apps', 'host'].forEach((m) => { const d = $(m); if (d && d.open) d.open = false; });
      const summary = (id) => { const d = $(id); return d && d.querySelector('summary'); };
      // The key bar scrolls sideways under the pinned ✂ and ≡, so a group
      // of keys is only the ones actually in view: not scrolled off the left,
      // not tucked under the pinned pair on the right.
      const keys = (from, to) => {
        const kb = $('keybar'), snip = $('snip-btn');
        const lo = kb.getBoundingClientRect().left, hi = snip ? snip.getBoundingClientRect().left : innerWidth;
        const out = [];
        let on = false;
        for (const b of kb.children) {
          if (b === from) on = true;
          if (on && shown(b)) {
            const r = b.getBoundingClientRect();
            if (r.left >= lo - 1 && r.right <= hi + 1) out.push(b);
          }
          if (b === to) break;
        }
        return out;
      };
      const scrollKeybar = (end) => { const kb = $('keybar'); if (kb) kb.scrollLeft = end ? kb.scrollWidth : 0; };
      const mod = /Mac|iPhone|iPad/.test(navigator.platform || '') ? '⌘' : 'Ctrl';

      // ── example panels ──
      const tile = (c, glyph) => '<span class="tm-app" style="--c:' + c + '">' + glyph + '</span>';
      const A = {
        code: svg('<path d="m8 8-4 4 4 4M16 8l4 4-4 4M13.5 5l-3 14"/>'),
        web: svg('<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.5 3.5 5.5 3.5 9s-1 6.5-3.5 9c-2.5-2.5-3.5-5.5-3.5-9s1-6.5 3.5-9z"/>'),
        pen: svg('<path d="M12 19l7-7-3-3-7 7v3z"/><path d="M18 13l-1.5-7.5L2 2l3.5 14.5L13 18z"/>'),
        term: svg('<path d="m5 8 4 4-4 4M12 16h7"/>'),
        note: svg('<path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z"/><path d="M14 3v5h5"/><path d="M9 11h6M9 15h6"/>'),
        music: svg('<path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/>'),
        mail: svg('<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/>'),
        cal: svg('<rect x="3" y="5" width="18" height="16" rx="2"/><path d="M3 10h18M8 3v4M16 3v4"/>'),
        files: svg('<path d="M20 19a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h5l2 2h7a1 1 0 0 1 1 1z"/>'),
        photo: svg('<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="2"/><path d="m21 17-5-5-9 8"/>'),
      };
      const tag = '<div class="tm-tag">Example</div>';
      const acts = '<span class="tm-acts">' + G.plus + G.pen + G.x + '</span>';
      // A session row as Machines draws it: its state, and on one awake the
      // moon that puts it to sleep (its place kept on one asleep, so the rows line up).
      const sess = (name, meta, state, cur) => '<div class="tm-sess' + (cur ? ' cur' : '') + '"><div><div class="tm-name">' + name + '</div><div class="tm-meta">' + meta + '</div></div>' +
        (state ? '<span class="tm-state ' + state[0] + '">' + state[1] + '</span>' : '') +
        (state && state[0] === 'sleep' ? '<span class="tm-sleep off">' + G.moon + '</span>' : '<span class="tm-sleep" data-mock="moon">' + G.moon + '</span>') + '</div>';
      const MOCK = {
        machines: () => tag +
          '<div class="tm-bar"><div class="tm-search">' + I.search + 'Search machines &amp; sessions…</div><div class="tm-bell" data-mock="bell">' + G.bell + '</div></div>' +
          '<div class="tm-mach"><span class="tm-dot on"></span><b>MacBook Pro</b><span class="tm-sub">18% · 3</span>' + acts + '</div>' +
          sess('claude', '~/projects/webapp', ['work', 'Working'], true) +
          sess('dev-server', 'npm run dev · idle 2m') +
          sess('codex', '~/projects/api', ['ask', 'Needs you']) +
          sess('docs', '~/projects/docs · asleep 3h', ['sleep', 'Asleep']) +
          '<div class="tm-mach"><span class="tm-dot on"></span><b>home-server</b><span class="tm-sub">4% · 2</span>' + acts + '</div>' +
          sess('backups', 'restic · idle 1h', ['done', 'Done']) +
          '<div class="tm-mach off"><span class="tm-dot"></span><b>Work laptop</b><span class="tm-sub">offline · 2h ago</span></div>',
        windows: () => tag +
          '<div class="tm-bar"><div class="tm-search">' + I.search + 'Search windows…</div></div>' +
          [['#3b82f6', A.code, 'Code', 'main.go · webapp'], ['#f59e0b', A.web, 'Browser', 'localhost:3000'],
           ['#a855f7', A.pen, 'Design', 'Onboarding flow'], ['#22c55e', A.term, 'Terminal', '~/projects/webapp'],
           ['#ec4899', A.photo, 'Preview', 'screenshot.png']]
            .map((w, i) => '<div class="tm-win' + (i ? '' : ' cur') + '">' + tile(w[0], w[1]) + '<div><div class="tm-name">' + w[2] + '</div><div class="tm-meta">' + w[3] + '</div></div></div>').join(''),
        apps: () => tag +
          '<div class="tm-grid">' +
          [['#f59e0b', A.web, 'Browser'], ['#3b82f6', A.code, 'Code'], ['#22c55e', A.term, 'Terminal'], ['#eab308', A.note, 'Notes'],
           ['#ec4899', A.music, 'Music'], ['#0ea5e9', A.mail, 'Mail'], ['#ef4444', A.cal, 'Calendar'], ['#6366f1', A.files, 'Files']]
            .map((a) => '<div class="tm-cell">' + tile(a[0], a[1]) + '<span>' + a[2] + '</span></div>').join('') + '</div>',
        host: () => tag +
          '<div class="tm-hosthead"><b>MacBook Pro</b><div class="tm-meta">macOS · arm64 · 10 cores</div></div>' +
          '<div class="tm-meter"><div><span>Memory</span><b>9.6 GB / 16 GB</b></div><i style="--v:60%"></i></div>' +
          '<div class="tm-meter"><div><span>CPU</span><b>23%</b></div><i style="--v:23%"></i></div>' +
          '<div class="tm-meter"><div><span>Battery</span><b>82% · 4h 10m left</b></div><i style="--v:82%"></i></div>' +
          '<div class="tm-btn">' + G.plus + 'New session on this host</div><div class="tm-btn">View full desktop</div>' +
          '<div class="tm-own">' + I.check + 'You own this machine</div>',
        // A meme card, drawn as a real one is (moments.js), from an example line.
        meme: () => {
          let src = '';
          try { src = window.reminalMoments.sampleMeme().toDataURL('image/png'); } catch (_) {}
          return tag + (src ? '<img class="tm-meme" alt="" src="' + src + '">' : '<div class="tm-meme-text">You’re absolutely right! I apologize for the confusion.</div>');
        },
      };
      const MOCK_W = { machines: 340, windows: 320, apps: 312, host: 290, meme: 260 };
      const NOTE = {
        machines: 'Placeholder data. Yours will list your own machines and sessions.',
        windows: 'Placeholder data. Yours will list the windows open on your computer.',
        apps: 'Placeholder data. Yours will list the apps on your computer.',
        host: 'Placeholder numbers. Yours are live from your computer.',
        meme: 'An example. Yours is made from what your agent says.',
      };
      const exampleNote = (name) => '<div class="tour-note"><span class="tour-chip">Example</span><span>' + NOTE[name] + '</span></div>';

      const STEPS = [
        { id: 'welcome', hero: true },
        {
          id: 'connect', icon: I.plug, when: () => !connected(),
          target: () => [$('session-input'), $('pin-input'), $('connect-btn')],
          title: 'Join a session',
          body: () => 'Run <code>reminal</code> on your computer. It prints a <b>session code</b> and a <b>PIN</b>: type them here, or just scan the QR code it shows and skip the typing.',
        },
        {
          id: 'terminal', icon: I.term, when: connected, target: () => $('terminal-wrap'), place: 'inside',
          title: 'This is your computer’s terminal',
          body: () => 'Not a copy, the real shell, live. Whatever you type runs there, and everyone watching sees the same screen. It is end-to-end encrypted, so the relay in between only ever sees noise.',
          tips: () => touchUI()
            ? ['Swipe to scroll back through history', 'Long-press to select and copy text', 'Tap a link to open it']
            : ['Drop a file here to upload it to the computer', '<kbd>' + mod + '</kbd> <kbd>F</kbd> searches the scrollback', 'Paste a screenshot and it lands on the computer'],
        },
        {
          id: 'status', icon: I.pulse, target: () => $('status'),
          title: () => connected() ? 'Live, and it stays that way' : 'Connection status',
          body: () => connected()
            ? '<b class="ok">Green</b> means live. If your Wi-Fi drops or your laptop sleeps, it turns <b class="warn">amber</b> and reconnects on its own, right where you left off.'
            : 'This pill tells you what is happening: what reminal is waiting for, when it is connecting, and green once you are live.',
        },
        {
          id: 'machines', icon: I.mach, anchor: () => summary('recents'), mock: 'machines',
          title: 'All your machines, one list',
          body: () => 'Every computer you own and every terminal running on it, with what each one is doing. Pick one to jump straight in. Your own machines never ask for a PIN.',
          tips: () => [k('plus') + ' starts a new terminal on that machine', k('pen') + ' renames it, ' + k('x') + ' ends a session', 'Search finds a session by name, folder or what is on its screen'],
        },
        {
          id: 'alerts', icon: I.bell, anchor: () => summary('recents'), mock: 'machines', focus: '[data-mock="bell"]',
          title: 'Alerts',
          body: () => 'Get a notification when a machine’s CPU runs hot, its battery gets low, or the charger is unplugged, even with this page closed.',
        },
        {
          id: 'sleep', icon: I.moon, anchor: () => summary('recents'), mock: 'machines', focus: '[data-mock="moon"]',
          title: 'Put a session to sleep',
          body: () => 'On your own machines, ' + k('moon') + ' on a session puts it to sleep: its programs stop and their memory is freed. Open it again and it wakes as itself, with the same link, PIN and conversation.',
          tips: () => ['If something would be lost, like a job still running, it asks first', 'From a terminal: reminal sleep, and reminal wake'],
        },
        {
          id: 'windows', icon: I.win, when: connected, anchor: () => summary('windows'), mock: 'windows',
          title: 'Any window, right here',
          body: () => 'Every app window on the computer. Pick one and it opens as a live pane you can click, type into, drag and resize, like it was running here.',
        },
        {
          id: 'apps', icon: I.app, when: connected, anchor: () => summary('apps'), mock: 'apps',
          title: 'Launch apps',
          body: () => 'Start any app on the computer from here. Its window shows up ready to use.',
        },
        {
          id: 'host', icon: I.host, when: connected, anchor: () => summary('host'), mock: 'host',
          title: 'The computer at a glance',
          body: () => 'Live CPU, memory and battery, a button for another terminal, and one for the whole desktop. Updates and <b>What’s new</b> live here too.',
        },
        {
          id: 'share', icon: I.share, when: connected, target: () => $('share-btn'),
          title: 'Bring someone in',
          body: () => 'A link and a QR code for this exact terminal. <b>Copy for an AI agent</b> gives an agent everything it needs to join and work alongside you.',
        },
        {
          id: 'moments', icon: I.spark, when: connected, anchor: () => $('share-btn'), mock: 'meme',
          title: 'Moments worth sharing',
          body: () => 'Share also turns this session into cards: a <b>receipt</b> of what the agent did, a sped-up <b>timelapse</b>, a <b>meme</b> of its best line, a phone shot, and your week as stories.',
          tips: () => ['When the agent says something meme-worthy, a ✦ note offers to make the meme', (touchUI() ? 'Select text and tap Meme' : 'Select text, then Share, then Meme') + ' to make one from any line', 'Made on this device, with secrets, user names and this machine’s name hidden. Look before you post.'],
        },
        {
          id: 'leave', icon: I.exit, when: connected, target: () => $('connect-btn'),
          title: 'Leave any time',
          body: () => 'Disconnecting only closes this view. The terminal keeps running on your computer, so you can come back to it from any device.',
        },
        {
          id: 'keys', icon: I.key, when: touchUI,
          target: () => keys($('keybar').querySelector('[data-key="up"]'), $('keybar').querySelector('[data-key="tilde"]')),
          enter: () => scrollKeybar(false),
          title: 'Keys your phone forgot',
          body: () => 'Arrows, <b>Tab</b>, <b>Esc</b> and the Ctrl shortcuts a shell needs. Hold an arrow or ' + k('bksp') + ' and it repeats. Swipe the bar for more.',
        },
        {
          id: 'tools', icon: I.tools, when: touchUI,
          target: () => keys($('kb-focus-btn'), $('upload-btn')),
          enter: () => scrollKeybar(true),
          title: 'Handy tools',
          body: () => 'Bring up the keyboard, make text bigger or smaller, copy and paste, search the scrollback, and <b>upload</b> a photo or file straight to the computer.',
        },
        {
          id: 'snips', icon: I.snip, when: touchUI, target: () => $('snip-btn'),
          title: 'Snippets',
          body: () => 'Save the commands you type all the time. <b>Drag up</b> from ' + k('snip') + ' and let go on one to paste it. Tap it to manage them.',
        },
        {
          id: 'dock', icon: I.dock, when: touchUI, target: () => $('keybar-toggle'),
          title: 'Switch sessions in one move',
          body: () => '<b>Tap</b> ' + k('menu') + ' to hide the key bar. <b>Drag up</b> and slide onto a session to switch to it, across all your machines.',
        },
        // The finale lands the spotlight on where the tour lives from now on.
        { id: 'done', hero: true, done: true, spot: () => touchUI() ? $('settings-btn') : summary('host'), enter: () => scrollKeybar(true) },
      ];

      let active = false, list = [], idx = 0, seen = new Set();
      // The terminal had the keyboard when the tour opened: it keeps it. The
      // tour never takes the keys of someone typing — what they type goes on
      // to the session, and the card answers its own keys only once it is
      // clicked or tabbed into.
      let termKeeps = false;
      const isTermKeys = (el) => !!(el && el.classList && el.classList.contains('xterm-helper-textarea'));
      let blockEl, ringEl, cardEl, mockEl = null, mockName = '', tick = null, lastKey = '', swipeX = null;

      // The welcome and the finale borrow reminal.app's hero: a faint grid,
      // a mono eyebrow, a headline that is mostly grey with the point in
      // white, an always-dark terminal figure and a row of figures.
      function heroHTML(done) {
        if (done) {
          return '<div class="tour-grid"></div>' +
            '<svg class="tour-done-check" viewBox="0 0 48 48" fill="none" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">' +
            '<circle cx="24" cy="24" r="22" stroke="#2e2e2e"/><circle class="arc" cx="24" cy="24" r="22" stroke="#ededed"/><path d="m15.5 24.5 6 6 11-12" stroke="#ededed"/></svg>' +
            '<div class="tour-eyebrow">Tour complete</div>' +
            '<div class="tour-title">That’s the whole app. <span class="em">Your computer is a tap away.</span></div>' +
            '<div class="tour-body">Replay this tour any time from ' +
            (touchUI() ? k('gear') + ' on the key bar.' : 'the <b>Host</b> menu, next to What’s new.') + '</div>';
        }
        return '<div class="tour-grid"></div>' +
          '<div class="tour-eyebrow">Welcome to reminal</div>' +
          '<div class="tour-title">Your computer’s terminal, windows and apps, <span class="em">live on this screen.</span></div>' +
          '<div class="tour-body">A one-minute look at where everything is. Nothing here changes your computer.</div>' +
          '<div class="tour-fig" aria-hidden="true"><div class="tour-figbar"><i></i><i></i><i></i><span>~/projects/webapp</span></div>' +
          '<div class="tour-figbody"><div><span class="p">$</span> <span class="typed">npm run dev</span><span class="cur"></span></div>' +
          '<div class="out o1">  ready on <span class="ok">http://localhost:3000</span></div>' +
          '<div class="out o2">  compiled in 412 ms</div></div></div>' +
          '<div class="tour-stats"><div><b>E2E</b><em>encrypted</em></div><div><b>0</b><em>open ports</em></div><div><b>60<span>fps</span></b><em>live windows</em></div></div>';
      }

      function build() {
        blockEl = document.createElement('div');
        blockEl.className = 'tour-block';
        ringEl = document.createElement('div');
        ringEl.className = 'tour-ring empty intro';
        cardEl = document.createElement('div');
        cardEl.className = 'tour-card';
        cardEl.setAttribute('role', 'dialog');
        cardEl.setAttribute('aria-modal', 'true');
        cardEl.setAttribute('aria-live', 'polite');
        cardEl.addEventListener('touchstart', (e) => { swipeX = e.touches[0].clientX; }, { passive: true });
        cardEl.addEventListener('touchend', (e) => {
          if (swipeX === null) return;
          const dx = e.changedTouches[0].clientX - swipeX;
          swipeX = null;
          if (dx < -50) next(); else if (dx > 50) back();
        }, { passive: true });
        document.body.append(blockEl, ringEl, cardEl);
      }

      function setMock(name) {
        if (name === mockName) return;
        if (mockEl) {
          const old = mockEl;
          old.style.opacity = '0';
          setTimeout(() => old.remove(), 200);
          mockEl = null;
        }
        mockName = name || '';
        if (!name) return;
        mockEl = document.createElement('div');
        mockEl.className = 'tour-mock tm-' + name;
        mockEl.setAttribute('aria-hidden', 'true');
        mockEl.innerHTML = MOCK[name]();
        document.body.appendChild(mockEl);
        if (!matchMedia('(prefers-reduced-motion: reduce)').matches) {
          mockEl.animate([{ opacity: 0, transform: 'translateY(-6px)' }, { opacity: 1, transform: 'none' }], { duration: 260, easing: 'cubic-bezier(.2,.85,.25,1)' });
        }
      }

      // Drop the example panel under its header control, where the real menu
      // would open.
      function placeMock(step) {
        if (!mockEl || !step.anchor) return;
        const a = step.anchor();
        if (!a) return;
        const ar = a.getBoundingClientRect();
        const vw = innerWidth;
        const w = Math.min(MOCK_W[mockName] || 320, vw - 16);
        const left = Math.min(vw - w - 8, Math.max(8, ar.left + ar.width / 2 - w / 2));
        mockEl.style.width = w + 'px';
        mockEl.style.left = Math.round(left) + 'px';
        mockEl.style.top = Math.round(ar.bottom + 10) + 'px';
      }

      function nudge() {
        if (!cardEl || matchMedia('(prefers-reduced-motion: reduce)').matches) return;
        cardEl.animate([{ transform: 'none' }, { transform: 'scale(1.03)' }, { transform: 'none' }], { duration: 260, easing: 'ease-out' });
      }

      function targetsOf(step) {
        let t;
        if (step.mock) {
          if (step.focus) t = mockEl ? [...mockEl.querySelectorAll(step.focus)] : [];
          else t = [step.anchor(), mockEl];
        } else if (step.target) t = step.target();
        else return [];
        if (!Array.isArray(t)) t = [t];
        return t.filter(shown);
      }

      function unionRect(els) {
        let l = Infinity, t = Infinity, r = -Infinity, b = -Infinity;
        for (const el of els) {
          const x = el.getBoundingClientRect();
          l = Math.min(l, x.left); t = Math.min(t, x.top); r = Math.max(r, x.right); b = Math.max(b, x.bottom);
        }
        return { left: l, top: t, right: r, bottom: b, width: r - l, height: b - t };
      }

      function render(dir) {
        const step = list[idx];
        seen.add(idx);
        const n = list.length;
        const mid = list.filter((s) => !s.hero).length;
        const pos = list.slice(0, idx + 1).filter((s) => !s.hero).length;
        const pct = step.hero ? (step.done ? 100 : 0) : Math.round(pos / mid * 100);
        const dots = list.map((s, i) => s.hero ? '' : '<button type="button" data-t="go" data-i="' + i + '" class="' + (i === idx ? 'on' : seen.has(i) ? 'seen' : '') + '" aria-label="Step ' + (i + 1) + '"></button>').join('');
        let inner;
        if (step.hero) {
          inner = heroHTML(step.done) +
            '<div class="tour-foot">' + (step.done
              ? '<button type="button" class="tour-btn2" data-t="back">Back</button><button type="button" class="tour-btn1" data-t="next">Start using reminal</button>'
              : '<button type="button" class="tour-btn2" data-t="skip">Skip</button><button type="button" class="tour-btn1" data-t="next">Show me around' + I.arrowR + '</button>') +
            '</div>' + (step.done ? '' : '<div class="tour-hint keys">Use <kbd>←</kbd> <kbd>→</kbd> to move, <kbd>Esc</kbd> to skip</div>');
        } else {
          const title = typeof step.title === 'function' ? step.title() : step.title;
          const tips = step.tips ? step.tips() : null;
          inner = '<div class="tour-top"><div class="tour-ico">' + (step.icon || '') + '</div>' +
            '<div class="tour-count">' + String(pos).padStart(2, '0') + '<span> / ' + String(mid).padStart(2, '0') + '</span></div>' +
            '<button type="button" class="tour-x" data-t="skip">Skip tour</button></div>' +
            '<div class="tour-title">' + title + '</div>' +
            '<div class="tour-body">' + step.body() + '</div>' +
            (tips ? '<ul class="tour-tips">' + tips.map((t) => '<li>' + t + '</li>').join('') + '</ul>' : '') +
            (step.mock ? exampleNote(step.mock) : '') +
            '<div class="tour-foot"><div class="tour-dots">' + dots + '</div>' +
            '<button type="button" class="tour-btn2" data-t="back">Back</button>' +
            '<button type="button" class="tour-btn1" data-t="next">' + (idx === n - 2 ? 'Finish' : 'Next') + '</button></div>';
        }
        cardEl.className = 'tour-card' + (step.hero ? ' hero' : '');
        cardEl.innerHTML = '<div class="tour-bar"><i></i></div><div class="tour-arrow none"></div><div class="tour-in">' + inner + '</div>';
        // Let the bar animate from where it was rather than snap.
        const bar = cardEl.querySelector('.tour-bar i');
        bar.style.width = (cardEl._pct || 0) + '%';
        requestAnimationFrame(() => { bar.style.width = pct + '%'; cardEl._pct = pct; });
        if (!matchMedia('(prefers-reduced-motion: reduce)').matches) {
          cardEl.querySelector('.tour-in').animate(
            [{ opacity: 0, transform: 'translateX(' + (dir * 18) + 'px)' }, { opacity: 1, transform: 'none' }],
            { duration: 320, easing: 'cubic-bezier(.2,.85,.25,1)' });
        }
        const primary = cardEl.querySelector('.tour-btn1');
        // Focused for Enter and screen readers, without painting a focus
        // ring nobody asked for (focusVisible is ignored where unsupported) —
        // unless the terminal has the keyboard (termKeeps): then it stays
        // there, and the card waits to be clicked.
        if (primary && !(termKeeps && isTermKeys(document.activeElement))) primary.focus({ preventScroll: true, focusVisible: false });
        lastKey = '';
        layout();
      }

      function layout() {
        if (!active) return;
        const step = list[idx];
        const vw = innerWidth, vh = innerHeight;
        placeMock(step);
        const els = step.hero ? (step.spot ? [step.spot()].filter(shown) : []) : targetsOf(step);
        const narrow = vw <= 560;
        let r = null;
        if (els.length) {
          r = unionRect(els);
          const pad = step.place === 'inside' ? -6 : 6;
          const l = Math.max(2, r.left - pad), t = Math.max(2, r.top - pad);
          const rr = Math.min(vw - 2, r.right + pad), bb = Math.min(vh - 2, r.bottom + pad);
          r = { left: l, top: t, right: rr, bottom: bb, width: rr - l, height: bb - t };
          if (r.width < 4 || r.height < 4) r = null;
        }
        // A spotlit control inside an example panel still wants the card
        // clear of the whole panel, not just of the control.
        let avoid = r;
        if (r && step.focus && mockEl) {
          const m = unionRect([step.anchor(), mockEl]);
          avoid = { left: Math.min(r.left, m.left), top: Math.min(r.top, m.top), right: Math.max(r.right, m.right), bottom: Math.max(r.bottom, m.bottom) };
          avoid.width = avoid.right - avoid.left; avoid.height = avoid.bottom - avoid.top;
        }
        const key = r ? [r.left, r.top, r.width, r.height].map(Math.round).join(',') + ',' + vw + ',' + vh : 'c' + vw + ',' + vh;
        if (key === lastKey) return;
        lastKey = key;

        if (r) {
          ringEl.classList.remove('empty');
          Object.assign(ringEl.style, { left: r.left + 'px', top: r.top + 'px', width: r.width + 'px', height: r.height + 'px' });
        } else {
          // No target: the hole shrinks to a point in the middle and the
          // whole page dims, which is the hero's backdrop.
          ringEl.classList.add('empty');
          Object.assign(ringEl.style, { left: vw / 2 + 'px', top: vh / 2 + 'px', width: '0px', height: '0px' });
        }

        const cw = cardEl.offsetWidth, ch = cardEl.offsetHeight;
        const arrow = cardEl.querySelector('.tour-arrow');
        let x, y, side = 'none';
        if (!r || step.hero) {
          x = (vw - cw) / 2; y = Math.max(12, (vh - ch) / 2);
        } else if (step.place === 'inside') {
          x = narrow ? 12 : Math.min(vw - cw - 16, Math.max(16, r.left + r.width - cw - 24));
          y = narrow ? vh - ch - 16 - (touchUI() ? ($('keybar').offsetHeight || 0) : 0) : Math.max(r.top + 24, (vh - ch) / 2);
        } else {
          const gap = 16;
          const below = avoid.bottom + gap, above = avoid.top - gap - ch;
          if (below + ch <= vh - 8) { y = below; side = 'up'; }
          else if (above >= 8) { y = above; side = 'down'; }
          else if (!narrow && avoid.right + gap + cw <= vw - 8) {
            // No room above or below: sit beside it.
            y = Math.min(vh - ch - 8, Math.max(8, avoid.top)); x = avoid.right + gap;
          } else { y = Math.max(8, vh - ch - 8); }
          const cx = r.left + r.width / 2;
          if (x === undefined) x = narrow ? 12 : Math.min(vw - cw - 12, Math.max(12, cx - cw / 2));
          if (side !== 'none' && avoid === r) {
            arrow.className = 'tour-arrow ' + side;
            arrow.style.left = Math.min(cw - 26, Math.max(12, cx - x - 7)) + 'px';
          } else side = 'none';
        }
        if (side === 'none') arrow.className = 'tour-arrow none';
        cardEl.style.left = Math.round(x) + 'px';
        cardEl.style.top = Math.round(y) + 'px';
      }

      function go(i, dir) {
        if (i < 0 || i >= list.length) return;
        idx = i;
        const step = list[idx];
        setMock(step.mock);
        if (step.enter) step.enter();
        render(dir);
        if (step.done) confetti();
      }
      function next() {
        if (idx >= list.length - 1) { finish(true); return; }
        go(idx + 1, 1);
      }
      function back() { if (idx > 0) go(idx - 1, -1); }

      // Clicks are taken at the window's capture phase, before the app's own
      // capture-phase "click outside closes the menu" handler and anything
      // under the blocker can see them.
      function onClick(e) {
        if (!active) return;
        const inCard = cardEl.contains(e.target);
        if (!inCard && e.target !== blockEl) return;
        e.stopPropagation();
        if (!inCard) { e.preventDefault(); nudge(); return; }
        const b = e.target.closest('[data-t]');
        if (!b) return;
        const t = b.getAttribute('data-t');
        if (t === 'next') next();
        else if (t === 'back') back();
        else if (t === 'skip') finish(false);
        else if (t === 'go') { const i = +b.getAttribute('data-i'); go(i, i > idx ? 1 : -1); }
      }

      function onKey(e) {
        if (!active) return;
        // A key for the terminal (it has the keyboard): the terminal's, Enter
        // and the arrows and Escape too — never taken for the tour.
        if (termKeeps && isTermKeys(e.target)) return;
        const k = e.key;
        const onBtn = e.target && e.target.closest && e.target.closest('.tour-card button');
        if ((k === 'Enter' || k === ' ') && onBtn) { e.stopPropagation(); return; } // the focused button's own click
        if (k === 'ArrowRight' || k === 'Enter' || k === ' ') next();
        else if (k === 'ArrowLeft') back();
        else if (k === 'Escape') finish(false);
        else if (k === 'Tab') { /* keep focus inside the card */ }
        else return;
        e.preventDefault();
        e.stopPropagation();
        if (k === 'Tab') {
          const bs = Array.from(cardEl.querySelectorAll('button'));
          const at = bs.indexOf(document.activeElement);
          const nx = bs[(at + (e.shiftKey ? -1 : 1) + bs.length) % bs.length];
          if (nx) nx.focus();
        }
      }

      function start() {
        if (active) return;
        closeMenus();
        // Drop steps whose controls are not on this screen (a hidden key bar,
        // a desktop without a Tour button) before the counter is computed.
        list = STEPS.filter((s) => {
          if (s.when && !s.when()) return false;
          if (s.hero) return true;
          if (s.mock) return shown(s.anchor());
          if (s.enter) s.enter();
          return targetsOf(s).length > 0;
        });
        scrollKeybar(false);
        active = true;
        idx = 0;
        seen = new Set();
        termKeeps = isTermKeys(document.activeElement);
        if (!termKeeps && document.activeElement && document.activeElement.blur) document.activeElement.blur();
        build();
        // Fade the dim in instead of slamming it down.
        requestAnimationFrame(() => requestAnimationFrame(() => ringEl.classList.remove('intro')));
        window.addEventListener('keydown', onKey, true);
        window.addEventListener('click', onClick, true);
        window.addEventListener('resize', layout);
        if (window.visualViewport) visualViewport.addEventListener('resize', layout);
        tick = setInterval(layout, 200);
        go(0, 1);
      }

      function finish(completed) {
        if (!active) return;
        active = false;
        try { localStorage.setItem(DONE_KEY, completed ? 'done' : 'skipped'); } catch (_) {}
        setMock(null);
        scrollKeybar(false);
        clearInterval(tick);
        window.removeEventListener('keydown', onKey, true);
        window.removeEventListener('click', onClick, true);
        window.removeEventListener('resize', layout);
        if (window.visualViewport) visualViewport.removeEventListener('resize', layout);
        const els = [blockEl, ringEl, cardEl];
        ringEl.classList.add('intro', 'empty');
        cardEl.style.opacity = '0';
        setTimeout(() => els.forEach((el) => el.remove()), 380);
        try { if (connected() && !touchUI()) term.focus(); } catch (_) {}
      }

      function confetti() {
        if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
        const c = document.createElement('canvas');
        c.className = 'tour-confetti';
        const dpr = window.devicePixelRatio || 1;
        c.width = innerWidth * dpr; c.height = innerHeight * dpr;
        document.body.appendChild(c);
        const g = c.getContext('2d');
        g.scale(dpr, dpr);
        const colors = ['#ededed', '#a1a1a1', '#7d7d7d', '#ffffff', '#62c073'];
        const cx = innerWidth / 2, cy = innerHeight / 2;
        const bits = Array.from({ length: 160 }, () => {
          const a = Math.random() * Math.PI * 2, v = 5 + Math.random() * 9;
          return { x: cx, y: cy, vx: Math.cos(a) * v, vy: Math.sin(a) * v - 5, w: 5 + Math.random() * 5, h: 8 + Math.random() * 6,
            r: Math.random() * 6, vr: (Math.random() - .5) * .4, c: colors[(Math.random() * colors.length) | 0] };
        });
        const t0 = performance.now();
        (function frame(t) {
          const life = (t - t0) / 2200;
          g.clearRect(0, 0, innerWidth, innerHeight);
          for (const b of bits) {
            b.vx *= 0.985; b.vy = b.vy * 0.985 + 0.32; b.x += b.vx; b.y += b.vy; b.r += b.vr;
            g.save(); g.translate(b.x, b.y); g.rotate(b.r);
            g.globalAlpha = Math.max(0, 1 - life * life);
            g.fillStyle = b.c; g.fillRect(-b.w / 2, -b.h / 2, b.w, b.h * Math.abs(Math.cos(b.r * 2)));
            g.restore();
          }
          if (life < 1) requestAnimationFrame(frame); else c.remove();
        })(t0);
      }

      window.reminalTour = { start };

      // First visit: show it once. A join link waits for the session to come
      // up (or give up) so the tour can show the connected UI; a bare visit
      // starts on the join form. ?tour forces it, for demos and support.
      const params = new URLSearchParams(location.search);
      let seenBefore = false;
      try { seenBefore = !!localStorage.getItem(DONE_KEY); } catch (_) {}
      if (params.has('tour')) seenBefore = false;
      if (seenBefore || document.body.classList.contains('pane-only') || params.has('pane')) return;
      const t0 = Date.now();
      const waitForSession = !!params.get('s');
      // Not while someone is typing, into the terminal or a field such as
      // the PIN: it opens in a pause (TOUR_TYPING_PAUSE_MS since their last
      // key), never over a command being typed. Nor over a field holding
      // what they typed and have not sent yet (a PIN half read off the
      // other screen): it would take the keyboard, and the rest of the PIN
      // and its Enter would go to the tour.
      const TOUR_TYPING_PAUSE_MS = 2000;
      const isField = (el) => !!(el && (el.tagName === 'TEXTAREA' || (el.tagName === 'INPUT' && !/^(button|checkbox|radio|range|submit|reset|file|color)$/i.test(el.type))));
      let lastTermKeyAt = 0;
      document.addEventListener('keydown', (e) => { if (isTermKeys(e.target) || isField(e.target)) lastTermKeyAt = Date.now(); }, true);
      (function poll() {
        const el = document.activeElement;
        const busy = document.querySelector('details[open]') ||
          ($('share-modal') && getComputedStyle($('share-modal')).display !== 'none') ||
          (window.reminalMoments && reminalMoments.isOpen()) ||
          Date.now() - lastTermKeyAt < TOUR_TYPING_PAUSE_MS ||
          (!connected() && isField(el) && !isTermKeys(el) && el.value !== '');
        const ready = !waitForSession || connected() || Date.now() - t0 > 6000;
        if (ready && !busy && Date.now() - t0 > 900) {
          // Connected: give the terminal a moment to paint its first screen
          // so the spotlight lands on something real.
          setTimeout(start, connected() ? 500 : 0);
        } else {
          setTimeout(poll, 250);
        }
      })();
    })();
