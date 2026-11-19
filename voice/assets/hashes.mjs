// Writes internal/client/web/voice/hashes.js from manifest.json: the size and
// SHA-256 the voice frame checks every file from /voice-assets/ against. Run
// it after changing the manifest (node voice/assets/hashes.mjs); a Go test
// keeps the two the same.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const manifest = JSON.parse(fs.readFileSync(path.join(here, 'manifest.json'), 'utf8'));
let out = `// The SHA-256 and size of every file the voice frame loads from
// /voice-assets/, by its path there: what integrity.js checks each against.
// Written by voice/assets/hashes.mjs from voice/assets/manifest.json; a test
// keeps the two the same.
export const PINNED = {\n`;
for (const f of manifest) out += `  ${JSON.stringify(f.path)}: ['${f.sha256}', ${f.size}],\n`;
out += '};\n';
fs.writeFileSync(path.join(here, '..', '..', 'internal', 'client', 'web', 'voice', 'hashes.js'), out);
console.log(`${manifest.length} files`);
