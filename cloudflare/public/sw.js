// reminal service worker: phone alerts only. It caches nothing and intercepts
// no requests, so the page always loads fresh from the network exactly as it
// did before this file existed. Its one job is to show the alerts a machine
// you own sends (CPU, battery, charger) when the page is not open.
//
// The alert arrives sealed end-to-end to this browser's push keys; the browser
// decrypts it before handing it here, so event.data is the machine's plain
// message: {title, body, tag, url}.

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('push', (e) => {
  let d = {};
  try { d = e.data ? e.data.json() : {}; } catch (_) { d = { body: e.data ? e.data.text() : '' }; }
  const title = String(d.title || 'reminal').slice(0, 80);
  const body = String(d.body || '').slice(0, 300);
  // A same-origin path only: the message came from a machine, and a machine
  // should not be able to send this browser somewhere else on a tap.
  const url = (typeof d.url === 'string' && d.url.startsWith('/') && !d.url.startsWith('//')) ? d.url : '/';
  e.waitUntil(self.registration.showNotification(title, {
    body,
    // Same machine + same kind replaces the last one instead of stacking.
    tag: (title + ':' + String(d.tag || 'alert')).slice(0, 120),
    renotify: true,
    data: { url },
  }));
});

self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const url = (e.notification.data && e.notification.data.url) || '/';
  e.waitUntil((async () => {
    const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const w of wins) {
      if ('focus' in w) return w.focus();
    }
    return self.clients.openWindow(url);
  })());
});
