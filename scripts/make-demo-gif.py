#!/usr/bin/env python3
from PIL import Image, ImageDraw, ImageFont
import random, os

W, H = 1000, 560
BG      = (13, 17, 23)
PANEL   = (22, 27, 34)
BORDER  = (48, 54, 61)
TITLE   = (30, 36, 44)
DIM     = (128, 138, 148)
GREEN   = (63, 185, 80)
BLUE    = (88, 166, 255)
CYAN    = (86, 214, 214)     # session code — same color on host + laptop links them
WHITE   = (233, 240, 246)
FLASH   = (33, 58, 38)

FP = '/System/Library/Fonts/Menlo.ttc'
def F(sz): return ImageFont.truetype(FP, sz)
m12, m11, m10, s9, big = F(12), F(11), F(10), F(9), F(16)

HOST  = (26, 92, 448, 520)
PHONE = (474, 92, 662, 512)
TERM2 = (686, 92, 984, 384)

def rr(d, box, r, fill, outline=None, w=1):
    d.rounded_rectangle(box, radius=r, fill=fill, outline=outline, width=w)

def window(d, box, title, r=9):
    x0, y0, x1, y1 = box
    rr(d, box, r, PANEL, BORDER, 1)
    rr(d, (x0, y0, x1, y0+24), r, TITLE); d.rectangle((x0+1, y0+14, x1-1, y0+24), fill=TITLE)
    for i, c in enumerate([(237, 106, 94), (245, 191, 79), (98, 197, 84)]):
        d.ellipse((x0+11+i*15, y0+8, x0+19+i*15, y0+16), fill=c)
    d.text(((x0+x1)/2, y0+12), title, font=s9, fill=DIM, anchor='mm')
    return x0 + 14, y0 + 34

def seg(d, x, y, parts, font):
    for t, c in parts:
        d.text((x, y), t, font=font, fill=c); x += d.textlength(t, font=font)
    return x

def label(d, box, text):
    x0, y0, x1, _ = box
    d.text(((x0+x1)/2, y0-15), text, font=m10, fill=DIM, anchor='mm')

def qr_grid(n=25, seed=7):
    rnd = random.Random(seed)
    g = [[rnd.random() > 0.52 for _ in range(n)] for _ in range(n)]
    def finder(fx, fy):
        for y in range(-1, 8):
            for x in range(-1, 8):
                gx, gy = fx+x, fy+y
                if 0 <= gx < n and 0 <= gy < n:
                    g[gy][gx] = (0 <= x <= 6 and 0 <= y <= 6) and ((x in (0, 6) or y in (0, 6)) or (2 <= x <= 4 and 2 <= y <= 4))
    finder(0, 0); finder(n-7, 0); finder(0, n-7)
    return g
QR = qr_grid()

def draw_qr(d, ox, oy, ms=5, fg=(13, 17, 23)):
    n = len(QR)
    d.rectangle((ox-ms, oy-ms, ox+n*ms+ms, oy+n*ms+ms), fill=(240, 243, 246))
    for y in range(n):
        for x in range(n):
            if QR[y][x]:
                d.rectangle((ox+x*ms, oy+y*ms, ox+x*ms+ms-1, oy+y*ms+ms-1), fill=fg)

SESSION, PIN = 'K3F7QP2A', '481920'
CONNECT = 'reminal connect ' + SESSION + ' ' + PIN
START, END = '~/code', '~'

def pl(cwd, cmd=''):
    l = [('harshal ', GREEN), (cwd, BLUE), (' % ', DIM)]
    if cmd:
        l.append((cmd, WHITE))
    return l
OUT1 = [('Desktop  ', BLUE), ('Documents  ', BLUE), ('Code', BLUE)]
OUT2 = [('Downloads  ', BLUE), ('Photos  ', BLUE), ('todo.md', WHITE)]

def draw_shell(d, x, y, lines, font, cursor=False, lh=17, flash=False, fw=0):
    if flash and lines:
        d.rectangle((x-5, y-2, x+fw, y+len(lines)*lh-1), fill=FLASH)
    for i, line in enumerate(lines):
        cx = x
        for t, c in line:
            d.text((cx, y), t, font=font, fill=c); cx += d.textlength(t, font=font)
        if cursor and i == len(lines)-1:
            d.rectangle((cx+1, y+2, cx+7, y+font.size+2), fill=GREEN)
        y += lh
    return y

def render(st):
    img = Image.new('RGB', (W, H), BG); d = ImageDraw.Draw(img)
    x = seg(d, 26, 22, [('re', WHITE), ('minal', BLUE)], big)
    d.text((x+14, 26), 'share one terminal — join from any device, live', font=m10, fill=DIM)
    shell = st.get('shell'); scur = st.get('scur', False); fl = st.get('flash', False)

    # ---------- HOST ----------
    label(d, HOST, 'your terminal')
    ox, oy = window(d, HOST, 'host — zsh'); y = oy
    seg(d, ox, y, pl(START, 'reminal'[:st['host_typed']]), m12)
    if st['host_typed'] < 7 and st.get('host_cursor'):
        cx = ox + d.textlength('harshal ' + START + ' % ' + 'reminal'[:st['host_typed']], m12)
        d.rectangle((cx+1, y+2, cx+7, y+14), fill=GREEN)
    y += 22
    if st['share']:
        y += 4
        seg(d, ox, y, [('reminal', GREEN), ('  sharing this terminal · end-to-end encrypted', DIM)], m10); y += 19
        d.line((ox, y, HOST[2]-16, y), fill=BORDER, width=1); y += 8
        seg(d, ox, y, [('Session   ', DIM), (SESSION, CYAN)], m12); y += 18
        seg(d, ox, y, [('PIN       ', DIM), (PIN, WHITE)], m12); y += 21
        d.text((ox, y), 'Scan to join, or `reminal connect`:', font=m10, fill=DIM); y += 15
        qx, qy = ox+2, y+2; draw_qr(d, qx, qy)
        if st.get('scanning'):
            d.rounded_rectangle((qx-4, qy-4, qx+128, qy+128), radius=4, outline=GREEN, width=2)
            d.text((qx+134, qy+56), 'scan ▸', font=m10, fill=GREEN)
        if shell:
            draw_shell(d, ox, qy+140, shell, m11, scur, flash=fl, fw=HOST[2]-HOST[0]-24)

    # ---------- PHONE ----------
    label(d, PHONE, 'your phone · a browser tab')
    px0, py0, px1, py1 = PHONE
    rr(d, (px0, py0, px1, py1), 26, (5, 8, 12), (74, 82, 92), 2)
    rr(d, (px0+56, py0+11, px1-56, py0+20), 5, (28, 34, 42))
    scr = (px0+9, py0+30, px1-9, py1-16)
    ph = st.get('phone', 'cam')
    if ph in ('cam', 'scan'):
        rr(d, scr, 6, (9, 11, 15))
        d.text(((scr[0]+scr[2])/2, scr[1]+16), 'Camera', font=m10, fill=DIM, anchor='mm')
        cx0, cy0, cx1, cy1 = scr[0]+34, scr[1]+70, scr[2]-34, scr[1]+230
        col = GREEN if ph == 'scan' else (90, 98, 108)
        for (ax, ay, dx, dy) in [(cx0, cy0, 1, 1), (cx1, cy0, -1, 1), (cx0, cy1, 1, -1), (cx1, cy1, -1, -1)]:
            d.line((ax, ay, ax+dx*20, ay), fill=col, width=3); d.line((ax, ay, ax, ay+dy*20), fill=col, width=3)
        if ph == 'scan':
            draw_qr(d, (cx0+cx1)//2-42, (cy0+cy1)//2-42, ms=3, fg=(20, 24, 30))
            sy = cy0 + int((cy1-cy0) * st.get('sweep', 0.5))
            d.line((cx0+2, sy, cx1-2, sy), fill=(120, 255, 150), width=2)
            d.text(((scr[0]+scr[2])/2, cy1+22), 'Scanning QR…', font=m11, fill=GREEN, anchor='mm')
        else:
            d.text(((scr[0]+scr[2])/2, cy1+22), 'point at the QR', font=m10, fill=DIM, anchor='mm')
    else:
        rr(d, scr, 6, PANEL)
        hb = (scr[0], scr[1], scr[2], scr[1]+22); rr(d, hb, 6, TITLE); d.rectangle((hb[0], hb[1]+10, hb[2], hb[3]), fill=TITLE)
        seg(d, scr[0]+8, scr[1]+6, [('re', WHITE), ('minal', BLUE)], m11)
        d.ellipse((scr[2]-60, scr[1]+7, scr[2]-53, scr[1]+14), fill=GREEN)
        d.text((scr[2]-49, scr[1]+6), SESSION[:6], font=s9, fill=DIM)
        if shell:
            draw_shell(d, scr[0]+8, scr[1]+32, shell, m10, scur, lh=15, flash=fl, fw=scr[2]-scr[0]-14)

    # ---------- TERM2 ----------
    label(d, TERM2, 'another machine · reminal installed')
    ox2, oy2 = window(d, TERM2, 'laptop-2 — zsh'); y = oy2
    tt = st.get('term_typed', 0); typed = CONNECT[:tt]; bl = len('reminal connect ')
    x = seg(d, ox2, y, [('laptop-2 % ', DIM)], m11)
    # `reminal connect ` (white) · SESSION (cyan, matches host) · ` PIN` (white)
    p1 = typed[:bl]; d.text((x, y), p1, font=m11, fill=WHITE); x += d.textlength(p1, font=m11)
    p2 = typed[bl:bl+len(SESSION)]
    if p2: d.text((x, y), p2, font=m11, fill=CYAN); x += d.textlength(p2, font=m11)
    p3 = typed[bl+len(SESSION):]
    if p3: d.text((x, y), p3, font=m11, fill=WHITE); x += d.textlength(p3, font=m11)
    if tt < len(CONNECT):
        d.rectangle((x+1, y+2, x+7, y+13), fill=GREEN)
    y += 20
    if st.get('term_live'):
        seg(d, ox2, y, [('✓ connected — live', GREEN)], m11); y += 21
        if shell:
            draw_shell(d, ox2, y, shell, m11, scur, flash=fl, fw=TERM2[2]-TERM2[0]-24)

    if st.get('caption'):
        d.text((W/2, H-16), st['caption'], font=m12, fill=st.get('cap_color', DIM), anchor='mm')
    return img

frames, durs = [], []
def add(st, ms): frames.append(render({**base, **st})); durs.append(ms)
base = dict(host_typed=0, share=False, phone='cam', term_typed=0, shell=None, caption='')

# 1 — host runs `reminal`
for i in range(8): add(dict(host_typed=i, host_cursor=i % 2 == 0, caption='Run reminal in the terminal you want to share'), 70)
add(dict(host_typed=7), 300)
# 2 — join info appears
add(dict(host_typed=7, share=True, shell=[pl(START)], scur=True, caption='It prints a Session code, a PIN, and a QR'), 1100)
# 3 — two independent devices join
for k in range(6):
    add(dict(host_typed=7, share=True, scanning=True, phone='scan', sweep=(k % 4)/3, term_typed=min(len(CONNECT), 4+k*5),
             shell=[pl(START)], scur=True, caption='Phone scans the QR · the other machine runs `reminal connect`'), 210)
add(dict(host_typed=7, share=True, scanning=True, phone='scan', sweep=.5, term_typed=len(CONNECT),
         shell=[pl(START)], scur=True, caption='Phone scans the QR · the other machine runs `reminal connect`'), 500)
LIVE = dict(host_typed=7, share=True, phone='live', term_typed=len(CONNECT), term_live=True)
# 4 — both connected
add({**LIVE, 'shell': [pl(START)], 'scur': True, 'caption': 'Two unrelated devices — now on the same live shell'}, 1100)
# 5 — cd ~ echoed everywhere
c1 = 'cd ~'
for i in range(1, len(c1)+1):
    add({**LIVE, 'shell': [pl(START, c1[:i])], 'scur': True, 'caption': 'Type on any device…'}, 95)
for k in range(2):
    add({**LIVE, 'shell': [pl(START, c1), pl(END)], 'scur': True, 'flash': k == 0, 'caption': 'cd ~  → every screen updates'}, 260)
add({**LIVE, 'shell': [pl(START, c1), pl(END)], 'scur': True, 'caption': 'cd ~  → every screen updates'}, 650)
# 6 — ls echoed everywhere
c2 = 'ls'
for i in range(1, len(c2)+1):
    add({**LIVE, 'shell': [pl(START, c1), pl(END, c2[:i])], 'scur': True, 'caption': 'run ls…'}, 120)
done = [pl(START, c1), pl(END, c2), OUT1, OUT2, pl(END)]
for k in range(2):
    add({**LIVE, 'shell': done, 'scur': True, 'flash': k == 0, 'caption': '…and the output lands on all of them at once', 'cap_color': GREEN}, 300)
add({**LIVE, 'shell': done, 'scur': True, 'caption': '…and the output lands on all of them at once', 'cap_color': GREEN}, 1800)

out = '/tmp/reminal-demo.gif'
frames[0].save(out, save_all=True, append_images=frames[1:], duration=durs, loop=0, optimize=True, disposal=2)
print('frames', len(frames), 'total_ms', sum(durs), 'size', round(os.path.getsize(out)/1024), 'KB')
frames[11].save('/tmp/f_scan.png'); frames[len(frames)-1].save('/tmp/f_final.png')
print('saved')
