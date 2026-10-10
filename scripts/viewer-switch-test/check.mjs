// Switching sessions (run.sh): to a session on another machine, or one not
// known to be on the same machine, the Windows and Apps panels drop the
// machine being left and say they are loading; a window-list request made
// there is settled with no answer (null), never the new machine's list, so an
// app launch begun there sends nothing to the new machine. Between two
// sessions on the same machine, the lists stay, so the panels open at once.
// A machine's directory listing a session another machine verifiably holds
// does not move it.
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
const srv = http.createServer((q, r) => { r.writeHead(200, { 'content-type': 'text/html' }); r.end(html); }).listen(8099);
const b = await chromium.launch();
const page = await b.newPage();
await page.goto('http://127.0.0.1:8099/');
const out = await page.evaluate(async () => {
  // No host or relay here: nothing is sent, no socket is opened.
  const sent = [];
  sendWin = (type, body) => { sent.push({ type, ...body }); };
  openSocket = () => {};
  const home = (sid, key) => { if (typeof noteSessionHome === 'function') noteSessionHome(sid, key); };
  home('MACAAAA1', 'bWFjLWtleQ'); home('MACAAAA2', 'bWFjLWtleQ'); home('CLOUDBB1', 'Y2xvdWQta2V5');
  const macWindows = { windows: [{ id: 'w1', app: 'Docker Desktop', title: 'Containers' }, { id: 'w2', app: 'Safari', title: 'News' }] };
  const macApps = { apps: [{ name: 'Finder' }, { name: 'Safari' }] };
  const shown = () => document.getElementById('windows-list').textContent;
  const apps = () => document.getElementById('apps-list').textContent;
  const onMac = () => {
    connectToSession('MACAAAA1', '123456');
    document.getElementById('windows').open = true;
    onWindowList(macWindows); onAppList(macApps);
  };
  const res = {};
  for (const [name, to] of [['other machine', 'CLOUDBB1'], ['unknown machine', 'ZZZZ9999'], ['same machine', 'MACAAAA2']]) {
    onMac();
    let answered;
    nextWindowList().then((w) => { answered = w; });
    sent.length = 0;
    let launched = false;
    launchApp({ id: 'safari', name: 'Safari' }).then(() => { launched = true; });
    connectToSession(to, '123456');
    const r = { windows: winList.length, apps: appList.length, list: shown(), appsList: apps() };
    onWindowList({ windows: [{ id: 'c1', app: 'Chromium', title: 'New Tab' }] });
    await new Promise((x) => setTimeout(x, 50));
    r.waiter = answered === undefined ? 'unsettled' : answered === null ? 'null' : answered.map((w) => w.id).join(',');
    r.launchSettled = launched;
    r.appOpenSent = sent.some((m) => m.type === 'app_open');
    pendingLaunch = null;
    res[name] = r;
  }
  // A verified home is not moved by a directory listing; an unknown one is filled.
  const dirList = [{ id: 'CLOUDBB1' }, { id: 'NEWMAC01' }];
  if (typeof noteDirectorySessions === 'function') noteDirectorySessions('bWFjLWtleQ', dirList);
  else for (const x of dirList) noteSessionHome(x.id, 'bWFjLWtleQ'); // what a directory reply did before
  res.directory = { cloudStillElsewhere: !onSameMachine('MACAAAA1', 'CLOUDBB1'), newFilled: onSameMachine('MACAAAA1', 'NEWMAC01') };
  return res;
});
console.log(JSON.stringify(out, null, 1));
const fails = [];
for (const k of ['other machine', 'unknown machine']) {
  const r = out[k];
  if (r.windows !== 0 || /Docker Desktop|Safari/.test(r.list)) fails.push(k + ': the machine left still listed in Windows');
  if (!/Loading windows/.test(r.list)) fails.push(k + ': Windows does not say it is loading');
  if (r.apps !== 0 || /Finder|Safari/.test(r.appsList)) fails.push(k + ': the machine left still listed in Apps');
  if (r.waiter !== 'null') fails.push(k + ': a window-list request made on the machine left was ' + (r.waiter === 'unsettled' ? 'never settled' : 'answered with ' + r.waiter));
  if (!r.launchSettled) fails.push(k + ': an app launch begun on the machine left never settled');
  if (r.appOpenSent) fails.push(k + ': an app launch begun on the machine left was sent to the new one');
}
if (out['same machine'].windows !== 2 || out['same machine'].apps !== 2) fails.push('same machine: its lists were dropped');
if (!out.directory.cloudStillElsewhere) fails.push('a directory listing moved a verified session to its machine');
if (!out.directory.newFilled) fails.push('a directory listing did not fill in an unknown session');
console.log(fails.length ? 'FAIL  ' + fails.join('; ') : 'PASS  a switch to another (or unknown) machine drops the machine left; to the same machine keeps it');
await b.close(); srv.close();
process.exit(fails.length ? 1 : 0);
