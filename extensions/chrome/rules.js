// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// Injected into a page (the extension's isolated world, which the page's own
// scripts cannot see) before each of page.js's functions: the rules every
// action is held to, and the map of element refs ("ref_12"), which lasts as
// long as the document.
(() => {
  const R = (globalThis.__reminal ||= { refs: new Map(), n: 0 });
  R.SIGN_IN = 'Paused: this is part of a sign-in form. Ask the person to sign in, then continue.';
  R.CAPTCHA = 'Paused: a CAPTCHA is showing on this page. Ask the person to solve it, then continue.';
  R.NEVER = 'Refused: reminal never types into password or payment fields. Ask the person to type it.';
  R.visible = (el) => {
    const r = el.getBoundingClientRect();
    if (r.width < 2 || r.height < 2) return false;
    const cs = getComputedStyle(el);
    return cs.visibility !== 'hidden' && cs.display !== 'none' && Number(cs.opacity) > 0.05;
  };
  // A CAPTCHA showing: one of the known challenge frames, visible, in view.
  R.captcha = () => [...document.querySelectorAll('iframe')].some((f) => {
    if (!/recaptcha|hcaptcha|challenges\.cloudflare\.com|turnstile|arkoselabs|funcaptcha/i.test((f.src || '') + ' ' + (f.title || ''))) return false;
    const r = f.getBoundingClientRect();
    return R.visible(f) && r.bottom > 0 && r.right > 0 && r.top < innerHeight && r.left < innerWidth;
  });
  // A password or payment field: the person types into those, never an agent.
  R.secret = (el) => !!el && el.tagName === 'INPUT' && (
    (el.type || '').toLowerCase() === 'password' ||
    /^cc-/i.test(el.autocomplete || '') ||
    /card.?num|cc.?num|cvc|cvv|security.?code/i.test((el.name || '') + ' ' + (el.id || '')));
  // Part of a sign-in form: in a form that has a password field (a sign-in
  // box in a page's header, elsewhere on the page, does not count).
  R.signIn = (el) => {
    if (!el || !el.closest) return false;
    if (el.tagName === 'INPUT' && (el.type || '').toLowerCase() === 'password') return true;
    const form = el.closest('form');
    return !!form && !!form.querySelector('input[type=password]');
  };
  R.paused = (el) => (R.captcha() ? R.CAPTCHA : R.signIn(el) ? R.SIGN_IN : '');
  R.what = (el) => {
    if (!el || el === document.body || el === document.documentElement) return 'the page';
    const name = (el.getAttribute('aria-label') || el.innerText || el.value || el.placeholder || el.title || '').trim().replace(/\s+/g, ' ').slice(0, 40);
    return `${el.tagName.toLowerCase()}${name ? ` "${name}"` : ''}`;
  };
})();
