"""Read the QR drawn in a reminal text output and decode it with zbar.

usage: decode.py <file>  -> prints "<style> <decoded text>", exit 1 if none.
ASCII style: "##" per dark module. Half-block style (terminal): each glyph is
two stacked modules, the drawn part light (a light-on-dark terminal).
"""
import re, sys
from PIL import Image
from pyzbar.pyzbar import decode

data = open(sys.argv[1], 'rb').read()
# Windows PowerShell 5.1 writes UTF-16LE with a BOM; everything else is bytes.
raw = data.decode('utf-16') if data[:2] == b'\xff\xfe' else data.decode('utf-8', 'replace')
if '\x1b' in raw:
    # A terminal capture (script, ConPTY) repaints with cursor moves and
    # erases: play it on an emulated screen and read what a person would see.
    import pyte
    screen = pyte.Screen(200, 400)
    pyte.Stream(screen).feed(raw.replace('\r\n', '\n').replace('\n', '\r\n'))
    lines = [l.rstrip() for l in screen.display]
else:
    lines = raw.replace('\r', '').split('\n')
half = [l for l in lines if l.strip() and set(l) <= set('▀▄█ ') and set(l) & set('▀▄█')]
asc = [l for l in lines if '##' in l and set(l) <= set('# ')]
if asc:
    style, w = 'ascii', max(len(l) for l in asc) // 2
    px = [[l.ljust(w * 2)[2 * x] == '#' for x in range(w)] for l in asc]   # True = dark
elif half:
    style, w = 'halfblock', max(len(l) for l in half)
    px = []
    for l in half:
        l = l.ljust(w)
        px.append([c not in '█▀' for c in l])   # top: drawn = light
        px.append([c not in '█▄' for c in l])   # bottom
else:
    print('no QR found'); sys.exit(1)
S, Q = 8, 4
img = Image.new('L', ((w + 2 * Q) * S, (len(px) + 2 * Q) * S), 255)
for y, row in enumerate(px):
    for x, dark in enumerate(row):
        if dark:
            img.paste(0, ((x + Q) * S, (y + Q) * S, (x + Q + 1) * S, (y + Q + 1) * S))
res = decode(img)
if not res:
    print(style, 'UNDECODABLE'); sys.exit(1)
print(style, res[0].data.decode())
