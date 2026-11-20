// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The consent window. Its buttons work only once this window has had the
// focus for a second, and stop again when it loses it: a page cannot turn a
// click the person meant for it into "Allow" by moving focus here, and only a
// real click (not a script's) counts.
const q = new URLSearchParams(location.search);
const key = q.get('key') || '', session = q.get('session') || '', name = q.get('name') || '', machine = q.get('machine') || '';
const who = document.getElementById('who');
const strong = document.createElement('strong');
strong.textContent = name ? `"${name}" (${session})` : session;
who.append(strong, `, a reminal session on this computer${machine ? ` (${machine})` : ''}, wants to use your browser.`);

const buttons = [document.getElementById('allow'), document.getElementById('deny')];
let armTimer = 0;
const arm = (on) => {
  clearTimeout(armTimer);
  buttons.forEach((b) => { b.disabled = true; });
  if (on) armTimer = setTimeout(() => { if (document.hasFocus()) buttons.forEach((b) => { b.disabled = false; }); }, 1000);
};
window.addEventListener('focus', () => arm(true));
window.addEventListener('blur', () => arm(false));
document.addEventListener('visibilitychange', () => arm(document.visibilityState === 'visible' && document.hasFocus()));
arm(document.hasFocus());

const answer = (allow) => (e) => {
  if (!e.isTrusted || e.currentTarget.disabled) return;
  buttons.forEach((b) => { b.disabled = true; });
  chrome.runtime.sendMessage({ t: 'consent', key, allow }).finally(() => window.close());
};
buttons[0].addEventListener('click', answer(true));
buttons[1].addEventListener('click', answer(false));
