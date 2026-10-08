// Every alarm a room sets goes through armAlarm.
//
// An alarm is the one way a Durable Object runs with nobody asking it to, and
// one that keeps re-arming itself bills for every run with no ceiling:
// Cloudflare has no spend cap. The rooms here arm only on a socket closing,
// a room expiring and an owner record running out, a few times in a room's
// life. armAlarm makes that a bound rather than a property of today's code:
// no alarm sooner than ALARM_MIN_GAP_MS from now, and past ALARM_MAX_PER_HOUR
// arms in an hour none sooner than the hour's end, with one RUNAWAY line in
// the log. The room still expires; it just cannot spin.

export const ALARM_MIN_GAP_MS = 1000;
export const ALARM_MAX_PER_HOUR = 500;
const ALARM_WINDOW_MS = 60 * 60 * 1000;

// ALARM_ARMS is the storage key of a room's count.
export const ALARM_ARMS = "alarmArms";

export interface AlarmArms {
  n: number; // arms in the window
  since: number; // when the window began
  warned?: boolean; // RUNAWAY logged for this window
}

// armAlarm sets the room's alarm for at, or later if the bounds say so, and
// returns when it was set for.
export async function armAlarm(state: DurableObjectState, room: string, at: number): Promise<number> {
  const now = Date.now();
  let arms = (await state.storage.get<AlarmArms>(ALARM_ARMS)) ?? { n: 0, since: now };
  if (now - arms.since >= ALARM_WINDOW_MS) arms = { n: 0, since: now };
  arms.n++;
  let when = Math.max(at, now + ALARM_MIN_GAP_MS);
  if (arms.n > ALARM_MAX_PER_HOUR) {
    if (!arms.warned) {
      console.error(`RUNAWAY ${room} ${state.id.toString()}: ${arms.n} alarms armed since ${new Date(arms.since).toISOString()}; holding the next until the hour is out`);
      arms.warned = true;
    }
    when = Math.max(when, arms.since + ALARM_WINDOW_MS);
  }
  await state.storage.put(ALARM_ARMS, arms);
  await state.storage.setAlarm(when);
  return when;
}
