// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

// What a stranger can make the relay's rooms do, and cost, run in the real
// Workers runtime (run.sh). Two things:
//   - a viewer naming a session that was never ready leaves nothing behind;
//   - every alarm is bounded (src/alarm.ts), and the rooms' real paths arm
//     only a few times in a session's life.
import { env, runInDurableObject, runDurableObjectAlarm } from "cloudflare:test";
import { describe, expect, it, vi } from "vitest";
import { ALARM_ARMS, ALARM_MAX_PER_HOUR, ALARM_MIN_GAP_MS, armAlarm, type AlarmArms } from "../src/alarm";

const room = (id: string) => env.SESSION.get(env.SESSION.idFromName(id));

async function open(stub: DurableObjectStub, id: string, role: string, ip = "203.0.113.7") {
  const r = await stub.fetch(`https://relay.test/ws/${id}/${role}`, { headers: { Upgrade: "websocket", "cf-connecting-ip": ip } });
  expect(r.status).toBe(101);
  const ws = r.webSocket!;
  ws.accept();
  return ws;
}

function next(ws: WebSocket): Promise<any> {
  return new Promise((resolve) => ws.addEventListener("message", (e) => resolve(JSON.parse(e.data as string)), { once: true }));
}

const stored = (stub: DurableObjectStub) =>
  runInDurableObject(stub, async (_o, state) => ({ keys: [...(await state.storage.list()).keys()], alarm: await state.storage.getAlarm() }));

describe("a session that was never ready", () => {
  it("keeps nothing for a viewer that names it", async () => {
    for (let i = 0; i < 25; i++) {
      const id = `NOPE${String(i).padStart(4, "0")}`;
      const stub = room(id);
      const ws = await open(stub, id, "viewer", `198.51.100.${i}`);
      const err = await next(ws);
      expect(err.error).toBe("session not found or not ready");
      const s = await stored(stub);
      expect(s.keys).toEqual([]);
      expect(s.alarm).toBeNull();
    }
  });
});

describe("armAlarm", () => {
  it("never sets an alarm sooner than the minimum gap", async () => {
    expect(ALARM_MIN_GAP_MS).toBeGreaterThanOrEqual(1000);
    expect(ALARM_MAX_PER_HOUR).toBeLessThanOrEqual(1000);
    const stub = room("GAP00001");
    await runInDurableObject(stub, async (_o, state) => {
      const now = Date.now();
      const when = await armAlarm(state, "SessionRoom", now - 60_000);
      expect(when).toBeGreaterThanOrEqual(now + ALARM_MIN_GAP_MS);
      expect(await state.storage.getAlarm()).toBe(when);
    });
  });

  it("holds a room that arms past the bound until the hour is out, and says so once", async () => {
    const stub = room("RUNAWAY1");
    await runInDurableObject(stub, async (_o, state) => {
      const log = vi.spyOn(console, "error").mockImplementation(() => {});
      const start = Date.now();
      let last = 0;
      for (let i = 1; i <= ALARM_MAX_PER_HOUR + 20; i++) {
        last = await armAlarm(state, "SessionRoom", Date.now());
        if (i === ALARM_MAX_PER_HOUR) expect(last).toBeLessThan(start + 60_000);
      }
      expect(last).toBeGreaterThanOrEqual(start + 60 * 60 * 1000 - 1000);
      const runaway = log.mock.calls.filter((c) => String(c[0]).startsWith("RUNAWAY SessionRoom"));
      expect(runaway.length).toBe(1);
      log.mockRestore();
    });
  });

  it("keeps the count when a room is cleared but kept for its owner", async () => {
    const stub = room("KEEPCNT1");
    await runInDurableObject(stub, async (_o, state) => {
      await state.storage.put({ ownerHash: "h", ownerUntil: Date.now() + 86_400_000 });
      await state.storage.put(ALARM_ARMS, { n: 400, since: Date.now() } satisfies AlarmArms);
      await state.storage.setAlarm(Date.now() + 1000);
    });
    expect(await runDurableObjectAlarm(stub)).toBe(true);
    await runInDurableObject(stub, async (_o, state) => {
      expect((await state.storage.get<AlarmArms>(ALARM_ARMS))?.n).toBe(401);
    });
  });
});

describe("a session's real life", () => {
  it("arms a handful of alarms, and leaves nothing once its owner record runs out", async () => {
    const id = "LIFE0001";
    const stub = room(id);
    const arms = async () => (await runInDurableObject(stub, async (_o, state) => (await state.storage.get<AlarmArms>(ALARM_ARMS))?.n ?? 0));

    // An agent connects, authenticates, drops off and comes back a few times.
    for (let i = 0; i < 3; i++) {
      const agent = await open(stub, id, "agent");
      agent.send(JSON.stringify({ type: "auth", token: "t".repeat(43) }));
      expect((await next(agent)).type).toBe("auth_ok");
      agent.close(1000, "bye");
      await new Promise((r) => setTimeout(r, 50));
    }
    const afterLife = await arms();
    expect(afterLife).toBeGreaterThan(0);
    expect(afterLife).toBeLessThanOrEqual(6);

    // It never comes back: the room expires, keeps its owner record, and
    // that runs out in turn.
    expect(await runDurableObjectAlarm(stub)).toBe(true);
    expect(await arms()).toBeLessThanOrEqual(afterLife + 1);
    await runInDurableObject(stub, async (_o, state) => {
      await state.storage.put("ownerUntil", Date.now() - 1);
    });
    expect(await runDurableObjectAlarm(stub)).toBe(true);
    const s = await stored(stub);
    expect(s.keys).toEqual([]);
    expect(s.alarm).toBeNull();
  });

  it("a copy code arms once", async () => {
    const stub = env.RENDEZVOUS.get(env.RENDEZVOUS.idFromName("ab-cd-ef"));
    const r = await stub.fetch("https://relay.test/rv/ab-cd-ef/source", { headers: { Upgrade: "websocket" } });
    expect(r.status).toBe(101);
    r.webSocket!.accept();
    const n = await runInDurableObject(stub, async (_o, state) => (await state.storage.get<AlarmArms>(ALARM_ARMS))?.n);
    expect(n).toBe(1);
  });
});
