// Every timing and threshold of the conversation, in one place, by what it
// governs. Milliseconds unless the name says otherwise.

// The voice-activity model hears 512 samples at 16 kHz per frame.
export const FRAME_MS = 32;

export const T = {
  // Hearing: when speech starts and stops (Silero speech probability).
  startP: 0.5, startFrames: 3,                            // a turn starts after 3 frames (~100 ms) above 0.5
  startPWhileSpeaking: 0.8, startFramesWhileSpeaking: 4,  // stricter while the agent's own voice plays
  keepP: 0.35,                                            // still talking above this
  preRollFrames: 10,                                      // audio kept from before speech was detected
  nearFactor: 0.3,                                        // speech this much quieter than you is someone else

  // Ending a turn.
  finalSilenceMs: 100,        // quiet this long: start transcribing the final words
  checkSilenceMs: 200,        // quiet this long: ask the turn model
  recheckSilenceMs: 600,      // and once more here if it said "not finished"
  maxSilenceMs: 1200,         // quiet this long: the turn is over, whatever it said
  maxSilenceThinkingMs: 2800, // unless it said "not finished": a thinking pause gets this long
  slowTurnModelMs: 350,       // a turn model slower than this here is not waited for...
  fastCommitMs: 550,          // ...and a pause this long ends the turn instead

  // Live text while you talk.
  partialEveryMs: 300, partialEveryMsPhone: 600,
  partialEveryMsOverlap: 100, // while the agent talks, the words decide quickly
  partialMaxSamples: 320000,  // live text covers the last 20 s at most

  // Long talk, sent to the agent in parts as it goes on.
  partEverySec: 12,           // a part at a pause once this much talk is in
  partPauseMs: 150,           // the pause that sends a part (phones)
  maxUttSec: 30,              // a part anyway after this much talk without a pause
  tailMinVoicedMs: 300,       // after a part, less speech than this at the end is no words

  // Talking over the agent.
  bargeStopMs: 600,           // this much voiced talk over it stops it, words or not
  backchannelMaxMs: 1500,     // a "yeah" that runs on this long is talking after all
  backchannelQuietMs: 300,    // quiet this long after a backchannel: its voice comes back up
  overlapWordsMs: 300,        // words count as talking over it once this much speech is in them
  duckGain: 0.12,             // its voice while you talk over it

  // The agent cutting in on long talk: a cue, then you have a moment to stop for it.
  // Timed from when "Sorry," is heard, not from when the agent's line arrived.
  cutInCue: 'Sorry,',
  cutInCueMs: 700,            // how long "Sorry," lasts, where it cannot be measured (the device's voice)
  cutInMinHeardMs: 150,       // stopping counts once this much of it has been heard
  cutInStopMs: 450,           // quiet this long: you stopped for it
  cutInWaitMs: 2500,          // still talking this long after it ended: you talked on over it
  cutInLateMs: 3000,          // ...but stopping within this after that still takes it
  cutInNoSoundMs: 2500,       // a cue that never makes a sound counts as heard after this
  cutBackMs: 800,             // what you said before it is cut at the quietest moment this long before it
  cutPadMs: 400,              // and given this much silence after, as speech that ended

  // Replies.
  ackAfterMs: 350,            // no answer by then: a quick "Okay." first
  ackAfterMsAgain: 900,       // the turn after one that had an "Okay.": wait longer
  quietShowMs: 600,           // its voice quiet this long mid-reply: the status says it is working
  replyQuietEndMs: 6000,      // and this long with no word and no sign of work: the reply is over
  ownReplyEndMs: 1500,        // a reply of its own (no turn to answer) ends after this much quiet
  doneGraceMs: 800,           // after the agent's turn ends: its last line may still be on its way
  noWorkSeenMs: 4000,         // a "done" this long after your words counts even if "working" was never seen
  resumeWithinMs: 15000,      // "go on" picks up a reply cut off this recently
  resumeNothingMs: 5000,      // a talk-over that was nothing picks it up by itself within this

  // Sounds of its own.
  targetRms: 0.075,           // every phrase at this loudness
  pulseAfterMs: 1500, pulseEveryMs: 2400, pulseMax: 2, // nothing said by then: a soft pulse, twice at most
  backEveryMs: 10000, backAfterTalkMs: 6000,          // an "mm-hm" at most this often, after this much talk
};
