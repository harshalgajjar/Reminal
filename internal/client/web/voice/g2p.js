// Text to phonemes for the natural voice (Kokoro), with no GPL phonemizer in
// it: words are looked up in CMUdict (BSD-2-Clause, see NOTICE) and written
// in the phoneme set Kokoro was trained on (misaki, US English); words it
// does not have are built from the words they are made of, spelled out when
// they are initials, or sounded out by rule.
//
//   await loadG2P(baseUrl)   once: fetches cmudict.txt next to this file
//   phonemize(text)          misaki phonemes, words space-separated, the
//                            punctuation Kokoro reads kept

let dict = null;

export async function loadG2P(base) {
  if (dict) return;
  const url = new URL('cmudict.txt', base || import.meta.url);
  loadG2PFromText(await (await fetch(url)).text());
}
// The dictionary's text as it is (tests read the file themselves).
export function loadG2PFromText(text) {
  dict = new Map();
  for (const line of text.split('\n')) {
    const i = line.indexOf(' ');
    if (i > 0) dict.set(line.slice(0, i), line.slice(i + 1));
  }
}

// ---------- CMUdict (ARPAbet) to misaki ----------
const VOWEL = {
  AA: 'ɑ', AE: 'æ', AO: 'ɔ', AW: 'W', AY: 'I', EH: 'ɛ', EY: 'A', IH: 'ɪ',
  IY: 'i', OW: 'O', OY: 'Y', UH: 'ʊ', UW: 'u',
};
const CONS = {
  B: 'b', CH: 'ʧ', D: 'd', DH: 'ð', F: 'f', G: 'ɡ', HH: 'h', JH: 'ʤ', K: 'k',
  L: 'l', M: 'm', N: 'n', NG: 'ŋ', P: 'p', R: 'ɹ', S: 's', SH: 'ʃ', T: 't',
  TH: 'θ', V: 'v', W: 'w', Y: 'j', Z: 'z', ZH: 'ʒ',
};
const MARK = { 1: 'ˈ', 2: 'ˌ', 0: '' };

// One word's ARPAbet ("W AO1 T ER0") as misaki ("wˈɔɾəɹ").
function arpaToMisaki(arpa) {
  const ph = arpa.trim().split(/\s+/);
  const isVowel = (p) => p && /\d$/.test(p);
  let out = '';
  for (let i = 0; i < ph.length; i++) {
    const p = ph[i], m = p.match(/^([A-Z]+)(\d)?$/);
    if (!m) continue;
    const [, base, st] = m;
    if (st !== undefined) {
      // An unstressed "up" opening a word keeps its vowel ("upgrade" is not "a grade").
      if (base === 'AH') out += st === '0' ? (i === 0 && ph[1] === 'P' ? 'ʌ' : 'ə') : MARK[st] + 'ʌ';
      else if (base === 'ER') out += st === '0' ? 'əɹ' : MARK[st] + 'ɜɹ';
      else out += MARK[st] + (VOWEL[base] || '');
      continue;
    }
    // American flap: a t (or d) between a vowel and an unstressed vowel
    // ("water", "better", "city"), never at the start of the word.
    if ((base === 'T' || base === 'D') && i > 0 && (isVowel(ph[i - 1]) || ph[i - 1] === 'R')
        && /0$/.test(ph[i + 1] || '')) { out += 'ɾ'; continue; }
    out += CONS[base] || '';
  }
  return out;
}

// ---------- spelling out ----------
const LETTER = {
  a: 'ˈA', b: 'bˈi', c: 'sˈi', d: 'dˈi', e: 'ˈi', f: 'ˈɛf', g: 'ʤˈi', h: 'ˈAʧ',
  i: 'ˈI', j: 'ʤˈA', k: 'kˈA', l: 'ˈɛl', m: 'ˈɛm', n: 'ˈɛn', o: 'ˈO', p: 'pˈi',
  q: 'kjˈu', r: 'ˈɑɹ', s: 'ˈɛs', t: 'tˈi', u: 'jˈu', v: 'vˈi', w: 'dˈʌbəlju',
  x: 'ˈɛks', y: 'wˈI', z: 'zˈi',
};
const spell = (w) => w.toLowerCase().split('').map((c) => LETTER[c] || '').join(' ');

// ---------- words it does not have ----------
// Endings added to a word it has: "pushed" from "push", "tests" from "test".
function endingAfter(base, suffix) {
  const last = base.slice(-1);
  const voicelessEnd = /[pkfθsʃʧt]$/.test(base);
  if (suffix === 's') return /[szʃʒʧʤ]$/.test(base) ? base + 'ᵻz' : base + (voicelessEnd ? 's' : 'z');
  if (suffix === 'ed') return /[td]$/.test(base) ? base + 'ᵻd' : base + (voicelessEnd ? 't' : 'd');
  if (suffix === 'ing') return base + 'ɪŋ';
  if (suffix === 'er') return base + 'əɹ';
  if (suffix === 'ly') return base + 'li';
  return base + last; // not reached
}
const ENDINGS = [
  ['ing', 'ing', ['', 'e']], ['ed', 'ed', ['', 'e']], ['es', 's', ['']], ['s', 's', ['']],
  ["'s", 's', ['']], ['er', 'er', ['', 'e']], ['ly', 'ly', ['']],
];

// The cheapest way to build a word from words it has ("livereload" from
// "live" + "reload"): fewest pieces, none shorter than three letters.
function fromPieces(w) {
  const n = w.length, best = new Array(n + 1).fill(null);
  best[0] = { cost: 0, parts: [] };
  for (let i = 0; i < n; i++) {
    if (!best[i]) continue;
    for (let j = i + 2; j <= n; j++) {
      const piece = w.slice(i, j), ph = dict.get(piece);
      // Two letters only for real little words ("me" in "readme"), never as a
      // first piece glued on by accident.
      if (!ph || (piece.length === 2 && !SHORT.has(piece))) continue;
      const c = best[i].cost + (piece.length === 2 ? 1.5 : 1);
      if (!best[j] || c < best[j].cost) best[j] = { cost: c, parts: best[i].parts.concat(ph) };
    }
  }
  const b = best[n];
  return b && b.cost <= 3 ? b.parts.map(arpaToMisaki).join('') : null;
}

// Sounding out by rule: the last resort, for names and made-up words.
const RULES = [
  ['tion', 'ʃən'], ['sion', 'ʒən'], ['ough', 'ʌf'], ['igh', 'I'], ['tch', 'ʧ'], ['dge', 'ʤ'],
  ['th', 'θ'], ['sh', 'ʃ'], ['ch', 'ʧ'], ['ph', 'f'], ['wh', 'w'], ['ck', 'k'], ['ng', 'ŋ'],
  ['qu', 'kw'], ['kn', 'n'], ['wr', 'ɹ'], ['ee', 'i'], ['ea', 'i'], ['oo', 'u'], ['ai', 'A'],
  ['ay', 'A'], ['oa', 'O'], ['ow', 'O'], ['ou', 'W'], ['oi', 'Y'], ['oy', 'Y'], ['au', 'ɔ'],
  ['aw', 'ɔ'], ['ew', 'u'], ['ie', 'i'], ['er', 'əɹ'], ['ar', 'ɑɹ'], ['or', 'ɔɹ'], ['ir', 'ɜɹ'],
  ['ur', 'ɜɹ'], ['a', 'æ'], ['e', 'ɛ'], ['i', 'ɪ'], ['o', 'ɑ'], ['u', 'ʌ'], ['y', 'i'],
  ['b', 'b'], ['c', 'k'], ['d', 'd'], ['f', 'f'], ['g', 'ɡ'], ['h', 'h'], ['j', 'ʤ'], ['k', 'k'],
  ['l', 'l'], ['m', 'm'], ['n', 'n'], ['p', 'p'], ['q', 'k'], ['r', 'ɹ'], ['s', 's'], ['t', 't'],
  ['v', 'v'], ['w', 'w'], ['x', 'ks'], ['z', 'z'],
];
const VOWELISH = /[æɛɪɑʌiAOWYIuɔəɜ]/;
function byRule(w) {
  let s = w.toLowerCase().replace(/[^a-z]/g, '');
  // A silent e at the end makes the vowel before it long ("cake", "smile").
  let longV = null;
  const m = s.match(/([aeiou])[^aeiou]e$/);
  if (m && s.length > 3) { longV = m[1]; s = s.slice(0, -1); }
  let out = '', i = 0;
  while (i < s.length) {
    const r = RULES.find(([g]) => s.startsWith(g, i));
    if (!r) { i++; continue; }
    let ph = r[1];
    if (longV && r[0] === longV && i === s.length - 2) ph = { a: 'A', e: 'i', i: 'I', o: 'O', u: 'u' }[longV];
    if (r[0] === 'c' && /[eiy]/.test(s[i + 1] || '')) ph = 's';
    if (r[0] === 'g' && /[eiy]/.test(s[i + 1] || '') && i > 0) ph = 'ʤ';
    out += ph; i += r[0].length;
  }
  // Stress on the first vowel.
  const v = out.search(VOWELISH);
  return v >= 0 ? out.slice(0, v) + 'ˈ' + out.slice(v) : out;
}

const SHORT = new Set(['me', 'up', 'in', 'on', 'it', 'at', 'to', 'go', 'do', 'no', 'so', 'my', 'by', 'we', 'he', 'us', 'be', 'of', 'or', 'an', 'is', 'as', 'ok']);

// Little words said lightly, as in running speech ("ðə", not "ðˈi"): read
// at full stress, every "the" and "to" sounds like a list.
const WEAK = {
  a: 'ə', an: 'ən', the: 'ðə', to: 'tə', of: 'əv', and: 'ənd', or: 'ɔɹ', for: 'fɔɹ', from: 'fɹʌm',
  at: 'æt', in: 'ɪn', is: 'ɪz', are: 'ɑɹ', was: 'wʌz', were: 'wəɹ', be: 'bi', been: 'bɪn',
  as: 'æz', can: 'kæn', has: 'hæz', have: 'hæv', had: 'hæd', his: 'hɪz', her: 'hɜɹ', them: 'ðɛm',
  your: 'jʊɹ', you: 'ju', it: 'ɪt', its: 'ɪts', that: 'ðæt', with: 'wɪð', but: 'bʌt', by: 'bI',
  we: 'wi', he: 'hi', she: 'ʃi', they: 'ðA', me: 'mi', my: 'mI', do: 'du', does: 'dʌz', will: 'wɪl',
  would: 'wʊd', should: 'ʃʊd', could: 'kʊd', than: 'ðæn', so: 'sO', if: 'ɪf', into: 'ɪntu',
};

// A prefix on a word it has ("refactored": re + factored).
const PREFIX = ['re', 'un', 'pre', 'de', 'dis', 'mis', 'non', 'over', 'under', 'sub', 'auto', 'multi', 'inter'];
const PREFIX_PH = { re: 'ɹi', un: 'ʌn', pre: 'pɹi', de: 'di', dis: 'dɪs', mis: 'mɪs', non: 'nɑn', over: 'ˌOvəɹ', under: 'ˌʌndəɹ', sub: 'sʌb', auto: 'ˌɔtO', multi: 'ˌmʌlti', inter: 'ˌɪntəɹ' };

// Words CMUdict lists with the less common reading first ("read" as "red").
const PREFER = { read: 'ɹˈid', lead: 'lˈid', tear: 'tˈɪɹ', wind: 'wˈɪnd', bass: 'bˈAs', readme: 'ɹˈidmi', github: 'ɡˈɪthʌb', gitlab: 'ɡˈɪtlæb', npm: 'ˈɛn pˈi ˈɛm', json: 'ʤˈAsən', ui: 'jˈu ˈI', ux: 'jˈu ˈɛks' };

function word(w) {
  const lw = w.toLowerCase();
  if (PREFER[lw]) return PREFER[lw];
  if (WEAK[lw]) return WEAK[lw];
  return known(w) || fromPieces(lw) || byRule(lw);
}
// Looked up, built from a word it has with an ending or a prefix, or spelled
// out; null when none of those works.
function known(w, depth = 0) {
  const lw = w.toLowerCase();
  const hit = dict.get(lw);
  if (hit) return arpaToMisaki(hit);
  if (depth > 1) return null;
  // Initials: all capitals and short ("API", "CLI"), or no vowel at all ("npm").
  if ((/^[A-Z]{2,5}s?$/.test(w) && !dict.has(lw)) || !/[aeiouy]/.test(lw)) {
    const plural = /^[A-Z]{2,5}s$/.test(w);
    return spell(plural ? w.slice(0, -1) : w) + (plural ? 'z' : '');
  }
  for (const [end, kind, restore] of ENDINGS) {
    if (!lw.endsWith(end) || lw.length - end.length < 3) continue;
    const stem = lw.slice(0, -end.length);
    for (const add of restore) {
      const cand = stem + add, ph = dict.get(cand) || (stem.length > 3 && stem.at(-1) === stem.at(-2) && dict.get(stem.slice(0, -1)));
      if (ph) return endingAfter(arpaToMisaki(ph), kind);
    }
  }
  for (const p of PREFIX) {
    if (!lw.startsWith(p) || lw.length - p.length < 3) continue;
    const rest = known(lw.slice(p.length), depth + 1);
    if (rest) return PREFIX_PH[p] + rest;
  }
  return null;
}

// ---------- numbers, money, times ----------
const ONES = ['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten',
  'eleven', 'twelve', 'thirteen', 'fourteen', 'fifteen', 'sixteen', 'seventeen', 'eighteen', 'nineteen'];
const TENS = ['', '', 'twenty', 'thirty', 'forty', 'fifty', 'sixty', 'seventy', 'eighty', 'ninety'];
function under1000(n) {
  const h = Math.floor(n / 100), r = n % 100, out = [];
  if (h) out.push(ONES[h], 'hundred');
  if (r >= 20) out.push(TENS[Math.floor(r / 10)] + (r % 10 ? ' ' + ONES[r % 10] : ''));
  else if (r || !h) out.push(ONES[r]);
  return out.join(' ');
}
function numberWords(n) {
  if (n === 0) return 'zero';
  const scales = ['', 'thousand', 'million', 'billion', 'trillion'], out = [];
  let i = 0;
  while (n > 0 && i < scales.length) {
    const chunk = n % 1000;
    if (chunk) out.unshift(under1000(chunk) + (scales[i] ? ' ' + scales[i] : ''));
    n = Math.floor(n / 1000); i++;
  }
  return out.join(' ');
}
const ORD = { one: 'first', two: 'second', three: 'third', five: 'fifth', eight: 'eighth', nine: 'ninth', twelve: 'twelfth' };
function ordinal(n) {
  const w = numberWords(n).split(' '), last = w.pop();
  const o = ORD[last] || (last.endsWith('y') ? last.slice(0, -1) + 'ieth' : last + 'th');
  return [...w, o].join(' ');
}
function year(n) {
  if (n >= 2000 && n < 2010) return numberWords(n);
  const a = Math.floor(n / 100), b = n % 100;
  return numberWords(a) + ' ' + (b === 0 ? 'hundred' : b < 10 ? 'oh ' + ONES[b] : numberWords(b));
}
const digits = (s) => s.split('').map((d) => ONES[+d]).join(' ');

function expandNumbers(t) {
  return t
    .replace(/([$£€])(\d[\d,]*)(?:\.(\d{1,2}))?\b/g, (_, sym, a, c) => {
      const n = +a.replace(/,/g, ''), unit = { $: 'dollar', '£': 'pound', '€': 'euro' }[sym];
      let s = numberWords(n) + ' ' + unit + (n === 1 ? '' : 's');
      if (c) { const k = +c.padEnd(2, '0'); s += ' and ' + numberWords(k) + ' ' + (sym === '£' ? (k === 1 ? 'penny' : 'pence') : (k === 1 ? 'cent' : 'cents')); }
      return s;
    })
    .replace(/\b(\d{1,2}):(\d{2})\b/g, (_, h, m) => numberWords(+h) + (m === '00' ? " o'clock" : +m < 10 ? ' oh ' + ONES[+m] : ' ' + numberWords(+m)))
    .replace(/\b(\d+)(st|nd|rd|th)\b/gi, (_, n) => ordinal(+n))
    .replace(/\b(\d+)%/g, (_, n) => numberWords(+n) + ' percent')
    .replace(/(\bversion\s+)?\bv?(\d+(?:\.\d+){2,})\b/gi, (_, said, v) => (said || 'version ') + v.split('.').map((x) => numberWords(+x)).join(' point '))
    .replace(/\b(\d+)\.(\d+)\b/g, (_, a, b) => numberWords(+a) + ' point ' + digits(b))
    .replace(/\b(1[1-9]\d\d|20\d\d)s?\b/g, (m, y) => year(+y) + (m.endsWith('s') ? 's' : ''))
    .replace(/\b\d[\d,]*\b/g, (m) => { const n = +m.replace(/,/g, ''); return Number.isSafeInteger(n) && n < 1e15 ? numberWords(n) : digits(m.replace(/,/g, '')); });
}

// ---------- the text ----------
const SAY = { '&': ' and ', '+': ' plus ', '=': ' equals ', '@': ' at ', '%': ' percent ', '/': ' slash ' };
const KEEP = new Set([',', '.', '!', '?', ';', ':', '—', '…']);

export function phonemize(text) {
  if (!dict) throw new Error('loadG2P first');
  let t = String(text)
    .replace(/[‘’]/g, "'").replace(/[“”]/g, '"')
    .replace(/\bDr\.(?= [A-Z])/g, 'Doctor').replace(/\bMr\./g, 'Mister').replace(/\bMrs\./g, 'Missus').replace(/\bMs\./g, 'Miz')
    .replace(/\be\.g\./gi, 'for example').replace(/\bi\.e\./gi, 'that is').replace(/\betc\./gi, 'et cetera');
  t = expandNumbers(t);
  t = t.replace(/[&+=@%]/g, (c) => SAY[c]).replace(/(\w)\/(\w)/g, '$1 slash $2');
  // Code-ish words: camelCase, snake_case and kebab-case are their words.
  t = t.replace(/([a-z])([A-Z])/g, '$1 $2').replace(/[_]/g, ' ').replace(/(\w)-(\w)/g, '$1 $2');
  const out = [];
  for (const tok of t.match(/[A-Za-z]+(?:'[A-Za-z]+)?|\.\.\.|[,.!?;:—…]/g) || []) {
    if (tok === '...') { out.push('…'); continue; }
    if (KEEP.has(tok)) { out.push(tok); continue; }
    out.push(word(tok));
  }
  // Punctuation sits on the word before it, as Kokoro was trained.
  return out.join(' ').replace(/ ([,.!?;:…])/g, '$1').replace(/ +/g, ' ').trim();
}
