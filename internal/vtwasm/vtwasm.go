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
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

//go:embed ghostty-vt.wasm
var module []byte

var (
	compileOnce sync.Once
	wasmRuntime wazero.Runtime
	compiled    wazero.CompiledModule
	compileErr  error
)

// compile builds the module once for every Terminal. A call into an instance
// is bounded in time (callLimit): past it, the instance is closed and the
// call gives an error, so a module that spins never holds its caller.
func compile() (wazero.Runtime, wazero.CompiledModule, error) {
	compileOnce.Do(func() {
		ctx := context.Background()
		wasmRuntime = wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
		compiled, compileErr = wasmRuntime.CompileModule(ctx, module)
	})
	return wasmRuntime, compiled, compileErr
}

// callLimit bounds one call into the instance: a write of what a program
// wrote, a resize, a read of one value. formatLimit bounds one into the
// formatter, which reads the whole scrollback. Variables for the tests.
var (
	callLimit   = 2 * time.Second
	formatLimit = 10 * time.Second
)

// ScrollbackBytes is the most memory a terminal's scrollback takes: at 200
// columns some 12,000 rows, at 80 some 30,000. A wasm instance never gives
// memory back, so what the scrollback reached is what the session keeps.
var ScrollbackBytes uint64 = 32 << 20

// errAlloc is the instance refusing memory: the call it was for is not made.
var errAlloc = errors.New("vtwasm: out of memory")

// errRange is a read or write outside the instance's memory.
var errRange = errors.New("vtwasm: out of range")

// libghostty-vt's C ABI, as its headers number it (include/ghostty/vt).
const (
	optScrollbackMaxBytes = 28 // GHOSTTY_TERMINAL_OPT_SCROLLBACK_MAX_BYTES
	optScrollbackMaxLines = 29 // GHOSTTY_TERMINAL_OPT_SCROLLBACK_MAX_LINES

	dataCols           = 1  // GHOSTTY_TERMINAL_DATA_COLS
	dataRows           = 2  // GHOSTTY_TERMINAL_DATA_ROWS
	dataCursorX        = 3  // GHOSTTY_TERMINAL_DATA_CURSOR_X
	dataCursorY        = 4  // GHOSTTY_TERMINAL_DATA_CURSOR_Y
	dataActiveScreen   = 6  // GHOSTTY_TERMINAL_DATA_ACTIVE_SCREEN (0 primary, 1 alternate)
	dataTotalRows      = 14 // GHOSTTY_TERMINAL_DATA_TOTAL_ROWS
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
	tagScreen  = 2 // GHOSTTY_POINT_TAG_SCREEN
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
	// limit bounds each call of the operation under way (callLimit when
	// zero; formatLimit while formatting).
	limit time.Duration
	// err is why it can no longer be used (closed, or a call into it
	// trapped): every call after that does nothing and gives zero, never
	// a panic in the process it runs in.
	err error
	// scratch is memory of the instance's, taken once, for the small values
	// passed by address (scratch*): a call into the instance costs as much as
	// a small read, and taking and freeing memory around each read made one
	// read three.
	scratch uint32
	// in is memory of the instance's kept for what a program writes (inCap
	// bytes; zero, none yet): the writes of a session come one after another,
	// most of them small.
	in    uint32
	inCap int
	// data is the terminal's data (get) as read since it last changed, the
	// data numbers in have (a bit each): what the session reads after every
	// write (its scrollback, the alternate screen) and every format (its
	// size) is read from the instance once.
	data [16]uint64
	have uint32
	// mark is a GhosttyTrackedGridRef (Mark; zero, none yet).
	mark uint32
	// fmts are the formatters made, by kind (formatter).
	fmts map[fmtKind]uint64
}

// scratch's layout: the value an option or a read is passed in, the pointer
// and length a format gives back, a selection, a point.
const (
	scratchVal   = 0  // 8 bytes
	scratchOut   = 8  // 8: pointer, length
	scratchSel   = 16 // 20: GhosttySelection
	scratchPoint = 40 // 24, 8-aligned: GhosttyPoint
	scratchSize  = 64
)

// inKeep is the most memory kept for writes: a larger one (a restored
// history) takes its own and gives it back.
const inKeep = 64 << 10

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
	// Its memory is reserved whole and committed as it grows (mem.go).
	ctx := experimental.WithMemoryAllocator(context.Background(), experimental.MemoryAllocatorFunc(allocate))
	mod, err := rt.InstantiateModule(ctx, cm, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, fmt.Errorf("vtwasm: instantiating: %w", err)
	}
	v := &Terminal{ctx: ctx, mod: mod, fns: map[string]api.Function{}, fmts: map[fmtKind]uint64{}}
	if v.scratch, err = v.alloc(scratchSize); err != nil {
		mod.Close(ctx)
		return nil, fmt.Errorf("vtwasm: no memory for the terminal: %w", err)
	}
	slot := v.call("ghostty_wasm_alloc_opaque")
	if slot == 0 || v.err != nil {
		mod.Close(ctx)
		return nil, fmt.Errorf("vtwasm: no memory for the terminal: %v", v.err)
	}
	if rc := v.call("ghostty_terminal_new", 0, slot, uint64(clamp16(cols)), uint64(clamp16(rows))); rc != 0 || v.err != nil {
		mod.Close(ctx)
		return nil, fmt.Errorf("vtwasm: ghostty_terminal_new: %d %v", int32(rc), v.err)
	}
	v.t = v.call("ghostty_wasm_take_opaque", slot)
	// Lines bound the scrollback: libghostty's default byte limit keeps a
	// few hundred rows. Its bytes are bounded too, by ScrollbackBytes (and
	// never past half the instance's memory, mem.go): past that it prunes
	// its oldest pages itself, so the session's memory stays near the budget
	// whatever its width, and formatting the rest always has room.
	err = errors.Join(v.setSize(optScrollbackMaxBytes, min(sizeMax, ScrollbackBytes, memoryMax/2)), v.setSize(optScrollbackMaxLines, uint64(maxLines)))
	if err == nil {
		err = v.err
	}
	if err != nil {
		// A terminal whose set-up failed is not handed out.
		mod.Close(ctx)
		return nil, fmt.Errorf("vtwasm: setting up: %w", err)
	}
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
		for _, f := range v.fmts {
			v.call("ghostty_formatter_free", f)
		}
		if v.mark != 0 {
			v.call("ghostty_tracked_grid_ref_free", uint64(v.mark))
		}
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
	buf, err := v.inBuf(len(p))
	if err != nil {
		return 0, err
	}
	if buf != v.in {
		defer v.free(buf, len(p))
	}
	if err := v.put(buf, p); err != nil {
		return 0, err
	}
	v.changed()
	v.call("ghostty_terminal_vt_write", v.t, uint64(buf), uint64(len(p)))
	if v.err != nil {
		return 0, v.err
	}
	return len(p), nil
}

// inBuf is memory for a write of n bytes: the kept memory (in), taken or
// grown to fit when n is within inKeep, else memory of its own the caller
// frees.
func (v *Terminal) inBuf(n int) (uint32, error) {
	if n > inKeep {
		return v.alloc(n)
	}
	if n > v.inCap {
		v.free(v.in, v.inCap)
		v.in, v.inCap = 0, 0
		p, err := v.alloc(max(n, 4<<10))
		if err != nil {
			return 0, err
		}
		v.in, v.inCap = p, max(n, 4<<10)
	}
	return v.in, nil
}

// changed forgets the data read (data): the terminal is about to change.
func (v *Terminal) changed() { v.have = 0 }

// Resize makes it cols by rows, re-wrapping its scrollback to the width.
func (v *Terminal) Resize(cols, rows int) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return v.err
	}
	v.changed()
	if rc := v.call("ghostty_terminal_resize", v.t, uint64(clamp16(cols)), uint64(clamp16(rows)), 0, 0); rc != 0 {
		return fmt.Errorf("vtwasm: resize: %d", int32(rc))
	}
	return nil
}

// SetMaxLines bounds its scrollback to n lines.
func (v *Terminal) SetMaxLines(n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.changed()
	_ = v.setSize(optScrollbackMaxLines, uint64(max(n, 0)))
}

// Reset is a full reset (RIS): screen, scrollback and modes, its size kept.
func (v *Terminal) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.changed()
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

// HistoryRows is History for its rows y0 to y1 (0 the oldest, inclusive):
// the lines they hold, the first one partly when y0 continues a row before
// it. Reading only the rows that changed is how a count of the history's
// lines is kept without formatting all of it.
func (v *Terminal) HistoryRows(y0, y1 int) (string, error) {
	return v.rows(tagHistory, dataScrollbackRows, y0, y1)
}

// Rows is HistoryRows for the rows of scrollback and screen together (0 the
// oldest), where a mark is (Marked).
func (v *Terminal) Rows(y0, y1 int) (string, error) {
	return v.rows(tagScreen, dataTotalRows, y0, y1)
}

// rows formats rows y0 to y1 of tag's rows, of which there are data's count.
func (v *Terminal) rows(tag uint32, data, y0, y1 int) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return "", v.err
	}
	n := int(v.get32(data))
	y0, y1 = max(y0, 0), min(y1, n-1)
	if n == 0 || y0 > y1 {
		return "", nil
	}
	cols, _ := v.size()
	return v.format(emitVT, true, &area{tag, 0, y0, cols - 1, y1})
}

// Mark keeps the screen's top left cell, and follows it through what the
// terminal does from then on, a resize's re-wrap among them (Marked): where
// the screen's first row went is where the history gained or lost rows.
func (v *Terminal) Mark() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return v.err
	}
	pt, err := v.point(tagActive, 0, 0)
	if err != nil {
		return err
	}
	if v.mark == 0 {
		out := v.scratch + scratchOut
		if rc := v.call("ghostty_terminal_grid_ref_track", v.t, uint64(pt), uint64(out)); rc != 0 || v.err != nil {
			return fmt.Errorf("vtwasm: mark: %d %v", int32(rc), v.err)
		}
		mark, ok := v.mod.Memory().ReadUint32Le(out)
		if !ok {
			return errRange
		}
		v.mark = mark
		return nil
	}
	if rc := v.call("ghostty_tracked_grid_ref_set", uint64(v.mark), v.t, uint64(pt)); rc != 0 || v.err != nil {
		return fmt.Errorf("vtwasm: mark: %d %v", int32(rc), v.err)
	}
	return nil
}

// Marked is where the cell Mark kept is now, in the rows of scrollback and
// screen together (0 the oldest); false when there is none, or its row was
// let go.
func (v *Terminal) Marked() (x, y int, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.mark == 0 || v.err != nil {
		return 0, 0, false
	}
	// A GhosttyPointCoordinate: {uint16 x; uint32 y}, 8 bytes.
	out := v.scratch + scratchOut
	if rc := v.call("ghostty_tracked_grid_ref_point", uint64(v.mark), tagScreen, uint64(out)); rc != 0 || v.err != nil {
		return 0, 0, false
	}
	mem := v.mod.Memory()
	px, ok1 := mem.ReadUint16Le(out)
	py, ok2 := mem.ReadUint32Le(out + 4)
	return int(px), int(py), ok1 && ok2
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
	v.limit = formatLimit
	defer func() { v.limit = 0 }()
	f, err := v.formatter(fmtKind{emit, unwrap})
	if err != nil {
		return "", err
	}
	// The selection is set on every format, or cleared for all of it: one
	// set before is no longer valid once the terminal has changed.
	var sel uint32
	if a != nil {
		if sel, err = v.selection(*a); err != nil {
			return "", err
		}
	}
	if rc := v.call("ghostty_formatter_set", f, fmtOptSelection, uint64(sel)); rc != 0 || v.err != nil {
		return "", fmt.Errorf("vtwasm: selection: %d %v", int32(rc), v.err)
	}
	out := v.scratch + scratchOut
	if rc := v.call("ghostty_formatter_format_alloc", f, 0, uint64(out), uint64(out+4)); rc != 0 || v.err != nil {
		return "", fmt.Errorf("vtwasm: format: %d %v", int32(rc), v.err)
	}
	ptr, ok1 := v.mod.Memory().ReadUint32Le(out)
	n, ok2 := v.mod.Memory().ReadUint32Le(out + 4)
	if !ok1 || !ok2 {
		return "", errRange
	}
	b, ok := v.mod.Memory().Read(ptr, n)
	if !ok {
		v.call("ghostty_free", 0, uint64(ptr), uint64(n)) // the buffer is still the module's to free
		return "", errRange
	}
	s := string(b)
	v.call("ghostty_free", 0, uint64(ptr), uint64(n))
	if emit == emitVT {
		s = stepsToSpaces(s)
	}
	return s, nil
}

// fmtKind is what a formatter emits, and whether it unwraps long lines.
type fmtKind struct {
	emit   uint32
	unwrap bool
}

// formatter is the formatter of kind k, made the first time (fmts): its
// options stay set, and making one and setting them was as many calls into
// the instance as the format itself.
func (v *Terminal) formatter(k fmtKind) (uint64, error) {
	if f, ok := v.fmts[k]; ok {
		return f, nil
	}
	slot := v.call("ghostty_wasm_alloc_opaque")
	if slot == 0 || v.err != nil {
		return 0, errors.Join(errAlloc, v.err)
	}
	if rc := v.call("ghostty_formatter_terminal_new", 0, slot, v.t); rc != 0 || v.err != nil {
		return 0, fmt.Errorf("vtwasm: formatter: %d %v", int32(rc), v.err)
	}
	f := v.call("ghostty_wasm_take_opaque", slot)
	val := v.scratch + scratchVal
	set := func(opt uint64, word uint32) error {
		if err := v.put32(val, word); err != nil {
			return err
		}
		if rc := v.call("ghostty_formatter_set", f, opt, uint64(val)); rc != 0 {
			return fmt.Errorf("vtwasm: formatter option %d: %d", opt, int32(rc))
		}
		return nil
	}
	opts := [][2]uint32{{fmtOptEmit, k.emit}, {fmtOptUnwrap, b2u(k.unwrap)}}
	if k.emit == emitVT {
		// Styles and links only: no palette, modes or cursor, which belong
		// to the terminal the text is painted into. Blank rows at the end
		// are kept: a viewer painting what scrolled off and then the screen
		// below it puts the screen where it was only with them.
		opts = append(opts, [2]uint32{fmtOptExtras, extraStyle | extraHyperlink}, [2]uint32{fmtOptTrim, 0})
	}
	for _, o := range opts {
		if err := set(uint64(o[0]), o[1]); err != nil {
			v.call("ghostty_formatter_free", f)
			return 0, err
		}
	}
	v.fmts[k] = f
	return f, nil
}

// selection is a GhosttySelection (start and end GhosttyGridRef, 8 bytes
// each on wasm32, then a bool) over a, in scratch, read by the format it is
// for.
func (v *Terminal) selection(a area) (uint32, error) {
	sel := v.scratch + scratchSel
	for i, xy := range [][2]int{{a.x0, a.y0}, {a.x1, a.y1}} {
		pt, err := v.point(a.tag, xy[0], xy[1])
		if err != nil {
			return 0, err
		}
		if rc := v.call("ghostty_terminal_grid_ref", v.t, uint64(pt), uint64(sel+uint32(i)*8)); rc != 0 || v.err != nil {
			return 0, fmt.Errorf("vtwasm: grid ref (%d,%d): %d", xy[0], xy[1], int32(rc))
		}
	}
	if !v.mod.Memory().WriteUint32Le(sel+16, 0) { // not a rectangle
		return 0, errRange
	}
	return sel, nil
}

// point is a GhosttyPoint in scratch: a tag, then a union padded to two
// uint64s (so 8-aligned) whose {uint16 x; uint32 y} starts it, 24 bytes,
// passed by its address on wasm32.
func (v *Terminal) point(tag uint32, x, y int) (uint32, error) {
	pt, mem := v.scratch+scratchPoint, v.mod.Memory()
	if !mem.WriteUint32Le(pt, tag) || !mem.WriteUint16Le(pt+8, uint16(x)) || !mem.WriteUint32Le(pt+12, uint32(y)) {
		return 0, errRange
	}
	return pt, nil
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
	limit := v.limit
	if limit == 0 {
		limit = callLimit
	}
	ctx, cancel := context.WithTimeout(v.ctx, limit)
	defer cancel()
	r, err := f.Call(ctx, args...)
	if err != nil {
		v.err = fmt.Errorf("vtwasm: %s: %w", name, err)
		return 0
	}
	if len(r) == 0 {
		return 0
	}
	return r[0]
}

// alloc is n bytes of the instance's memory, for the caller to free; an
// error, and no memory, when the instance refuses them.
func (v *Terminal) alloc(n int) (uint32, error) {
	p := uint32(v.call("ghostty_wasm_alloc", uint64(n)))
	if v.err != nil {
		return 0, v.err
	}
	if p == 0 {
		return 0, errAlloc
	}
	return p, nil
}

func (v *Terminal) free(p uint32, n int) {
	if p != 0 {
		v.call("ghostty_wasm_free", uint64(p), uint64(n))
	}
}

// put writes b at p in the instance's memory.
func (v *Terminal) put(p uint32, b []byte) error {
	if !v.mod.Memory().Write(p, b) {
		return errRange
	}
	return nil
}

func (v *Terminal) put32(p uint32, x uint32) error {
	if !v.mod.Memory().WriteUint32Le(p, x) {
		return errRange
	}
	return nil
}

func (v *Terminal) setSize(opt int, val uint64) error {
	if v.err != nil {
		return v.err
	}
	p := v.scratch + scratchVal
	if err := v.put32(p, uint32(val)); err != nil {
		return err
	}
	v.call("ghostty_terminal_set", v.t, uint64(opt), uint64(p))
	return v.err
}

// get reads 8 bytes of the terminal's data and gives them, from the instance
// once while it has not changed (data); zero when it cannot.
func (v *Terminal) get(data int) uint64 {
	if v.err != nil {
		return 0
	}
	if v.have&(1<<data) != 0 {
		return v.data[data]
	}
	p := v.scratch + scratchVal
	if !v.mod.Memory().WriteUint64Le(p, 0) {
		return 0
	}
	v.call("ghostty_terminal_get", v.t, uint64(data), uint64(p))
	x, ok := v.mod.Memory().ReadUint64Le(p)
	if !ok || v.err != nil {
		return 0
	}
	v.data[data], v.have = x, v.have|1<<data
	return x
}

// get32 and get16 read a uint32 or uint16 of the terminal's data.
func (v *Terminal) get32(data int) uint32 { return uint32(v.get(data)) }

func (v *Terminal) get16(data int) uint16 { return uint16(v.get(data)) }

func clamp16(n int) int { return min(max(n, 1), 0xFFFF) }

func b2u(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}
