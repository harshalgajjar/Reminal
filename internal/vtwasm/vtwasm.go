// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

// Package vtwasm is a terminal emulator: libghostty-vt (MIT, from
// ghostty-org/ghostty), built for wasm32 (scripts/ghostty-vt/build.sh) and
// run under wazero, in pure Go. It keeps a session's screen and scrollback
// the way a terminal does, and a resize re-wraps the scrollback to the new
// width, as a terminal's own does: nothing written is lost to a narrower
// screen.
//
// Each Terminal is a wasm instance of its own (its own memory); the module
// is compiled once. A Terminal is safe for concurrent use: its calls are
// serialized.
package vtwasm

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

//go:embed ghostty-vt.wasm
var module []byte

var (
	compileOnce sync.Once
	runtime     wazero.Runtime
	compiled    wazero.CompiledModule
	compileErr  error
)

// compile builds the module once for every Terminal.
func compile() (wazero.Runtime, wazero.CompiledModule, error) {
	compileOnce.Do(func() {
		ctx := context.Background()
		runtime = wazero.NewRuntime(ctx)
		compiled, compileErr = runtime.CompileModule(ctx, module)
	})
	return runtime, compiled, compileErr
}

// libghostty-vt's C ABI, as its headers number it (include/ghostty/vt).
const (
	optScrollbackMaxBytes = 28 // GHOSTTY_TERMINAL_OPT_SCROLLBACK_MAX_BYTES
	optScrollbackMaxLines = 29 // GHOSTTY_TERMINAL_OPT_SCROLLBACK_MAX_LINES

	dataCols           = 1  // GHOSTTY_TERMINAL_DATA_COLS
	dataRows           = 2  // GHOSTTY_TERMINAL_DATA_ROWS
	dataCursorX        = 3  // GHOSTTY_TERMINAL_DATA_CURSOR_X
	dataCursorY        = 4  // GHOSTTY_TERMINAL_DATA_CURSOR_Y
	dataActiveScreen   = 6  // GHOSTTY_TERMINAL_DATA_ACTIVE_SCREEN (0 primary, 1 alternate)
	dataScrollbackRows = 15 // GHOSTTY_TERMINAL_DATA_SCROLLBACK_ROWS

	fmtOptEmit      = 1 // GHOSTTY_FORMATTER_OPT_EMIT
	fmtOptUnwrap    = 2 // GHOSTTY_FORMATTER_OPT_UNWRAP
	fmtOptTrim      = 3 // GHOSTTY_FORMATTER_OPT_TRIM
	fmtOptSelection = 4 // GHOSTTY_FORMATTER_OPT_SELECTION
	fmtOptExtras    = 5 // GHOSTTY_FORMATTER_OPT_EXTRAS

	emitPlain = 0 // GHOSTTY_FORMATTER_FORMAT_PLAIN
	emitVT    = 1 // GHOSTTY_FORMATTER_FORMAT_VT

	extraStyle     = 1 << 7 // GHOSTTY_FORMATTER_EXTRA_STYLE
	extraHyperlink = 1 << 8 // GHOSTTY_FORMATTER_EXTRA_HYPERLINK

	tagActive  = 0 // GHOSTTY_POINT_TAG_ACTIVE
	tagHistory = 3 // GHOSTTY_POINT_TAG_HISTORY

	sizeMax = 0xFFFFFFFF // SIZE_MAX: size_t is 32 bits on wasm32
)

// Terminal is one terminal: a screen, its scrollback, its cursor and modes.
type Terminal struct {
	// mu serializes calls into the instance, which two goroutines must
	// never be in at once, whatever lock its caller holds or forgets.
	mu  sync.Mutex
	ctx context.Context
	mod api.Module
	fns map[string]api.Function
	t   uint64 // GhosttyTerminal
	// err is why it can no longer be used (closed, or a call into it
	// trapped): every call after that does nothing and gives zero, never
	// a panic in the process it runs in.
	err error
}

// errClosed is a Terminal used after Close.
var errClosed = errors.New("vtwasm: closed")

// Err is why it can no longer be used, or nil.
func (v *Terminal) Err() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.err
}

// New is a terminal of cols by rows keeping at most maxLines of scrollback.
func New(cols, rows, maxLines int) (*Terminal, error) {
	rt, cm, err := compile()
	if err != nil {
		return nil, fmt.Errorf("vtwasm: compiling: %w", err)
	}
	ctx := context.Background()
	mod, err := rt.InstantiateModule(ctx, cm, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, fmt.Errorf("vtwasm: instantiating: %w", err)
	}
	v := &Terminal{ctx: ctx, mod: mod, fns: map[string]api.Function{}}
	slot := v.call("ghostty_wasm_alloc_opaque")
	if rc := v.call("ghostty_terminal_new", 0, slot, uint64(clamp16(cols)), uint64(clamp16(rows))); rc != 0 {
		mod.Close(ctx)
		return nil, fmt.Errorf("vtwasm: ghostty_terminal_new: %d", int32(rc))
	}
	v.t = v.call("ghostty_wasm_take_opaque", slot)
	// Lines, not bytes, bound the scrollback: libghostty's default byte limit
	// keeps a few hundred rows.
	v.setSize(optScrollbackMaxBytes, sizeMax)
	v.setSize(optScrollbackMaxLines, uint64(maxLines))
	return v, nil
}

// Close frees the terminal and its instance.
func (v *Terminal) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.mod == nil {
		return
	}
	if v.err == nil {
		v.call("ghostty_terminal_free", v.t)
	}
	v.mod.Close(v.ctx)
	v.mod, v.err = nil, errClosed
}

// Write feeds the bytes a program wrote to its terminal. Replies the
// terminal would send back (to a query) are not kept.
func (v *Terminal) Write(p []byte) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return 0, v.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	buf := v.alloc(len(p))
	v.mod.Memory().Write(buf, p)
	v.call("ghostty_terminal_vt_write", v.t, uint64(buf), uint64(len(p)))
	v.free(buf, len(p))
	if v.err != nil {
		return 0, v.err
	}
	return len(p), nil
}

// Resize makes it cols by rows, re-wrapping its scrollback to the width.
func (v *Terminal) Resize(cols, rows int) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return v.err
	}
	if rc := v.call("ghostty_terminal_resize", v.t, uint64(clamp16(cols)), uint64(clamp16(rows)), 0, 0); rc != 0 {
		return fmt.Errorf("vtwasm: resize: %d", int32(rc))
	}
	return nil
}

// SetMaxLines bounds its scrollback to n lines.
func (v *Terminal) SetMaxLines(n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.setSize(optScrollbackMaxLines, uint64(max(n, 0)))
}

// Reset is a full reset (RIS): screen, scrollback and modes, its size kept.
func (v *Terminal) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.call("ghostty_terminal_reset", v.t)
}

// Size is its width and height in cells.
func (v *Terminal) Size() (cols, rows int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.size()
}

func (v *Terminal) size() (cols, rows int) {
	return int(v.get16(dataCols)), int(v.get16(dataRows))
}

// Cursor is the cursor's cell on the screen, from the top left, 0-based.
func (v *Terminal) Cursor() (x, y int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return int(v.get16(dataCursorX)), int(v.get16(dataCursorY))
}

// AltScreen says a full-screen program has the alternate screen up.
func (v *Terminal) AltScreen() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.get32(dataActiveScreen) != 0
}

// ScrollbackRows is how many rows have scrolled off the top of the screen.
func (v *Terminal) ScrollbackRows() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return int(v.get32(dataScrollbackRows))
}

// History is its scrollback as the program wrote it: one line each, the
// rows a long line wrapped onto joined again, with its colours and styles
// as VT sequences. A terminal of any width wraps them anew.
func (v *Terminal) History() (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return "", v.err
	}
	n := int(v.get32(dataScrollbackRows))
	if n == 0 {
		return "", nil
	}
	cols, _ := v.size()
	return v.format(emitVT, true, &area{tagHistory, 0, 0, cols - 1, n - 1})
}

// Screen is the screen as VT sequences, one row a line, as wide as it is.
func (v *Terminal) Screen() (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	cols, rows := v.size()
	return v.format(emitVT, false, &area{tagActive, 0, 0, cols - 1, rows - 1})
}

// Contents is everything it holds, scrollback then screen, as the program
// wrote it: one line each, the rows a long line wrapped onto joined again,
// with its styles as VT sequences. Painted with autowrap on, a terminal of
// any width lays it out as its own; blank rows at the end are left off.
func (v *Terminal) Contents() (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.format(emitVT, true, nil)
}

// Text is everything it holds, scrollback and screen, as plain text, the
// rows a long line wrapped onto joined again.
func (v *Terminal) Text() (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.format(emitPlain, true, nil)
}

// area is a span of cells in one of libghostty's coordinate systems.
type area struct {
	tag            uint32
	x0, y0, x1, y1 int
}

// format formats the terminal, or only a, as emit, unwrapping long lines or
// not.
func (v *Terminal) format(emit uint32, unwrap bool, a *area) (string, error) {
	if v.err != nil {
		return "", v.err
	}
	slot := v.call("ghostty_wasm_alloc_opaque")
	if rc := v.call("ghostty_formatter_terminal_new", 0, slot, v.t); rc != 0 {
		return "", fmt.Errorf("vtwasm: formatter: %d", int32(rc))
	}
	f := v.call("ghostty_wasm_take_opaque", slot)
	defer v.call("ghostty_formatter_free", f)
	val := v.alloc(32)
	defer v.free(val, 32)
	set := func(opt uint64, word uint32) error {
		v.mod.Memory().WriteUint32Le(val, word)
		if rc := v.call("ghostty_formatter_set", f, opt, uint64(val)); rc != 0 {
			return fmt.Errorf("vtwasm: formatter option %d: %d", opt, int32(rc))
		}
		return nil
	}
	if err := set(fmtOptEmit, emit); err != nil {
		return "", err
	}
	if err := set(fmtOptUnwrap, b2u(unwrap)); err != nil {
		return "", err
	}
	if emit == emitVT {
		// Styles and links only: no palette, modes or cursor, which belong
		// to the terminal the text is painted into.
		if err := set(fmtOptExtras, extraStyle|extraHyperlink); err != nil {
			return "", err
		}
		// Blank rows at the end are kept: a viewer painting what scrolled
		// off and then the screen below it puts the screen where it was
		// only with them.
		if err := set(fmtOptTrim, 0); err != nil {
			return "", err
		}
	}
	if a != nil {
		sel, err := v.selection(*a)
		if err != nil {
			return "", err
		}
		defer v.free(sel, 20)
		if rc := v.call("ghostty_formatter_set", f, fmtOptSelection, uint64(sel)); rc != 0 {
			return "", fmt.Errorf("vtwasm: selection: %d", int32(rc))
		}
	}
	out := v.alloc(8)
	defer v.free(out, 8)
	if rc := v.call("ghostty_formatter_format_alloc", f, 0, uint64(out), uint64(out+4)); rc != 0 {
		return "", fmt.Errorf("vtwasm: format: %d", int32(rc))
	}
	ptr, _ := v.mod.Memory().ReadUint32Le(out)
	n, _ := v.mod.Memory().ReadUint32Le(out + 4)
	b, ok := v.mod.Memory().Read(ptr, n)
	if !ok {
		return "", errors.New("vtwasm: format: out of range")
	}
	s := string(b)
	v.call("ghostty_free", 0, uint64(ptr), uint64(n))
	if emit == emitVT {
		s = stepsToSpaces(s)
	}
	return s, nil
}

// selection is a GhosttySelection (start and end GhosttyGridRef, 8 bytes
// each on wasm32, then a bool) over a, in wasm memory the caller frees (20
// bytes).
func (v *Terminal) selection(a area) (uint32, error) {
	sel := v.alloc(20)
	mem := v.mod.Memory()
	for i, xy := range [][2]int{{a.x0, a.y0}, {a.x1, a.y1}} {
		// A GhosttyPoint: a tag, then a union padded to two uint64s (so
		// 8-aligned) whose {uint16 x; uint32 y} starts it: 24 bytes, passed by
		// its address on wasm32.
		pt := v.alloc(24)
		mem.WriteUint32Le(pt, a.tag)
		mem.WriteUint16Le(pt+8, uint16(xy[0]))
		mem.WriteUint32Le(pt+12, uint32(xy[1]))
		rc := v.call("ghostty_terminal_grid_ref", v.t, uint64(pt), uint64(sel+uint32(i)*8))
		v.free(pt, 24)
		if rc != 0 {
			v.free(sel, 20)
			return 0, fmt.Errorf("vtwasm: grid ref (%d,%d): %d", xy[0], xy[1], int32(rc))
		}
	}
	mem.WriteUint32Le(sel+16, 0) // not a rectangle
	return sel, nil
}

func (v *Terminal) call(name string, args ...uint64) uint64 {
	if v.err != nil {
		return 0
	}
	f, ok := v.fns[name]
	if !ok {
		if f = v.mod.ExportedFunction(name); f == nil {
			v.err = errors.New("vtwasm: no export " + name)
			return 0
		}
		v.fns[name] = f
	}
	r, err := f.Call(v.ctx, args...)
	if err != nil {
		v.err = fmt.Errorf("vtwasm: %s: %w", name, err)
		return 0
	}
	if len(r) == 0 {
		return 0
	}
	return r[0]
}

func (v *Terminal) alloc(n int) uint32 { return uint32(v.call("ghostty_wasm_alloc", uint64(n))) }

func (v *Terminal) free(p uint32, n int) { v.call("ghostty_wasm_free", uint64(p), uint64(n)) }

func (v *Terminal) setSize(opt int, val uint64) {
	if v.err != nil {
		return
	}
	p := v.alloc(4)
	v.mod.Memory().WriteUint32Le(p, uint32(val))
	v.call("ghostty_terminal_set", v.t, uint64(opt), uint64(p))
	v.free(p, 4)
}

// get32 and get16 read a uint32 or uint16 of the terminal's data.
func (v *Terminal) get32(data int) uint32 {
	if v.err != nil {
		return 0
	}
	p := v.alloc(8)
	defer v.free(p, 8)
	v.mod.Memory().WriteUint64Le(p, 0)
	v.call("ghostty_terminal_get", v.t, uint64(data), uint64(p))
	x, _ := v.mod.Memory().ReadUint32Le(p)
	return x
}

func (v *Terminal) get16(data int) uint16 {
	if v.err != nil {
		return 0
	}
	p := v.alloc(8)
	defer v.free(p, 8)
	v.mod.Memory().WriteUint64Le(p, 0)
	v.call("ghostty_terminal_get", v.t, uint64(data), uint64(p))
	x, _ := v.mod.Memory().ReadUint16Le(p)
	return x
}

func clamp16(n int) int { return min(max(n, 1), 0xFFFF) }

func b2u(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}
