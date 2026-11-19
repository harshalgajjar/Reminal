// How loud it is, for the viewer's mic button: every STEP ms while voice is
// on, a level from 0 to 1 and whose sound it is (you, the agent, or quiet).
// On a timer, so it goes on while the panel is closed.
import { send } from './bridge.js';

const STEP = 70;
const QUIET = 0.1;

// Gently curved, so speech rises and falls as a voice memo's trace does, and
// a quiet room stays near 0.
const level = (rms) => Math.pow(Math.min(1, rms / 0.25), 0.6);
export const rmsOf = (x) => { let e = 0; for (let i = 0; i < x.length; i++) e += x[i] * x[i]; return Math.sqrt(e / Math.max(1, x.length)); };

let micEnergy = 0, micSamples = 0, mic = 0, timer = 0;
// The last levels sent, newest last (read by tests and the timings).
export const recentLevels = [];
const KEEP = 400;

// A frame of the mic: averaged over the frames between two steps.
export function micFrame(frame) {
  for (let i = 0; i < frame.length; i++) micEnergy += frame[i] * frame[i];
  micSamples += frame.length;
}

// agentLevel: the agent's voice now, 0 to 1. muted: your side reads as quiet.
export function startLevels({ agentLevel, muted }) {
  micEnergy = micSamples = mic = 0; recentLevels.length = 0;
  clearInterval(timer);
  timer = setInterval(() => {
    if (micSamples) { mic = Math.sqrt(micEnergy / micSamples); micEnergy = micSamples = 0; }
    const you = muted() ? 0 : level(mic), it = agentLevel();
    const who = it > QUIET && it >= you ? 'it' : you > QUIET ? 'you' : 'idle';
    const h = who === 'idle' ? 0 : Math.round(Math.max(you, it) * 100) / 100;
    recentLevels.push({ h, who }); if (recentLevels.length > KEEP) recentLevels.splice(0, recentLevels.length - KEEP);
    send('level', { h, who });
  }, STEP);
}

export function stopLevels() { clearInterval(timer); timer = 0; recentLevels.length = 0; }

export const levelOf = level;
