# Release notes

One file per release, named for its version: `3.5.8.md`, `3.6.0.md`. Add the
file in the same commit that earns it, not at tag time — a changelog written
from `git log` a week later is a list of commit subjects, not release notes.

The release workflow publishes `changelog/<version>.md` as the GitHub release
body. A tag with no matching file falls back to the release commit message, so
this is additive: nothing breaks if a file is missing.

That body is also how a host on an older version reads about a newer one.
Notes cannot ship inside the binary — a machine on 3.5.4 has no 3.5.6 file —
so the Host panel fetches release bodies over the same API the update check
already uses. These files are the source; the release body is the transport.

## Format

    # 3.5.6

    - What changed, in a sentence someone can act on.
    - A second thing.

    Fixes
    - What was broken, stated as the symptom rather than the patch.

A heading, bullets, and optional plain-line subheadings. Deliberately not full
markdown: the viewer renders these in about thirty lines (`renderNotes` in
`cloudflare/public/index.html`), and the format is chosen so nobody reaches for
a table or an image and has it silently break on a phone. Wrapped bullet
continuations are indented and get folded back into their bullet.

Two traps, because the in-app "What's new" panel is stricter than GitHub and
most people read the notes there, not on GitHub:

- **No inline markdown except `` `code` ``.** `**bold**`, `*italic*`, and
  `[links](…)` are NOT parsed — they render as literal `**`/`*`/`[]` characters
  in the panel. Write plain text; backticks for code are the only markup.
- **Every non-bullet, non-heading line is a subheading, and subheadings are
  UPPERCASED by CSS.** So a line like `Fixed` becomes a clean `FIXED` label, but
  a full prose sentence becomes a shouting all-caps paragraph. Don't write intro
  sentences or paragraphs — lead with short section labels (`Improved`, `Fixed`,
  `Added`) and put everything else in bullets.

The GitHub release body is copied verbatim from this file at tag time; if you
must edit a release's notes after it is published, `gh release edit vX.Y.Z
--notes-file changelog/X.Y.Z.md` (and commit the file too).

Write for someone deciding whether to upgrade now — and assume they are not
an engineer. Say what they can now do, or what stops hurting, in everyday
words. No internals: no "cache", "feed", "protocol", "handler", no function
names. A fix names the symptom they felt and says it is gone, not how.

The notes should make a reader want to press Upgrade. Read them back as that
person: if a bullet would not make you curious to try it, rewrite it. Short
bullets, one idea each, a warm and confident tone.
