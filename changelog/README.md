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
markdown: the viewer renders these in about thirty lines, and the format is
chosen so nobody reaches for a table or an image and has it silently break on
a phone. Wrapped bullet continuations are indented and get folded back into
their bullet.

Write for someone deciding whether to upgrade now. Say what they get or what
stops hurting — not which function was touched.
