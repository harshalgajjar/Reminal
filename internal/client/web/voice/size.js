// How tall the voice panel's content is, told to the viewer as soon as the
// page is laid out, and again whenever it changes (a section opened, the
// conversation growing). It runs before the speech libraries load, so the
// viewer can show the panel at its size from the start rather than at its
// largest until they have downloaded.
(() => {
  if (window.parent === window) return;
  const tell = () => {
    const el = document.getElementById('hero');
    if (el) window.parent.postMessage({ src: 'reminal-voice', t: 'height', h: el.getBoundingClientRect().height }, location.origin);
  };
  const watch = () => {
    tell();
    const el = document.getElementById('hero');
    if (el && window.ResizeObserver) new ResizeObserver(tell).observe(el);
  };
  // Loaded right after the panel's markup and before the libraries, whose
  // download holds up DOMContentLoaded: the panel is already there.
  if (document.getElementById('hero')) watch(); else document.addEventListener('DOMContentLoaded', watch);
})();
