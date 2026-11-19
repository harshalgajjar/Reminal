// What an agent writes, made fit to say out loud. Agents write for a screen:
// markdown, `code`, file paths, links, hashes. Read as written, that is the
// complaint people make most about voice modes ("asterisk asterisk", a URL
// spelled letter by letter, a code block read out). So the voice gets the
// sense of it, and the screen keeps the detail.
//
//   const s = new Speakable();
//   s.push(chunk)  what to say so far (whole words only), as text streams in
//   s.end()        the rest
//   s.take(text)   all of a finished text at once

// How a developer says it: an acronym said as a word, one spelled out, and
// capitals that are only shouting ("do NOT") read as words.
const SAY = { JSON: 'jason', SQL: 'sequel', YAML: 'yammel', TOML: 'tommel', GUI: 'gooey', ASCII: 'askey', OK: 'okay', LGTM: 'looks good to me', WIP: 'work in progress', TODO: 'to do', FYI: 'for your information', IIRC: 'if I remember right', TBD: 'to be decided', NPM: 'N P M', GIF: 'gif', PIN: 'pin', RAM: 'ram', CORS: 'cors', REST: 'rest', CRUD: 'crud', NASA: 'nasa', MCP: 'M C P', PTY: 'P T Y', TTY: 'T T Y', WASM: 'wasm', LLM: 'L L M', README: 'read me' };
const SPELL = new Set(['API', 'CLI', 'PR', 'UI', 'UX', 'URL', 'CSS', 'HTML', 'IDE', 'OS', 'IO', 'CI', 'CD', 'VM', 'PDF', 'SSH', 'TCP', 'UDP', 'DNS', 'HTTP', 'HTTPS', 'AI', 'ID', 'IP', 'SDK', 'GPU', 'CPU', 'TLS', 'SSL', 'JWT', 'AWS', 'GCP', 'USB', 'VPN', 'EOF', 'UTF', 'XML', 'TTS', 'STT', 'VAD', 'AEC', 'MIT', 'BSD', 'GPL', 'AGPL', 'PID', 'UID', 'ENV', 'SHA', 'MD', 'IOS', 'MAC', 'TS', 'JS']);
const UNIT = /^(\d+(?:\.\d+)?)(ms|s|sec|min|h|MB|GB|KB|kB|TB|Mbps|x|fps|Hz|kHz)$/;
const UNITS = { ms: 'milliseconds', s: 'seconds', sec: 'seconds', min: 'minutes', h: 'hours', MB: 'megabytes', GB: 'gigabytes', KB: 'kilobytes', kB: 'kilobytes', TB: 'terabytes', Mbps: 'megabits a second', x: 'times', fps: 'frames a second', Hz: 'hertz', kHz: 'kilohertz' };

function sayCaps(w) {
  const plural = /[A-Z]s$/.test(w) || (/S$/.test(w) && SPELL.has(w.slice(0, -1))) ? 's' : '';
  const base = plural ? w.slice(0, -1) : w;
  if (SAY[base]) return SAY[base] + plural;
  if (SPELL.has(base) || !/[AEIOU]/.test(base)) return base.split('').join(' ') + (plural ? "'s" : '');
  return w.toLowerCase();
}

export class Speakable {
  constructor() { this.buf = ''; this.fence = false; this.toldCode = false; this.lineStart = true; this.space = false; }

  // Text as it is written, a few characters at a time: what to say so far,
  // only whole words, so a link or path is never half-transformed.
  push(s) {
    this.buf += s;
    let i = Math.max(this.buf.lastIndexOf(' '), this.buf.lastIndexOf('\n'), this.buf.lastIndexOf('\t'));
    // A [link text](url) still open waits whole, as its text has spaces.
    const o = this.buf.lastIndexOf('[');
    if (o >= 0 && o <= i && !/\]\([^)\s]*\)/.test(this.buf.slice(o)) && this.buf.length - o < 300) i = o - 1;
    if (i < 0) return '';
    const done = this.buf.slice(0, i + 1);
    this.buf = this.buf.slice(i + 1);
    return this.take(done);
  }

  end() {
    const r = this.take(this.buf + ' ');
    this.buf = '';
    return r + (this.space ? ' ' : '');
  }

  // The space after the last word is held back until the next word comes, so
  // punctuation arriving on its own (an emoji dropped before a full stop) can
  // join the word before it.
  take(text) {
    const r = this.lines(text);
    let out = r.replace(/\s+$/, '');
    if (out && this.space && !/^[.,!?;:]/.test(out)) out = ' ' + out;
    if (out || /\s$/.test(r)) this.space = /\s$/.test(r) || (!out && this.space);
    return out;
  }

  lines(text) {
    let out = '';
    for (const part of text.split(/(\n)/)) {
      if (part === '\n') {
        // A line break is a pause: a list item or a heading ends there.
        if (out.trim()) out = /[.!?,:;]\s*$/.test(out) ? out.replace(/\s*$/, ' ') : out.replace(/\s*$/, '. ');
        this.lineStart = true;
        continue;
      }
      // [text](link): the text, on the whole line, as the text can have spaces.
      const line = part.replace(/\[([^\]]+)\]\([^)\s]+\)/g, '$1');
      for (const tok of line.split(/(\s+)/)) {
        if (!tok) continue;
        if (/^\s+$/.test(tok)) { if (out && !/\s$/.test(out)) out += ' '; continue; }
        if (/^(```|~~~)/.test(tok)) {
          this.fence = !this.fence;
          if (this.fence && !this.toldCode) {
            this.toldCode = true;
            const before = out.trim();
            out = out.replace(/\s*$/, '') + (before && !/[.!?]$/.test(before) ? '. ' : before ? ' ' : '') + "I've put the code on screen. ";
          }
          continue;
        }
        if (this.fence) continue;
        const first = this.lineStart;
        this.lineStart = false;
        const w = this.word(tok, first);
        if (!w) continue;
        if (/^[.,!?;:]+$/.test(w)) out = out.replace(/\s+$/, '') + w; else out += w;
      }
    }
    return out;
  }

  word(t, lineStart) {
    if (lineStart && /^(#{1,6}|[-*+>•]|\d+[.)])$/.test(t)) return ''; // list markers, headings, quotes
    t = t.replace(/\*\*|__|~~|`/g, '').replace(/^[*_#>]+|[*_]+$/g, '').replace(/\p{Extended_Pictographic}|️/gu, '');
    if (!t) return '';
    let [, pre, core, post] = t.match(/^([("'\[]*)(.*?)([)"'\].,!?;:]*)$/);
    if (/^https?:\/\//i.test(core)) core = 'a link to ' + core.replace(/^https?:\/\/(www\.)?/i, '').split(/[\/?#]/)[0];
    else if (/^[~.]?[\w.@-]*\/[\w./@-]+$/.test(core) && (/[.~]/.test(core) || core.split('/').length > 2)) core = core.replace(/\/+$/, '').split('/').pop(); // a path: its name
    else if (/^[0-9a-f]{10,40}$/i.test(core) && /\d/.test(core) && /[a-f]/i.test(core)) core = 'that hash';
    else if (/^v?\d+\.\d+\.\d+/.test(core)) core = core.replace(/^v(?=\d)/, 'version ').replace(/(\d)\.(?=\d)/g, '$1 point ');
    else if (UNIT.test(core)) core = core.replace(UNIT, (_, n, u) => `${n} ${UNITS[u] || u}`);
    else if (/^[A-Z][A-Z0-9]{1,5}s?$/.test(core) && !/^\d/.test(core)) core = sayCaps(core);
    else if (core === '->' || core === '=>' || core === '→') core = 'to';
    else if (core === '&&' || core === '&') core = 'and';
    else if (core === '||' || core === '|' || core === '---' || core === '***') core = '';
    else if (/^e\.g\.?$/i.test(core + post)) { core = 'for example'; post = post.replace(/^\./, ''); }
    else if (/^i\.e\.?$/i.test(core + post)) { core = 'that is'; post = post.replace(/^\./, ''); }
    if (/^[.\/]+$/.test(core)) core = ''; // "./..." and the like
    post = post.replace(/[\])"']/g, '');
    if (core) return pre.replace(/[\[("']/g, '') + core + post;
    return /[.!?]/.test(post) ? post.replace(/[^.!?]/g, '').slice(0, 1) : '';
  }
}
