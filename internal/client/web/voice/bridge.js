// The voice frame's only channel: postMessage to and from the reminal viewer
// that framed it, on the same origin. It carries text and small events, never
// audio, and the frame never sees the session's keys or terminal.
//
// To the viewer ("reminal-voice"):
//   ready | state {text, cls, running, muted} | height {h} | level {h, who}
//   said {text} | part {text} | cut {text} | listening {on} | close
// From the viewer ("reminal-viewer"):
//   speak {text}        a line the agent said with reminal's speak tool
//   agent {state}       the agent's state: working, done, needs you
//   name {name}         what to call the agent (its session's name)
//   session {id, name}  the viewer switched to another session
//   cmd {c, on}         start | stop | mute | hold, from the mic button's dock
//   notice {why}        what they said was not sent: asking | no-agent | no-hook | busy | hidden | unsent
//   stop

const host = window.parent !== window ? window.parent : null;

export const inViewer = !!host;

export function send(t, data = {}) {
  if (host) host.postMessage({ src: 'reminal-voice', t, ...data }, location.origin);
}

export function onMessage(handle) {
  window.addEventListener('message', (e) => {
    if (e.source !== host || e.origin !== location.origin || !e.data || e.data.src !== 'reminal-viewer') return;
    handle(e.data);
  });
}
