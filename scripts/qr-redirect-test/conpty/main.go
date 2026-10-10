//go:build windows

// conpty runs a command on a real Windows console (a ConPTY, what Windows
// Terminal uses) and saves everything it draws to a file, so a test can see
// what reminal prints to an interactive console.
//
//	conpty <outfile> <command line>
package main

import (
	"io"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func main() {
	out, cmdline := os.Args[1], strings.Join(os.Args[2:], " ")
	var inR, inW, outR, outW windows.Handle
	must(windows.CreatePipe(&inR, &inW, nil, 0))
	must(windows.CreatePipe(&outR, &outW, nil, 0))
	var pc windows.Handle
	must(windows.CreatePseudoConsole(windows.Coord{X: 200, Y: 80}, inR, outW, 0, &pc))
	attrs, err := windows.NewProcThreadAttributeList(1)
	must(err)
	must(attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, unsafe.Pointer(pc), unsafe.Sizeof(pc)))
	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(*si))
	// Without this the child inherits this process's own stdio (a pipe, under
	// a test runner) instead of the pseudo console.
	si.Flags = windows.STARTF_USESTDHANDLES
	var pi windows.ProcessInformation
	must(windows.CreateProcess(nil, windows.StringToUTF16Ptr(cmdline), nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, nil, nil, &si.StartupInfo, &pi))
	f, err := os.Create(out)
	must(err)
	done := make(chan struct{})
	go func() { io.Copy(f, os.NewFile(uintptr(outR), "conout")); close(done) }()
	windows.WaitForSingleObject(pi.Process, 30000)
	time.Sleep(500 * time.Millisecond) // let the console flush the last frame
	windows.ClosePseudoConsole(pc)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	f.Close()
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
