// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// What runs in a page, in the extension's own isolated world. Each function
// is sent on its own by chrome.scripting.executeScript, after rules.js (the
// rules and the ref map, as globalThis.__reminal): no closures.

// locate: where an action on ref, or on the point x, y, lands, and whether
// it is paused there.
export function locate(ref, x, y) {
  const R = globalThis.__reminal;
  let el;
  if (ref) {
    const w = R.refs.get(ref);
    el = w && w.deref();
    if (!el || !el.isConnected) return { error: `${ref} is not on the page any more: read it again with browser_read_page.` };
    el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
    const r = el.getBoundingClientRect();
    x = r.left + r.width / 2; y = r.top + r.height / 2;
  } else if (x != null && y != null) {
    el = document.elementFromPoint(x, y);
  } else {
    return { error: 'Give a ref, or x and y.' };
  }
  return { x, y, what: R.what(el), paused: R.paused(el), secret: R.secret(el) ? R.NEVER : "", frame: !!el && /^(IFRAME|FRAME|OBJECT|EMBED)$/.test(el.tagName) };
}

// focused: what typing would go into, in this frame (each frame answers):
// whether this frame has the focus, and whether its focused element is a
// frame (the focus is further in). Refused for a password or payment field;
// paused in a sign-in form or with a CAPTCHA showing.
export function focused() {
  const R = globalThis.__reminal;
  let el = document.activeElement;
  while (el && el.shadowRoot && el.shadowRoot.activeElement) el = el.shadowRoot.activeElement;
  return {
    hasFocus: document.hasFocus(), frame: !!el && /^(IFRAME|FRAME|OBJECT|EMBED)$/.test(el.tagName),
    what: R.what(el), refused: R.secret(el) ? R.NEVER : '', paused: R.paused(el), captcha: R.captcha(),
  };
}

// frameCheck: whether this frame holds something only the person does (a
// sign-in, a payment field, a CAPTCHA): asked of every frame when an action
// lands on a frame, whose inside cannot be told apart from here.
export function frameCheck() {
  const R = globalThis.__reminal;
  return { personOnly: R.personOnly(), captcha: R.captcha() };
}

export function middle() { return { x: innerWidth / 2, y: innerHeight / 2 }; }

// readTree: the page as an accessibility tree, an element a line, each one
// that can be acted on with a ref. A password's value is never read out.
export function readTree(interactiveOnly) {
  const R = globalThis.__reminal;
  const ROLE = { A: 'link', BUTTON: 'button', SELECT: 'combobox', TEXTAREA: 'textbox', H1: 'heading', H2: 'heading', H3: 'heading', H4: 'heading', H5: 'heading', H6: 'heading', IMG: 'img', NAV: 'navigation', MAIN: 'main', FORM: 'form', UL: 'list', OL: 'list', LI: 'listitem', TABLE: 'table', LABEL: 'label', P: 'paragraph', DIALOG: 'dialog', HEADER: 'banner', FOOTER: 'contentinfo' };
  const INPUT = { checkbox: 'checkbox', radio: 'radio', button: 'button', submit: 'button', reset: 'button', range: 'slider', search: 'searchbox', hidden: '' };
  const roleOf = (el) => el.getAttribute('role') || (el.tagName === 'INPUT' ? (INPUT[(el.type || '').toLowerCase()] ?? 'textbox') : ROLE[el.tagName] || '');
  const actionable = (el, role) => /^(link|button|textbox|searchbox|combobox|checkbox|radio|slider|tab|menuitem|option|switch)$/.test(role) || el.isContentEditable || el.hasAttribute('onclick');
  const nameOf = (el) => {
    const by = el.getAttribute('aria-labelledby');
    let n = el.getAttribute('aria-label') || (by ? by.split(/\s+/).map((id) => (document.getElementById(id) || {}).innerText || '').join(' ') : '');
    if (!n && el.labels && el.labels.length) n = el.labels[0].innerText;
    if (!n) n = el.alt || el.title || el.placeholder || '';
    if (!n && !/^(INPUT|SELECT|TEXTAREA|UL|OL|NAV|MAIN|FORM|TABLE|HEADER|FOOTER)$/.test(el.tagName)) n = el.innerText || '';
    return n.trim().replace(/\s+/g, ' ').slice(0, 100);
  };
  const lines = [];
  if (R.captcha()) lines.push('(A CAPTCHA is showing: actions are paused until the person solves it.)');
  if (R.deepAll(document, 'input, textarea').some(R.passwordish)) lines.push('(This page has a sign-in: actions inside it are paused for the person.)');
  const walk = (el, depth) => {
    if (lines.length > 3000 || el.nodeType !== 1) return;
    if (/^(SCRIPT|STYLE|NOSCRIPT|TEMPLATE)$/.test(el.tagName) || el.getAttribute('aria-hidden') === 'true') return;
    if (el.id === '__reminal_indicator' || getComputedStyle(el).display === 'none') return;
    const role = roleOf(el);
    const act = actionable(el, role) && R.visible(el);
    let next = depth;
    if (role && (act || !interactiveOnly)) {
      let ref = el.__reminalRef;
      if (!ref) { ref = el.__reminalRef = 'ref_' + (++R.n); R.refs.set(ref, new WeakRef(el)); }
      let line = `${'  '.repeat(depth)}- ${role}`;
      const name = nameOf(el);
      if (name) line += ` "${name}"`;
      if (act) line += ` [${ref}]`;
      if (/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) {
        if (R.passwordish(el)) line += ' (password: the person types it)';
        else if (R.payment(el)) line += ' (payment: the person types it)';
        else if (el.type === 'checkbox' || el.type === 'radio') line += el.checked ? ' checked' : ' unchecked';
        else if (el.value) line += ` value="${String(el.value).slice(0, 80)}"`;
      }
      if (/^H[1-6]$/.test(el.tagName)) line += ` level=${el.tagName[1]}`;
      if (el.tagName === 'A' && el.getAttribute('href')) line += ` href="${el.getAttribute('href').slice(0, 120)}"`;
      lines.push(line);
      next = depth + 1;
    }
    for (const c of el.children) walk(c, next);
    if (el.shadowRoot) for (const c of el.shadowRoot.children) walk(c, next);
  };
  walk(document.body, 0);
  return `${document.title} (${location.href})\n` + lines.join('\n');
}

// pageText: the readable text, its main part when it has one.
export function pageText() {
  const main = document.querySelector('main, article, [role=main]') || document.body;
  const t = (main.innerText || '').replace(/\n{3,}/g, '\n\n').trim();
  return `${document.title} (${location.href})\n\n` + (t.length > 100000 ? t.slice(0, 100000) + '\n(cut at 100000 characters)' : t);
}

// indicator: a border round the page and a label while an agent acts on it.
// In a closed shadow root of its own (the page's styles and scripts do not
// reach in), and never in the way of a click.
export function indicator(show, name) {
  let host = document.getElementById('__reminal_indicator');
  if (!show) { if (host) host.remove(); return; }
  if (host) return;
  host = document.createElement('div');
  host.id = '__reminal_indicator';
  host.style.cssText = 'all:initial;position:fixed;inset:0;pointer-events:none;z-index:2147483647';
  const root = host.attachShadow({ mode: 'closed' });
  root.innerHTML = '<style>.b{position:fixed;inset:0;border:3px solid #e8762d;border-radius:4px;box-sizing:border-box}' +
    '.l{position:fixed;bottom:12px;left:50%;transform:translateX(-50%);background:#e8762d;color:#fff;font:600 13px/1.3 system-ui,sans-serif;padding:6px 12px;border-radius:14px;box-shadow:0 2px 8px rgba(0,0,0,.25)}</style>' +
    '<div class="b"></div><div class="l"></div>';
  root.querySelector('.l').textContent = `reminal: ${name} is using this tab`;
  (document.documentElement || document.body).appendChild(host);
}
