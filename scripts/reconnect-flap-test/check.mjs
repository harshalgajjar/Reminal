// A session's terminal while its relay link flaps. reminal's relay listens on
// 18080; the session reaches it through a proxy on 18081 that, while flapping,
// resets every connection 4s after it opens (a reset, as the middleboxes in
// the field send). The session runs under script(1), so what it prints into
// its own terminal is read back from the typescript.
//
//   0-10s    steady
//   10-70s   flapping: every connection reset after 4s
//   70-140s  steady: the link holds (a redial may wait out a 30s backoff,
//            and the link then has to hold for 30s)
//   140s     one reset, straight back
//   140-185s steady
//
// Alongside, from 0s, a second session whose relay is out of reach from the
// start: it must say so within a few seconds, not leave its join link
// unexplained.
import { spawn } from "node:child_process";
import net from "node:net";
import fs from "node:fs";

const t0 = Date.now();
const at = () => (Date.now() - t0) / 1000;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const relay = spawn("reminal", ["relay", "18080"], { stdio: "ignore" });
await sleep(2000);

let flapping = false;
const live = new Set();
net.createServer((c) => {
  const up = net.connect(18080, "127.0.0.1");
  c.pipe(up).pipe(c);
  const pair = { c, up };
  live.add(pair);
  const kill = () => { live.delete(pair); c.resetAndDestroy(); up.destroy(); };
  c.on("error", () => {}); up.on("error", () => {});
  c.on("close", () => { live.delete(pair); up.destroy(); });
  up.on("close", () => { live.delete(pair); c.destroy(); });
  if (flapping) setTimeout(kill, 4000);
  pair.kill = kill;
}).listen(18081, "127.0.0.1");

const log = "/tmp/term.log";
const sess = spawn("script", ["-qfec", "reminal", log], {
  stdio: ["pipe", "ignore", "ignore"],
  env: { ...process.env, REMINAL_RELAY: "ws://127.0.0.1:18081/ws", REMINAL_WEB: "http://127.0.0.1:18080" },
});
const offLog = "/tmp/offline.log";
const offHome = fs.mkdtempSync("/tmp/home-");
const offline = spawn("script", ["-qfec", "reminal", offLog], {
  stdio: ["pipe", "ignore", "ignore"],
  env: { ...process.env, HOME: offHome, REMINAL_RELAY: "ws://127.0.0.1:1/ws", REMINAL_WEB: "http://127.0.0.1:1" },
});
setTimeout(() => offline.kill(), 8000);
const size = () => { try { return fs.statSync(log).size; } catch { return 0; } };
const between = (a, b) => fs.readFileSync(log).subarray(a, b).toString();

const until = async (s) => { while (at() < s) await sleep(200); };
await until(10);
const mFlap = size();
flapping = true;
for (const p of live) p.kill(); // the open link drops too
await until(70);
flapping = false;
await until(140);
const mBlip = size();
for (const p of live) p.kill();
await until(185);
const mEnd = size();
sess.kill(); relay.kill();

const count = (s, re) => (s.match(re) || []).length;
const down = /Retrying until it is back/g, back = /back online/g;
const flap = between(mFlap, mBlip), blip = between(mBlip, mEnd);
const off = fs.existsSync(offLog) ? fs.readFileSync(offLog).toString() : "";
const res = [
  ["started offline: said within 8s", count(off, down) === 1, `${count(off, down)} outage lines`],
  ["flapping minute: outage said at most once", count(flap, down) <= 1, `${count(flap, down)} outage lines`],
  ["flapping minute: back online at most once", count(flap, back) <= 1, `${count(flap, back)} back-online lines`],
  ["flapping minute: an outage said is ended once the link holds", count(flap, down) === count(flap, back), `${count(flap, down)} down, ${count(flap, back)} back`],
  ["single drop: nothing said", count(blip, down) + count(blip, back) === 0, `${count(blip, down)} down, ${count(blip, back)} back`],
];
let fail = 0;
for (const [name, ok, why] of res) {
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}  (${why})`);
  if (!ok) fail++;
}
console.log(fail ? `${fail} failed` : "all passed");
process.exit(fail ? 1 : 0);
