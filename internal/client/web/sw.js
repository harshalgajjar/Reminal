// reminal service worker: phone alerts only. It intercepts no requests (it
// keeps one thing: an alerts token for the page, below), so the page always loads fresh from the network exactly as it
// did before this file existed. Its one job is to show the alerts a machine
// you own sends (CPU, battery, charger) when the page is not open.
//
// The alert arrives sealed end-to-end to this browser's push keys; the browser
// decrypts it before handing it here, so event.data is the machine's plain
// message: {title, body, tag, url}.

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

// The relay's own message, when alerts are turned on for a machine: the
// token that binds them to it. Kept for the page (it may be closed by the
// time this comes), which hands it to that machine; and shown, plainly.
const ALERT_TOKENS = 'reminal-alert-tokens';
function alertsOn(e, d) {
  e.waitUntil((async () => {
    if (/^[A-Za-z0-9+/]{43}=$/.test(d.machine) && /^v1\.\d{1,12}\.[A-Za-z0-9_-]{43}$/.test(d.token)) {
      try { await (await caches.open(ALERT_TOKENS)).put('/alert-token/' + encodeURIComponent(d.machine), new Response(d.token)); } catch (_) {}
      for (const w of await self.clients.matchAll({ type: 'window', includeUncontrolled: true })) w.postMessage({ type: 'alert-token' });
    }
    await self.registration.showNotification('reminal', { body: 'Alerts are on for this device.', tag: 'alerts-on:' + String(d.machine).slice(0, 60), data: { url: '/', at: 0 } });
  })());
}

self.addEventListener('push', (e) => {
  let d = {};
  try { d = e.data ? e.data.json() : {}; } catch (_) { d = { body: e.data ? e.data.text() : '' }; }
  if (d && d.kind === 'alerts-on') return alertsOn(e, d);
  const title = String(d.title || 'reminal').slice(0, 80);
  const body = String(d.body || '').slice(0, 300);
  // A same-origin path only: the message came from a machine, and a machine
  // should not be able to send this browser somewhere else on a tap.
  const url = (typeof d.url === 'string' && d.url.startsWith('/') && !d.url.startsWith('//')) ? d.url : '/';
  // Same machine + same kind replaces the last one instead of stacking.
  const tag = (title + ':' + String(d.tag || 'alert')).slice(0, 120);
  // When the machine saw it. Push services do not promise order or speed.
  const at = Number.isFinite(d.at) ? d.at : 0;
  e.waitUntil((async () => {
    let text = body;
    // Arrived late (the phone was asleep, the network was down): say when it
    // actually happened, so "unplugged" an hour on is not read as news.
    if (at && Date.now() - at > 60000) {
      text += ' · at ' + new Date(at).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
    }
    let t = tag, quiet = false;
    // A late "unplugged" must not replace the "connected" that came after
    // it. It still has to show (a push that shows nothing is punished by the
    // browser), so it shows beside the newer one, quietly.
    try {
      const prev = await self.registration.getNotifications({ tag });
      if (at && prev.some((n) => n.data && n.data.at > at)) { t = tag + ':' + at; quiet = true; }
    } catch (_) {}
    await self.registration.showNotification(title, {
      body: text,
      tag: t,
      renotify: !quiet,
      silent: quiet,
      data: { url, at },
    });
  })());
});

self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const url = (e.notification.data && e.notification.data.url) || '/';
  e.waitUntil((async () => {
    const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const w of wins) {
      if ('focus' in w) {
        // An open page is told where the notification points, and goes there
        // itself (routeNotificationURL in app.js); one that has no use for it
        // stays as it is.
        w.postMessage({ type: 'notification-url', url });
        return w.focus();
      }
    }
    return self.clients.openWindow(url);
  })());
});
