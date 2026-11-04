// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// pick.js: <ui-pick>, a dropdown that says what each option means. Where a
// native <select> shows one line a choice and nothing of what it does, this
// shows each option as a label with its meaning under it, and keeps the
// chosen one's meaning in view under the control. One element for every
// page (the viewer, the teams page): no browser menu, the same look and
// keys everywhere.
//
//   <ui-pick class="aw-on" aria-label="Leave">
//     <option value="" data-sub="Requests reach them as usual.">Not on leave</option>
//     <option value="1" data-sub="Away for the days below." selected>On leave</option>
//   </ui-pick>
//
// Its options are <option> children, as a select's (an <optgroup> is a
// heading over its options), kept as the element's model: what finds a
// select's options (el.options, el.selectedIndex, option[selected],
// querySelectorAll('option')) finds these. A sub may carry a few lines; an
// option may be disabled.
//
//   el.options = [{ value: '', label: 'Not on leave', sub: '…' }, { head: 'Beside you' }, …]
//                                  // written from code: the <option>s made here
//   el.value = '1';                // as a select's: the chosen value, '' for none
//   el.selectedOption              // { value, label, sub } of the chosen one, or null
//   el.addEventListener('change', …)  // when the person picks (never when code sets value)
//   el.disabled = true;            // or the disabled attribute
//   Pick.make(options, value, attrs)   // a <ui-pick> built from code
//   Pick.html(options, value, attrs)   // the same as HTML, escaped, for a page that writes its forms as strings
//
// Opened, the list is a popover under (or over) the control; on a narrow
// screen, a sheet from the bottom with the control's label as its title.
// Keys as a listbox's: Space, Enter or the arrows open it; the arrows,
// Home and End move; typed letters find a label; Enter or Space pick;
// Escape closes, the keyboard back on the control; Tab leaves it. A tap
// outside closes it. Its colours come from the page's --pick-* variables,
// with the viewer's dark ones as the defaults.
//
// data-compact: the control shows the chosen label alone (a pill, a chip);
// the list still says what each means.
(() => {
  'use strict';
  if (customElements.get('ui-pick')) return;

  const CSS = `
ui-pick { display: block; width: 100%; min-width: 0; }
ui-pick > option, ui-pick > optgroup { display: none; }
ui-pick, .pk-list { --pk-min: var(--pick-min, 40px); --pk-bg: var(--pick-bg, #0d1117); --pk-fg: var(--pick-fg, #e6edf3); --pk-sub: var(--pick-sub, #8b949e); --pk-line: var(--pick-line, #30363d); --pk-focus: var(--pick-focus, #388bfd); --pk-ring: var(--pick-ring, rgba(56,139,253,.18)); --pk-list: var(--pick-list, #161b22); --pk-hover: var(--pick-hover, #21262d); --pk-radius: var(--pick-radius, 8px); --pk-size: var(--pick-size, 14px); }
ui-pick[hidden] { display: none; }
ui-pick .pk-btn { display: flex; align-items: center; gap: 10px; width: 100%; min-height: var(--pk-min); box-sizing: border-box; padding: 8px 11px; border-radius: var(--pk-radius); border: 1px solid var(--pk-line); background: var(--pk-bg); color: var(--pk-fg); font: inherit; font-size: var(--pk-size); text-align: left; cursor: pointer; }
ui-pick .pk-btn:focus-visible { outline: none; border-color: var(--pk-focus); box-shadow: 0 0 0 3px var(--pk-ring); }
ui-pick .pk-btn[aria-expanded="true"] { border-color: var(--pk-focus); }
ui-pick .pk-btn:disabled { opacity: .55; cursor: default; }
ui-pick .pk-text { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
ui-pick .pk-label { font-weight: 500; line-height: 1.3; overflow-wrap: anywhere; }
ui-pick .pk-sub { font-size: calc(var(--pk-size) - 1.5px); line-height: 1.35; color: var(--pk-sub); white-space: pre-line; }
ui-pick .pk-sub:empty { display: none; }
ui-pick[data-compact] .pk-btn { min-height: 0; padding: 4px 8px; gap: 6px; }
ui-pick[data-compact] .pk-sub { display: none; }
ui-pick .pk-chev { flex: none; width: 16px; height: 16px; color: var(--pk-sub); }
.pk-list { position: fixed; z-index: 1000; box-sizing: border-box; max-height: min(60vh, 420px); overflow: auto; padding: 5px; margin: 0; background: var(--pk-list); color: var(--pk-fg); border: 1px solid var(--pk-line); border-radius: 10px; box-shadow: 0 14px 34px rgba(0,0,0,.45); font-size: var(--pk-size); outline: none; overscroll-behavior: contain; }
.pk-list .pk-head { padding: 9px 11px 4px; font-size: calc(var(--pk-size) - 2px); font-weight: 600; text-transform: uppercase; letter-spacing: .04em; color: var(--pk-sub); }
.pk-list .pk-head:not(:first-child) { margin-top: 4px; border-top: 1px solid var(--pk-line); padding-top: 9px; }
.pk-list .pk-opt { display: flex; align-items: flex-start; gap: 10px; padding: 9px 11px; border-radius: 7px; cursor: pointer; }
.pk-list .pk-opt .pk-text { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
.pk-list .pk-opt .pk-label { font-weight: 500; line-height: 1.3; }
.pk-list .pk-opt .pk-sub { font-size: calc(var(--pk-size) - 1.5px); line-height: 1.35; color: var(--pk-sub); white-space: pre-line; }
.pk-list .pk-opt .pk-sub:empty { display: none; }
.pk-list .pk-opt[aria-selected="true"] .pk-label { color: var(--pk-focus); }
.pk-list .pk-opt.pk-active, .pk-list .pk-opt:hover { background: var(--pk-hover); }
.pk-list .pk-opt[aria-disabled="true"] { opacity: .5; cursor: default; }
.pk-list .pk-tick { flex: none; width: 16px; height: 16px; margin-top: 2px; visibility: hidden; color: var(--pk-focus); }
.pk-list .pk-opt[aria-selected="true"] .pk-tick { visibility: visible; }
.pk-list .pk-title { display: none; }
.pk-back { position: fixed; inset: 0; z-index: 999; background: rgba(0,0,0,.45); }
.pk-list.pk-sheet { left: 0 !important; right: 0 !important; top: auto !important; bottom: 0 !important; width: 100% !important; max-height: 80vh !important; border-radius: 16px 16px 0 0; padding: 8px 8px max(8px, env(safe-area-inset-bottom)); }
.pk-list.pk-sheet .pk-title { display: block; padding: 10px 11px 8px; font-weight: 600; color: var(--pk-sub); font-size: calc(var(--pk-size) - 1px); }
.pk-list.pk-sheet .pk-opt { padding: 12px 11px; }
@media (pointer: coarse) { .pk-list .pk-opt { padding: 11px; } }
`;
  const CHEV = '<svg class="pk-chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg>';
  const TICK = '<svg class="pk-tick" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>';
  const SHEET_WIDTH = 600; // below this, the list is a sheet from the bottom

  let style = null;
  const ensureCSS = () => { if (!style) { style = document.createElement('style'); style.id = 'pk-css'; style.textContent = CSS; document.head.appendChild(style); } };
  const str = (v) => (v == null ? '' : String(v));
  const span = (cls) => { const t = document.createElement('span'); t.className = cls; return t; };
  // A label and its meaning, in one node, for the control and for the list.
  const text = (o) => {
    const t = span('pk-text');
    t.appendChild(Object.assign(span('pk-label'), { textContent: o ? o.label : '' }));
    t.appendChild(Object.assign(span('pk-sub'), { textContent: o && o.sub ? o.sub : '' }));
    return t;
  };
  // An <option> as the list reads it.
  const read = (el) => ({ value: el.value, label: el.textContent, sub: el.dataset.sub || '', disabled: el.disabled, el });
  // An <option> from what code gives.
  const option = (o) => {
    const el = document.createElement('option');
    el.value = str(o.value);
    el.textContent = str(o.label == null ? o.value : o.label);
    if (o.sub) el.dataset.sub = str(o.sub);
    if (o.disabled) el.disabled = true;
    return el;
  };

  // The one list open, if any.
  let open = null;
  const closeOpen = () => { if (open) open.close(); };
  document.addEventListener('pointerdown', (e) => { if (open && !open.list.contains(e.target) && !open.pick.contains(e.target)) open.close(); }, true);
  window.addEventListener('resize', () => { if (open) open.place(); });
  window.addEventListener('scroll', () => { if (open) open.place(); }, true);

  class Pick extends HTMLElement {
    static get observedAttributes() { return ['disabled', 'aria-label']; }
    connectedCallback() {
      ensureCSS();
      if (!this.btn) {
        this.btn = document.createElement('button');
        this.btn.type = 'button';
        this.btn.className = 'pk-btn';
        this.btn.setAttribute('aria-haspopup', 'listbox');
        this.btn.setAttribute('aria-expanded', 'false');
        this.btn.appendChild(text(null));
        this.btn.insertAdjacentHTML('beforeend', CHEV);
        this.btn.addEventListener('click', () => (open && open.pick === this ? this.close() : this.open()));
        this.btn.addEventListener('keydown', (e) => {
          if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === ' ' || e.key === 'Enter') { e.preventDefault(); this.open(); }
        });
      }
      // After its options (written as children), where it is drawn.
      this.appendChild(this.btn);
      // No option chosen yet: the first is, as a select's.
      if (!this.selectedOption && this._choices().length) this._choices()[0].el.selected = true;
      this._draw();
    }
    attributeChangedCallback(name) {
      if (!this.btn) return;
      if (name === 'disabled') this.btn.disabled = this.hasAttribute('disabled');
      if (name === 'aria-label') this.btn.setAttribute('aria-label', this.getAttribute('aria-label') || '');
    }
    // Its <option>s, in order, as a select's `options` (headings are the
    // <optgroup>s they sit in).
    get options() { return [...this.querySelectorAll('option')]; }
    set options(list) {
      const was = this.value;
      for (const el of [...this.children]) if (el.tagName === 'OPTION' || el.tagName === 'OPTGROUP') el.remove();
      let into = this, first = true;
      for (const o of Array.isArray(list) ? list : []) {
        if (!o) continue;
        if (o.head) { into = document.createElement('optgroup'); into.label = str(o.head); this.insertBefore(into, this.btn || null); continue; }
        const el = option(o);
        if (into === this) this.insertBefore(el, this.btn || null); else into.appendChild(el);
        if (first) { el.selected = true; first = false; }
      }
      // Kept where the new options have it.
      if (this._choices().some((c) => c.value === was)) this.value = was; else this._draw();
    }
    get selectedIndex() { return this.options.findIndex((el) => el.selected); }
    get selectedOption() { const c = this._choices().find((o) => o.el.selected); return c ? { value: c.value, label: c.label, sub: c.sub } : null; }
    // The options one may choose, read.
    _choices() { return this.options.map(read); }
    get value() { const o = this.selectedOption; return o ? o.value : ''; }
    set value(v) {
      v = str(v);
      // As a select's: a value no option has leaves it unchosen.
      let hit = false;
      for (const el of this.options) { const on = !hit && el.value === v; el.selected = on; if (on) hit = true; }
      this._draw();
    }
    get disabled() { return this.hasAttribute('disabled'); }
    set disabled(v) { if (v) this.setAttribute('disabled', ''); else this.removeAttribute('disabled'); }
    // The chosen option: what the control shows.
    _draw() {
      if (!this.btn) return;
      this.btn.replaceChild(text(this.selectedOption), this.btn.querySelector('.pk-text'));
      this.btn.disabled = this.hasAttribute('disabled');
      if (this.hasAttribute('aria-label')) this.btn.setAttribute('aria-label', this.getAttribute('aria-label'));
      if (open && open.pick === this) open.mark();
    }
    // Chosen by the person: the value, the control, a change event (as a
    // select fires one), and the keyboard back on the control.
    _choose(v) {
      const was = this.value;
      this.close();
      if (v !== was) { this.value = v; this.dispatchEvent(new Event('change', { bubbles: true })); }
      this.btn.focus();
    }
    open() {
      const choices = this._choices();
      if (this.disabled || !choices.length) return;
      closeOpen();
      const list = document.createElement('div');
      list.className = 'pk-list';
      list.setAttribute('role', 'listbox');
      list.tabIndex = -1;
      const label = this.getAttribute('aria-label') || '';
      if (label) list.setAttribute('aria-label', label);
      list.appendChild(Object.assign(document.createElement('div'), { className: 'pk-title', textContent: label }));
      let group = null;
      const opts = choices.map((o, i) => {
        // Its heading, where it starts a group.
        const g = o.el.parentElement && o.el.parentElement.tagName === 'OPTGROUP' ? o.el.parentElement : null;
        if (g && g !== group) list.appendChild(Object.assign(document.createElement('div'), { className: 'pk-head', textContent: g.label }));
        group = g;
        const b = document.createElement('div');
        b.className = 'pk-opt';
        b.id = 'pk-opt-' + Math.random().toString(36).slice(2, 8) + '-' + i;
        b.setAttribute('role', 'option');
        b.dataset.v = o.value;
        if (o.disabled) b.setAttribute('aria-disabled', 'true');
        b.insertAdjacentHTML('beforeend', TICK);
        b.appendChild(text(o));
        b.addEventListener('click', () => { if (!o.disabled) this._choose(o.value); });
        b.addEventListener('pointermove', () => state.activate(i, false));
        list.appendChild(b);
        return b;
      });
      const sheet = window.innerWidth < SHEET_WIDTH;
      let back = null;
      if (sheet) { back = document.createElement('div'); back.className = 'pk-back'; list.classList.add('pk-sheet'); document.body.appendChild(back); }
      document.body.appendChild(list);
      const state = {
        pick: this, list, back, active: -1,
        mark: () => { const v = this.value; opts.forEach((b) => b.setAttribute('aria-selected', b.dataset.v === v ? 'true' : 'false')); },
        activate: (i, scroll) => {
          if (i < 0 || i >= opts.length) return;
          state.active = i;
          opts.forEach((b, k) => b.classList.toggle('pk-active', k === i));
          list.setAttribute('aria-activedescendant', opts[i].id);
          if (scroll !== false) opts[i].scrollIntoView({ block: 'nearest' });
        },
        step: (d) => {
          let i = state.active;
          for (let n = 0; n < opts.length; n++) { i = (i + d + opts.length) % opts.length; if (!choices[i].disabled) break; }
          state.activate(i);
        },
        place: () => {
          if (sheet) return;
          const r = this.btn.getBoundingClientRect(), vh = window.innerHeight, vw = window.innerWidth;
          const width = Math.min(Math.max(r.width, 260), vw - 16);
          list.style.width = width + 'px';
          list.style.left = Math.max(8, Math.min(r.left, vw - width - 8)) + 'px';
          // Under the control where it fits, else over it; at most what fits.
          const below = vh - r.bottom - 8, above = r.top - 8, h = Math.min(list.scrollHeight + 2, Math.min(0.6 * vh, 420));
          if (below >= h || below >= above) { list.style.top = (r.bottom + 4) + 'px'; list.style.bottom = ''; list.style.maxHeight = Math.min(h, below) + 'px'; }
          else { list.style.bottom = (vh - r.top + 4) + 'px'; list.style.top = ''; list.style.maxHeight = Math.min(h, above) + 'px'; }
        },
        close: () => {
          if (open !== state) return;
          open = null;
          list.remove();
          if (back) back.remove();
          this.btn.setAttribute('aria-expanded', 'false');
        },
      };
      open = state;
      this.btn.setAttribute('aria-expanded', 'true');
      state.mark();
      state.place();
      state.activate(Math.max(0, choices.findIndex((o) => o.el.selected)));
      // Typed letters find the next label starting so.
      let typed = '', typedAt = 0;
      list.addEventListener('keydown', (e) => {
        if (e.key === 'ArrowDown') { e.preventDefault(); state.step(1); }
        else if (e.key === 'ArrowUp') { e.preventDefault(); state.step(-1); }
        else if (e.key === 'Home') { e.preventDefault(); state.activate(0); }
        else if (e.key === 'End') { e.preventDefault(); state.activate(opts.length - 1); }
        else if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); const o = choices[state.active]; if (o && !o.disabled) this._choose(o.value); }
        else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); this.close(); this.btn.focus(); }
        else if (e.key === 'Tab') { this.close(); }
        else if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
          const now = Date.now();
          typed = (now - typedAt < 800 ? typed : '') + e.key.toLowerCase(); typedAt = now;
          const from = typed.length === 1 ? state.active + 1 : state.active;
          for (let n = 0; n < opts.length; n++) { const i = (from + n) % opts.length; if (choices[i].label.toLowerCase().startsWith(typed)) { state.activate(i); break; } }
        }
      });
      if (back) back.addEventListener('click', () => this.close());
      list.focus({ preventScroll: true });
    }
    close() { if (open && open.pick === this) open.close(); }
    // The keyboard on the control (a page's el.focus(), as a select's).
    focus(o) { if (this.btn) this.btn.focus(o); }
  }
  customElements.define('ui-pick', Pick);

  const esc = (s) => str(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const attrsOf = (attrs) => Object.entries(attrs || {}).filter(([, v]) => v != null && v !== false).map(([k, v]) => ' ' + k + (v === true ? '' : '="' + esc(v) + '"')).join('');
  const make = (options, value, attrs) => {
    const el = document.createElement('ui-pick');
    for (const k in (attrs || {})) if (attrs[k] != null && attrs[k] !== false) el.setAttribute(k, attrs[k] === true ? '' : attrs[k]);
    el.options = options || [];
    if (value != null) el.value = value;
    return el;
  };
  const html = (options, value, attrs) => {
    let out = '<ui-pick' + attrsOf(attrs) + '>', inGroup = false;
    for (const o of options || []) {
      if (!o) continue;
      if (o.head) { out += (inGroup ? '</optgroup>' : '') + '<optgroup label="' + esc(o.head) + '">'; inGroup = true; continue; }
      out += '<option value="' + esc(o.value) + '"' + (value != null && str(o.value) === str(value) ? ' selected' : '') + (o.disabled ? ' disabled' : '') + (o.sub ? ' data-sub="' + esc(o.sub) + '"' : '') + '>' + esc(o.label == null ? o.value : o.label) + '</option>';
    }
    return out + (inGroup ? '</optgroup>' : '') + '</ui-pick>';
  };
  window.Pick = { make, html, close: closeOpen };
})();
