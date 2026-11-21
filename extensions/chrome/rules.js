// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// Injected into every frame of a page (the extension's isolated world, which
// the page's own scripts cannot see) before each of page.js's functions: the
// rules every action is held to, and the map of element refs ("ref_12"),
// which lasts as long as the document.
(() => {
  const R = (globalThis.__reminal ||= { refs: new Map(), n: 0, wasPassword: new WeakSet() });
  R.SIGN_IN = 'Paused: this is part of a sign-in form. Ask the person to sign in, then continue.';
  R.CAPTCHA = 'Paused: a CAPTCHA is showing on this page. Ask the person to solve it, then continue.';
  R.NEVER = 'Refused: reminal never types into password or payment fields. Ask the person to type it.';
  R.FRAME = 'Paused: that part of the page is a frame with a sign-in, payment field or CAPTCHA in it. Ask the person to do it, then continue.';

  // Every element matching sel, in open shadow roots too.
  R.deepAll = (root, sel) => {
    const out = [...root.querySelectorAll(sel)];
    for (const el of root.querySelectorAll('*')) if (el.shadowRoot) out.push(...R.deepAll(el.shadowRoot, sel));
    return out;
  };
  // The element's parent, across a shadow root's edge.
  R.up = (el) => el.parentElement || (el.parentNode && el.parentNode.host) || null;

  R.visible = (el) => {
    const r = el.getBoundingClientRect();
    if (r.width < 2 || r.height < 2) return false;
    const cs = getComputedStyle(el);
    return cs.visibility !== 'hidden' && cs.display !== 'none' && Number(cs.opacity) > 0.05;
  };

  // A field that is, or was, a password (a "show password" button makes it
  // text for a while), or that a site marks as one: its autocomplete, its
  // name, or text drawn as dots.
  const SECRET_WORDS = /\bpass(word|wd|code|phrase)?\b|passwd|\bpwd\b|\bpin\b|\botp\b|one.?time.?(code|pass)|2fa|totp/i;
  const PAY_WORDS = /card.?num|cc.?num|cc.?(csc|cvc|cvv|exp)|\bcvc\b|\bcvv\b|\bcsc\b|security.?code|card.?code/i;
  R.textField = (el) => !!el && (el.tagName === 'TEXTAREA' || el.isContentEditable || (el.tagName === 'INPUT' && !/^(checkbox|radio|button|submit|reset|file|image|hidden|range|color)$/i.test(el.type || '')));
  R.passwordish = (el) => {
    if (!R.textField(el)) return false;
    if ((el.type || '').toLowerCase() === 'password' || R.wasPassword.has(el)) return true;
    const ac = (el.getAttribute('autocomplete') || '').toLowerCase();
    if (/\b(current-password|new-password|one-time-code)\b/.test(ac)) return true;
    const ts = getComputedStyle(el).getPropertyValue('-webkit-text-security');
    if (ts && ts !== 'none') return true;
    const words = [el.name, el.id, el.getAttribute('aria-label'), el.placeholder].filter(Boolean).join(' ');
    return SECRET_WORDS.test(words);
  };
  R.payment = (el) => {
    if (!R.textField(el)) return false;
    if (/\bcc-/.test((el.getAttribute('autocomplete') || '').toLowerCase())) return true;
    return PAY_WORDS.test([el.name, el.id, el.getAttribute('aria-label'), el.placeholder].filter(Boolean).join(' '));
  };
  R.secret = (el) => R.passwordish(el) || R.payment(el);
  // A dropdown that is part of a payment (a card's expiry month and year).
  R.secretSelect = (el) => !!el && el.tagName === 'SELECT' && (/\bcc-/.test((el.getAttribute('autocomplete') || '').toLowerCase()) || PAY_WORDS.test([el.name, el.id, el.getAttribute('aria-label')].filter(Boolean).join(' ')) || /exp.*(month|year)|card.?exp/i.test([el.name, el.id, el.getAttribute('aria-label')].filter(Boolean).join(' ')));

  // Watch for a password field turning into text (shown), so it stays one.
  const mark = () => R.deepAll(document, 'input').forEach((i) => { if ((i.type || '').toLowerCase() === 'password') R.wasPassword.add(i); });
  mark();
  if (!R.observing) {
    R.observing = true;
    new MutationObserver((ms) => {
      for (const m of ms) {
        if (m.type === 'attributes' && m.oldValue && m.oldValue.toLowerCase() === 'password') R.wasPassword.add(m.target);
        if (m.type === 'childList') mark();
      }
      // A sign-in or payment field that comes and goes between an agent's
      // tools is remembered for this document (R.ever): looked for at most
      // twice a second.
      if (!R.ever && !R.everTimer) R.everTimer = setTimeout(() => { R.everTimer = 0; if (R.personOnlyFields()) R.ever = true; }, 500);
    }).observe(document, { subtree: true, childList: true, attributes: true, attributeFilter: ['type'], attributeOldValue: true });
  }

  // A CAPTCHA showing: one of the known challenge frames or widgets,
  // visible, in view (shadow roots too).
  const CAPTCHA_SRC = /recaptcha|hcaptcha|challenges\.cloudflare\.com|turnstile|arkoselabs|funcaptcha|geetest/i;
  R.captcha = () => R.deepAll(document, 'iframe, .g-recaptcha, .h-captcha, .cf-turnstile').some((f) => {
    if (f.tagName === 'IFRAME' && !CAPTCHA_SRC.test((f.src || '') + ' ' + (f.title || ''))) return false;
    const r = f.getBoundingClientRect();
    return R.visible(f) && r.bottom > 0 && r.right > 0 && r.top < innerHeight && r.left < innerWidth;
  });

  // Part of a sign-in form: the element is a password field, or is in a form
  // (its own, or the one its form= attribute names) that has one; or, with no
  // form, in the smallest group around it that holds other fields and a
  // password one (a sign-in made of loose fields). A sign-in box elsewhere on
  // the page does not count.
  R.signIn = (el) => {
    if (!el) return false;
    if (R.passwordish(el)) return true;
    const form = el.form || (() => { for (let p = R.up(el); p; p = R.up(p)) if (p.tagName === 'FORM') return p; return null; })();
    if (form) return [...form.elements].some(R.passwordish) || R.deepAll(form, 'input').some(R.passwordish);
    for (let p = R.up(el), i = 0; p && p !== document.body && p !== document.documentElement && i < 8; p = R.up(p), i++) {
      const fields = R.deepAll(p, 'input, textarea, select, button, [contenteditable]');
      if (fields.length > 1) return fields.some(R.passwordish);
    }
    return false;
  };
  // Anything here that only the person does: a sign-in, a payment field, a
  // CAPTCHA (for a frame an action may land in).
  R.personOnlyFields = () => R.deepAll(document, 'input, textarea, [contenteditable]').some(R.secret);
  R.personOnly = () => R.captcha() || R.personOnlyFields();
  if (R.personOnlyFields()) R.ever = true;
  R.paused = (el) => (R.captcha() ? R.CAPTCHA : R.signIn(el) ? R.SIGN_IN : '');
  R.what = (el) => {
    if (!el || el === document.body || el === document.documentElement) return 'the page';
    const name = R.secret(el) ? '' : (el.getAttribute('aria-label') || el.innerText || el.value || el.placeholder || el.title || '').trim().replace(/\s+/g, ' ').slice(0, 40);
    return `${el.tagName.toLowerCase()}${name ? ` "${name}"` : ''}`;
  };
})();
