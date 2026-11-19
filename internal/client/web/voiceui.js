// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// voiceui.js: the viewer's side of voice. A mic button opens a panel
// holding the voice frame (web/voice/), which hears you and speaks on
// this device. The frame passes only text to this page, and this page carries
// it to the session shown: what you said (typed in as "[voice] ...", by the
// session), that you talked over the agent, and that voice is on. Back to the
// frame go what the agent says with reminal's speak tool, whether it is
// working, and which session is shown. The frame never holds the session's
// keys: it is loaded only when first opened, from this origin, and its
// messages are taken only from it.
//
// There are two mics: the key bar's (#voice-btn, on a phone or a touch
// screen) and the header's (#voice-btn-desk, for a desktop viewer, where
// the key bar is not shown). Both show voice's state: a spinner while it
// starts, then five bars of sound while it runs (green you, purple the
// agent). The key bar's, swiped up, raises a dock of what voice can do now
// (Start; or Stop, Mute, Hold); a desktop viewer has those in the panel.
//
// app.js calls (all safe before voice was ever opened):
//   reminalVoice.hostInfo(info)   host_info arrived: whether voice works there,
//                                 the agent's state and the session's name
//   reminalVoice.speech(text, notice)  a line the agent said with the speak
//                                 tool, or why what was said was not typed in
//   reminalVoice.sessionChanged() the session shown is about to change
//   reminalVoice.busy()           voice is in use (the tour waits for it)
// It reads from app.js (see `app` below): sessionId, lastHostInfo,
// loadHistory, sendWin, keybarTop and headerBottom.
(function () {
  'use strict';

  // ---------------------------------------------------------------- app.js
  // Everything this file uses of app.js, in one place.
  const app = {
    sessionId: () => sessionId,
    hostInfo: () => lastHostInfo,
    savedName(id) {
      try { const e = loadHistory().find((h) => h.id === id); return e && e.name ? String(e.name) : ''; } catch (_) { return ''; }
    },
    // What the frame reports, to the session, as a speech_in message.
    send: (data) => sendWin('speech_in', data),
    keybarTop: () => (typeof keybarTop === 'function' ? keybarTop() : null),
    headerBottom: () => (typeof headerBottom === 'function' ? headerBottom() : 8),
  };

  // The mics: the key bar's (touch: a tap, and a swipe up for the dock) and,
  // for a desktop viewer where the key bar is not shown, the header's (a tap).
  const btn = document.getElementById('voice-btn');
  const deskBtn = document.getElementById('voice-btn-desk');
  const buttons = [btn, deskBtn].filter(Boolean);
  let anchor = btn || deskBtn; // the mic the dock, or a note, shows by
  const TITLE = {
    idle: 'Voice: talk to the agent in this session',
    starting: 'Voice is starting',
    on: 'Voice is on: tap for the conversation',
    // The session's reminal predates voice: its host_info has no voice.
    none: 'Voice needs a newer reminal on that machine',
  };

  // ------------------------------------------------------------ the frame
  let panel = null, frame = null;
  let ready = false;     // the frame said it is loaded and takes commands
  let queued = null;     // a command asked for before it was ready ('start')
  let on = false;        // voice is running
  let muted = false;
  // Whether the session shown takes voice, as its host_info says: true,
  // false (a reminal from before voice: the mic would talk into nothing),
  // or null while its host_info has not come yet.
  let capable = null;

  function toFrame(m) {
    if (frame && frame.contentWindow) frame.contentWindow.postMessage({ src: 'reminal-viewer', ...m }, location.origin);
  }
  // The panel and its frame are made the first time it is shown.
  function show(visible) {
    if (!panel) {
      if (!visible) return;
      panel = document.createElement('div');
      panel.id = 'voice-panel';
      frame = document.createElement('iframe');
      frame.title = 'Voice';
      frame.allow = 'microphone; autoplay';
      frame.src = '/voice/voice.html';
      ready = false;
      panel.appendChild(frame);
      // Shown once the frame says how tall it is (at once, before its
      // libraries load), never first at its largest; in any case soon.
      panel.classList.add('sizing');
      setTimeout(() => panel.classList.remove('sizing'), 2500);
      document.body.appendChild(panel);
    }
    panel.hidden = !visible;
  }
  // A command for the frame, once it is there and loaded; a Start asked for
  // before then opens the panel if loading takes long (its Start is a tap).
  function command(c, held) {
    if (c === 'start' && capable === false) { tell(TITLE.none); return; }
    if (c === 'start') setStarting(true);
    if (frame && ready) { toFrame({ t: 'cmd', c, on: !!held }); return; }
    if (c !== 'start') return;
    queued = 'start';
    if (!panel) { show(true); show(false); }
    setTimeout(() => { if (queued) { queued = null; setStarting(false); show(true); } }, 4000);
  }

  // ------------------------------------------------------------ the name
  // What voice's conversation calls the agent: the session's own name
  // (host_info), else the name it was saved under here, else its id.
  let nameSent = '';
  function nameOf() {
    const id = app.sessionId();
    if (!id) return '';
    const info = app.hostInfo();
    if (info && info.name) return String(info.name);
    return app.savedName(id.toUpperCase()) || id.toUpperCase();
  }
  function sendName(force) {
    if (!frame) return;
    const n = nameOf();
    if (!n || (n === nameSent && !force)) return;
    nameSent = n;
    toFrame({ t: 'name', name: n.slice(0, 60) });
  }

  // ------------------------------------------------------------ the buttons
  // Every mic shows the same state. From Start (swiped or tapped) until it
  // listens, a button spins: the models may take a while to load, and a
  // button that only waits looks like a tap that did nothing. While voice is
  // on, it shows the sound instead of the mic: five bars, as the frame hears
  // and speaks it (green you, purple the agent, dots when quiet), its border
  // what voice is doing. Where the session's reminal predates voice it looks
  // off, and says why.
  function addSpan(b, cls, n) {
    const s = document.createElement('span');
    s.className = cls; s.setAttribute('aria-hidden', 'true');
    for (let i = 0; i < n; i++) s.appendChild(document.createElement('i'));
    b.appendChild(s);
    return s;
  }
  const barsOf = new Map();
  for (const b of buttons) { addSpan(b, 'vspin', 0); barsOf.set(b, addSpan(b, 'vbars', 5)); }
  const each = (fn) => buttons.forEach(fn);

  const idleTitle = () => (capable === false ? TITLE.none : TITLE.idle);
  // The session shown takes voice or not (its host_info). Voice that was on
  // stops, and the panel closes, as its Start would talk into nothing there.
  function setCapable(yes) {
    capable = yes;
    each((b) => {
      b.classList.toggle('novoice', !yes);
      if (!on && !b.classList.contains('starting')) b.title = idleTitle();
    });
    if (yes) { if (actedOn) closeDock(); return; } // its note is moot now
    const open = panel && !panel.hidden;
    if (on) toFrame({ t: 'cmd', c: 'stop', on: false });
    if (on || open) { show(false); tell(TITLE.none, true); }
  }
  function setStarting(starting) {
    each((b) => {
      b.classList.toggle('starting', starting);
      b.title = starting ? TITLE.starting : on ? TITLE.on : idleTitle();
    });
  }
  function setLive(live) {
    on = live;
    each((b) => {
      b.classList.toggle('live', live);
      b.title = live ? TITLE.on : idleTitle();
      if (!live) for (const i of barsOf.get(b).children) { i.style.height = ''; i.className = ''; }
    });
  }
  // The border: what voice is doing (frame's state classes to the button's).
  function setActivity(running, cls) {
    each((b) => {
      b.classList.remove('on', 'hear', 'speak');
      if (running) b.classList.add(cls === 'speak' || cls === 'duck' ? 'speak' : cls === 'hear' ? 'hear' : 'on');
    });
  }
  // One level from the frame: the bars move along, the newest on the right.
  function level(h, who) {
    if (!on) return;
    for (const bars of barsOf.values()) {
      const b = bars.children;
      for (let i = 0; i < b.length - 1; i++) { b[i].style.height = b[i + 1].style.height; b[i].className = b[i + 1].className; }
      const last = b[b.length - 1];
      last.style.height = (3 + Math.max(0, Math.min(1, h)) * 15).toFixed(1) + 'px';
      last.className = who === 'you' || who === 'it' ? who : '';
    }
  }

  // ------------------------------------------------------------ the dock
  // Swipe up from the button, as from the snippet button: a dock of what
  // voice can do now rises above it, and letting go on one does it. Hold
  // holds while the finger rests on it, as its button does. A tap still
  // opens the conversation.
  const dock = document.createElement('div');
  dock.id = 'voicedock';
  document.body.appendChild(dock);
  let hits = [], active = null;

  function dockItems() {
    return on
      ? [['stop', 'Stop', 'Turn voice off'],
        ['mute', muted ? 'Unmute' : 'Mute', muted ? 'Listen again' : 'Stop listening for now'],
        ['hold', 'Hold', 'Rest here; it waits until you let go']]
      : [['start', 'Start', 'Talk to ' + (nameOf() || 'this session')]];
  }
  // The dock above the button, with these rows ([cmd, name, meta]; a row
  // with no cmd is only words).
  function openDock(items = dockItems()) {
    clearTimeout(tellTimer);
    dock.innerHTML = '';
    for (const [c, name, meta] of items) {
      const el = document.createElement('div');
      el.className = 'sessdock-item'; el._cmd = c;
      const n = document.createElement('div'); n.className = 'name'; n.textContent = name;
      el.append(n);
      if (meta) { const m = document.createElement('div'); m.className = 'meta'; m.textContent = meta; el.append(m); }
      dock.appendChild(el);
    }
    // Measured at the left edge: a fixed box shrinks to fit where it was last put.
    dock.style.left = '0px'; dock.style.top = '0px';
    dock.classList.add('open');
    const r = dock.getBoundingClientRect(), hr = anchor.getBoundingClientRect();
    if (hr.top < window.innerHeight / 2) {
      dock.style.top = hr.bottom + 8 + 'px'; // the header's mic: under it
    } else {
      const kbTop = app.keybarTop() ?? hr.top; // the key bar's: over the bar
      dock.style.top = Math.max(app.headerBottom(), kbTop - 12 - r.height) + 'px';
    }
    dock.style.left = Math.max(8, Math.min(window.innerWidth - 8 - r.width, hr.left + hr.width / 2 - r.width / 2)) + 'px';
    hits = [...dock.children].map((el) => { const b = el.getBoundingClientRect(); return { el, top: b.top, bottom: b.bottom, left: b.left, right: b.right }; });
  }
  function closeDock() {
    clearTimeout(tellTimer);
    if (actedOn) {
      document.removeEventListener('pointerdown', actedOn, true);
      document.removeEventListener('keydown', actedOn, true);
      actedOn = null;
    }
    dock.classList.remove('open');
    hits = []; active = null;
  }
  // A few words by the mic: why it did nothing. After a tap they go after a
  // moment; untilActed, they stay until the person's next tap or key (voice
  // stopped without their asking, perhaps while they looked away).
  let tellTimer = 0, actedOn = null;
  const shownMic = () => buttons.find((b) => b.getBoundingClientRect().width > 0) || null;
  function tell(words, untilActed = false) {
    if (!anchor || !anchor.getBoundingClientRect().width) anchor = shownMic() || anchor;
    if (!anchor) return;
    openDock([[null, words, '']]);
    if (!untilActed) { tellTimer = setTimeout(closeDock, 4000); return; }
    // From the next turn of the event loop: not the tap that led here.
    setTimeout(() => {
      actedOn = () => closeDock();
      document.addEventListener('pointerdown', actedOn, true);
      document.addEventListener('keydown', actedOn, true);
    }, 0);
  }
  const itemAt = (x, y) => { const h = hits.find((h) => y >= h.top && y < h.bottom && x >= h.left && x <= h.right); return h ? h.el : null; };

  // A tap on any mic opens (or closes) the conversation; on a session whose
  // reminal predates voice it says so instead.
  let lastSwipeEnd = -Infinity; // no swipe yet: no tap is ever taken for one
  for (const b of buttons) {
    b.addEventListener('click', (e) => {
      if (b === btn && e.timeStamp - lastSwipeEnd < 400) return; // a swipe's release, not a tap
      anchor = b;
      const opening = !panel || panel.hidden;
      if (opening && capable === false) { tell(TITLE.none); return; }
      show(opening);
    });
  }
  // The key bar's mic only: the swipe up for the dock (a desktop viewer has
  // the same commands in the panel).
  if (btn) {
    let gesture = null;
    btn.addEventListener('pointerdown', (e) => {
      anchor = btn;
      try { btn.setPointerCapture(e.pointerId); } catch (_) {}
      gesture = { startY: e.clientY, tried: false, holding: false };
    });
    btn.addEventListener('pointermove', (e) => {
      const g = gesture;
      if (!g) return;
      if (!g.tried && g.startY - e.clientY > 8) { g.tried = true; openDock(); }
      if (!dock.classList.contains('open')) return;
      const over = itemAt(e.clientX, e.clientY);
      if (over === active) return;
      if (active) active.classList.remove('active');
      active = over;
      if (over) over.classList.add('active');
      // Hold is held while the finger rests on it.
      const onHold = !!over && over._cmd === 'hold';
      if (onHold !== g.holding) { g.holding = onHold; command('hold', onHold); }
    });
    const end = (e, cancel) => {
      const g = gesture;
      gesture = null;
      if (!g || !g.tried) return; // a plain tap: the click opens the conversation
      lastSwipeEnd = e.timeStamp;
      const target = cancel ? null : itemAt(e.clientX, e.clientY);
      closeDock();
      if (g.holding) command('hold', false);
      if (target && target._cmd && target._cmd !== 'hold') command(target._cmd);
    };
    btn.addEventListener('pointerup', (e) => end(e, false));
    btn.addEventListener('pointercancel', (e) => end(e, true));
  }

  // ------------------------------------------------------------ from the frame
  const text = (m) => (typeof m.text === 'string' && m.text.trim() ? m.text : '');
  const FROM_FRAME = {
    said(m) { if (text(m)) app.send({ kind: 'said', text: m.text.slice(0, 2000) }); },
    // A part of long talk still going on: typed in as "[voice, continues] ...".
    part(m) { if (text(m)) app.send({ kind: 'part', text: m.text.slice(0, 2000) }); },
    cut(m) { app.send({ kind: 'cut', text: String(m.text || '').slice(-400) }); },
    // voice: false while the frame has nothing to say lines with (the
    // device's voice failed): speak then answers "Not spoken" at once.
    listening(m) { app.send(m.voice === false ? { kind: 'listening', on: !!m.on, voice: false } : { kind: 'listening', on: !!m.on }); },
    // A line of the agent's the device could not say: speak tells the agent.
    unheard(m) { if (text(m)) app.send({ kind: 'unheard', text: m.text.slice(0, 1000) }); },
    state(m) {
      setActivity(m.running, m.cls);
      muted = !!m.muted;
      setStarting(!m.running && /^Loading/.test(String(m.text || '')));
      if (!!m.running !== on) setLive(!!m.running);
    },
    level(m) { level(+m.h || 0, String(m.who || '')); },
    ready() {
      sendName(true);
      ready = true;
      if (queued) { const c = queued; queued = null; toFrame({ t: 'cmd', c, on: false }); }
    },
    close() { show(false); },
    // The panel is as tall as what the frame shows (the CSS height stays the
    // most it may take): no empty dock below its rows.
    height(m) {
      if (panel && Number.isFinite(m.h) && m.h > 0) {
        panel.style.height = 'min(' + Math.ceil(m.h + 2) + 'px, calc(100vh - 140px))';
        panel.classList.remove('sizing');
      }
    },
  };
  window.addEventListener('message', (e) => {
    if (!frame || e.source !== frame.contentWindow || e.origin !== location.origin) return;
    const m = e.data;
    if (!m || m.src !== 'reminal-voice' || !Object.hasOwn(FROM_FRAME, m.t)) return;
    FROM_FRAME[m.t](m);
  });

  // ------------------------------------------------------------ for app.js
  window.reminalVoice = {
    // Every host_info of the session shown: whether its reminal takes voice
    // (an older one leaves "voice" out), and, when it says, the agent's state.
    // (A newer reminal with an older viewer needs nothing: that viewer has no
    // mic, so it never sends speech_in.)
    hostInfo(info) {
      info = info || {};
      setCapable(info.voice === true);
      if (frame && 'attn' in info) toFrame({ t: 'agent', state: info.attn || '' });
      sendName(false);
    },
    // A line the agent said, or a notice that what the person said was not
    // typed in (a prompt is up, no agent is in front, or the terminal hides
    // what is typed): the frame says it.
    speech(t, notice) {
      if (!frame) return;
      if (typeof notice === 'string' && notice) toFrame({ t: 'notice', why: notice.slice(0, 20) });
      else if (typeof t === 'string' && t) toFrame({ t: 'speak', text: t });
    },
    // Voice goes with the session shown: what you say next is typed into
    // that one, and the reply of the one left is not read out over it.
    // Voice is in use (on, starting, or its panel open): the first-visit
    // tour waits rather than cover the mic and the panel.
    busy() {
      return on || queued !== null || buttons.some((b) => b.classList.contains('starting')) || !!(panel && !panel.hidden);
    },
    sessionChanged() {
      toFrame({ t: 'session', id: app.sessionId(), name: nameOf() });
      capable = null; // until the new session's host_info says
      nameSent = '';
    },
  };
})();
