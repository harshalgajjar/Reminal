// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// Chromium as a snap (what Ubuntu installs for "chromium") is confined: it
// may start no program in a hidden folder of the home folder, where reminal
// is installed, and may not reach ~/.reminal, where the daemon's browser
// socket is. It may start a program in its own folder
// (~/snap/chromium/common) and reach a socket there. So for a snap browser
// that is set up, this folder holds
//
//	~/snap/chromium/common/reminal/reminal       a copy of reminal, the helper
//	~/snap/chromium/common/reminal/browser.sock  a second socket of the hub
//
// and the hub serves the second socket as it serves the first. Whether it
// is served at all is said by a note outside the snap's reach,
// ~/.reminal/browser-snap, which only `reminal integrate` writes and takes
// away: what is in the snap's folder never decides it.
//
// The confined browser can write in that folder and in the folders above
// it, and this code runs unconfined. So nothing here follows a path a
// confined program could have turned into a link to somewhere else: each
// folder from the home folder down is opened by name without following
// links and must be the person's own, and every file is then made, read or
// removed by name inside the folder that was opened.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// snapBrowser is the snap set up this way.
const snapBrowser = "chromium"

const (
	snapHelperName = "reminal"
	snapSockName   = "browser.sock"
)

// snapBrowserParts are the folders from the home folder down to the one
// reminal keeps its files in, for the snap.
func snapBrowserParts() []string {
	return []string{"snap", snapBrowser, "common", "reminal"}
}

// SnapBrowserDir is the folder reminal keeps its files in for the snap
// browser, as a path (for a manifest and for words; never opened by it).
func SnapBrowserDir(home string) string {
	return filepath.Join(append([]string{home}, snapBrowserParts()...)...)
}

// SnapBrowserHelper is the path of reminal's copy there.
func SnapBrowserHelper(home string) string {
	return filepath.Join(SnapBrowserDir(home), snapHelperName)
}

// SnapBrowserProfile is the snap browser's user data folder, relative to
// the home folder.
func SnapBrowserProfile() string {
	return filepath.Join("snap", snapBrowser, "common", snapBrowser)
}

// openOwnDir opens the folder parts under home, one name at a time, never
// following a link, each of them a folder that is the person's own. With
// create, a folder that is missing is made (0700). The last one must be
// open to nobody else; with create it is put so.
func openOwnDir(home string, parts []string, create bool) (*os.File, error) {
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	uid := uint32(os.Getuid())
	for i, name := range parts {
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if err = unix.Mkdirat(fd, name, 0o700); err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(fd)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(parts[:i+1]...), err)
		}
		fd = next
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			unix.Close(fd)
			return nil, err
		}
		if st.Uid != uid {
			unix.Close(fd)
			return nil, fmt.Errorf("%s is not this user's folder", filepath.Join(parts[:i+1]...))
		}
		if i == len(parts)-1 && st.Mode&0o077 != 0 {
			if !create {
				unix.Close(fd)
				return nil, fmt.Errorf("%s is open to other users", filepath.Join(parts...))
			}
			if err := unix.Fchmod(fd, 0o700); err != nil {
				unix.Close(fd)
				return nil, err
			}
		}
	}
	return os.NewFile(uintptr(fd), filepath.Join(parts...)), nil
}

// snapManifestMax bounds a host manifest read back from the snap's folder.
const snapManifestMax = 64 << 10

// openFileIn opens the regular file name in the folder dir: never a link,
// never waited for (something that is not a file is refused, whatever it
// is), and says its size.
func openFileIn(dir *os.File, name string) (*os.File, int64, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, 0, fmt.Errorf("%s is not a file", name)
	}
	return os.NewFile(uintptr(fd), name), st.Size, nil
}

// readIn is the regular file name in the folder dir, when it is no larger
// than max.
func readIn(dir *os.File, name string, max int64) ([]byte, error) {
	f, size, err := openFileIn(dir, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if size > max {
		return nil, fmt.Errorf("%s is too large", name)
	}
	return io.ReadAll(io.LimitReader(f, max))
}

// sameIn reports whether the file name in the folder dir is want, byte for
// byte. One of another size is not read at all.
func sameIn(dir *os.File, name string, want []byte) bool {
	f, size, err := openFileIn(dir, name)
	if err != nil {
		return false
	}
	defer f.Close()
	if size != int64(len(want)) {
		return false
	}
	have, err := io.ReadAll(io.LimitReader(f, size))
	return err == nil && sameBytes(have, want)
}

// writeIn writes name in the folder dir: to a new file beside it, then moved
// over it, so a reader sees the old or the new and a link is never written
// through.
func writeIn(dir *os.File, name string, data []byte, mode uint32) error {
	tmp := fmt.Sprintf(".%s.%d.tmp", name, os.Getpid())
	_ = unix.Unlinkat(int(dir.Fd()), tmp, 0)
	fd, err := unix.Openat(int(dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(os.FileMode(mode))
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name)
	}
	if err != nil {
		_ = unix.Unlinkat(int(dir.Fd()), tmp, 0)
	}
	return err
}

// snapBrowserNote is the note that says the snap browser is set up: in
// ~/.reminal, which the snap may not reach.
func snapBrowserNote(home string) string {
	return filepath.Join(home, ".reminal", "browser-snap")
}

// SnapBrowserEnabled reports whether a setup was made (the note is there).
func SnapBrowserEnabled(home string) bool {
	st, err := os.Lstat(snapBrowserNote(home))
	return err == nil && st.Mode().IsRegular()
}

// SnapBrowserSetUp puts a copy of the reminal at exe where the snap browser
// may start it, making the folder if need be, and writes the note that has
// the daemon serve its socket there. Run again it brings the copy up to exe
// (an upgrade).
func SnapBrowserSetUp(home, exe string) error {
	want, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	dir, err := openOwnDir(home, snapBrowserParts(), true)
	if err != nil {
		return err
	}
	defer dir.Close()
	if !sameIn(dir, snapHelperName, want) {
		if err := writeIn(dir, snapHelperName, want, 0o700); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(snapBrowserNote(home)), 0o700); err != nil {
		return err
	}
	return os.WriteFile(snapBrowserNote(home), []byte(snapBrowser+"\n"), 0o600)
}

// SnapBrowserSetUpDone reports whether a setup was made (the note is
// there), and whether the copy in the snap's folder is the reminal at exe.
func SnapBrowserSetUpDone(home, exe string) (there, current bool) {
	if !SnapBrowserEnabled(home) {
		return false, false
	}
	dir, err := openOwnDir(home, snapBrowserParts(), false)
	if err != nil {
		return true, false
	}
	defer dir.Close()
	want, err := os.ReadFile(exe)
	return true, err == nil && sameIn(dir, snapHelperName, want)
}

// SnapBrowserRemove takes away the note first, so the daemon serves nothing
// there whatever is left, then what is in reminal's folder in the snap (the
// copy, the socket, and anything else put there since) and the folder.
func SnapBrowserRemove(home string) error {
	if err := os.Remove(snapBrowserNote(home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	parts := snapBrowserParts()
	dir, err := openOwnDirLoose(home, parts)
	if err != nil {
		return nil // no such folder of this user's: nothing of reminal's is there
	}
	names, _ := dir.Readdirnames(-1)
	for _, name := range append(names, snapHelperName, snapSockName) {
		if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.EISDIR) {
			dir.Close()
			return err
		}
	}
	dir.Close()
	parent, err := openOwnDirLoose(home, parts[:len(parts)-1])
	if err != nil {
		return nil
	}
	defer parent.Close()
	err = unix.Unlinkat(int(parent.Fd()), parts[len(parts)-1], unix.AT_REMOVEDIR)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTEMPTY) {
		return nil // a folder made inside it since is left; nothing is served there
	}
	return err
}

// openOwnDirLoose is openOwnDir for a folder that others may read (the
// snap's own folders are made so).
func openOwnDirLoose(home string, parts []string) (*os.File, error) {
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	uid := uint32(os.Getuid())
	for i, name := range parts {
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil || st.Uid != uid {
			unix.Close(fd)
			return nil, fmt.Errorf("%s is not this user's folder", filepath.Join(parts[:i+1]...))
		}
	}
	return os.NewFile(uintptr(fd), filepath.Join(parts...)), nil
}

func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := sha256.Sum256(a), sha256.Sum256(b)
	return bytes.Equal(x[:], y[:])
}

// installedExe is the path this reminal was started from. After an upgrade
// in place the system says it with " (deleted)" at its end: the path, with
// the new reminal at it, is what is wanted.
func installedExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(exe, " (deleted)"), nil
}

// snapBrowserListen opens the hub's second socket, in the snap browser's
// folder, when a setup was made (the note in ~/.reminal says so): never
// otherwise, and the folder is never made here. The copy is first brought
// up to the installed reminal, which an upgrade has just replaced. The
// returned folder stays open for as long as the socket is served.
func snapBrowserListen(home string) (net.Listener, *os.File, error) {
	if !SnapBrowserEnabled(home) {
		return nil, nil, errors.New("no snap browser is set up")
	}
	dir, err := openOwnDir(home, snapBrowserParts(), false)
	if err != nil {
		return nil, nil, err
	}
	if exe, err := installedExe(); err != nil {
		log.Printf("browser: the copy for the snap browser was not looked at: %v", err)
	} else if want, err := os.ReadFile(exe); err != nil {
		log.Printf("browser: the copy for the snap browser was not looked at: %v", err)
	} else if !sameIn(dir, snapHelperName, want) {
		if err := writeIn(dir, snapHelperName, want, 0o700); err != nil {
			log.Printf("browser: the copy for the snap browser was not brought up to date: %v", err)
		}
	}
	_ = unix.Unlinkat(int(dir.Fd()), snapSockName, 0)
	// Bound by name inside the folder that was opened, not by a path from
	// the home folder down.
	ln, err := net.Listen("unix", fmt.Sprintf("/proc/self/fd/%d/%s", dir.Fd(), snapSockName))
	if err != nil {
		dir.Close()
		return nil, nil, err
	}
	closeSockToOthers(dir, snapSockName)
	return ln, dir, nil
}

// closeSockToOthers puts the socket name in the folder dir at 0600 (its
// folder is already the person's alone): through a handle on the thing of
// that name itself, and only when it is a socket, so a link put in its
// place is never followed.
func closeSockToOthers(dir *os.File, name string) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFSOCK {
		_ = unix.Chmod(fmt.Sprintf("/proc/self/fd/%d", fd), 0o600)
	}
}

// snapBrowserGone reports whether the setup served was taken away since
// (its note is no longer there).
func snapBrowserGone(home string) bool { return !SnapBrowserEnabled(home) }

// snapBrowserUnlisten removes the socket's name from dir.
func snapBrowserUnlisten(dir *os.File) {
	_ = unix.Unlinkat(int(dir.Fd()), snapSockName, 0)
	_ = dir.Close()
}

// snapBrowserCanRead reports whether the snap browser may read the file at
// path: one in the home folder and in no hidden folder of it, or on a
// drive that was plugged in.
func snapBrowserCanRead(home, path string) bool {
	for _, root := range []string{"/media/", "/mnt/", "/run/media/"} {
		if strings.HasPrefix(path, root) {
			return true
		}
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	return !strings.HasPrefix(rel, ".") && !strings.HasPrefix(rel, "snap"+string(filepath.Separator))
}

// browserHostSockInSnap is the hub's socket as reminal's helper reaches it
// when a snap browser started it: the one in that snap's own folder. "" when
// this process is not inside the snap.
func browserHostSockInSnap() string {
	if os.Getenv("SNAP_NAME") != snapBrowser {
		return ""
	}
	common := os.Getenv("SNAP_USER_COMMON")
	if common == "" {
		return ""
	}
	return filepath.Join(common, "reminal", snapSockName)
}

// SnapBrowserCanSetUp reports whether SnapBrowserSetUp could make its
// folder: each folder on the way that is there is the person's own and no
// link (one not there yet is made).
func SnapBrowserCanSetUp(home string) bool {
	parts := snapBrowserParts()
	for n := 1; n <= len(parts); n++ {
		dir, err := openOwnDirLoose(home, parts[:n])
		if errors.Is(err, unix.ENOENT) {
			return true
		}
		if err != nil {
			return false
		}
		dir.Close()
	}
	return true
}

// snapManifestParts are the folders down to where the snap browser reads
// its host manifests.
func snapManifestParts() []string {
	return []string{"snap", snapBrowser, "common", snapBrowser, "NativeMessagingHosts"}
}

// SnapBrowserManifestPath is the path of the host manifest name there (for
// words; never opened by it).
func SnapBrowserManifestPath(home, name string) string {
	return filepath.Join(append(append([]string{home}, snapManifestParts()...), name)...)
}

// SnapBrowserWriteManifest writes the host manifest name where the snap
// browser reads it, making the folders if need be.
func SnapBrowserWriteManifest(home, name string, data []byte) error {
	parts := snapManifestParts()
	for n := 1; n <= len(parts); n++ {
		dir, err := openOwnDirLoose(home, parts[:n])
		if errors.Is(err, unix.ENOENT) {
			parent, perr := openOwnDirLoose(home, parts[:n-1])
			if perr != nil {
				return perr
			}
			merr := unix.Mkdirat(int(parent.Fd()), parts[n-1], 0o755)
			parent.Close()
			if merr != nil && !errors.Is(merr, unix.EEXIST) {
				return merr
			}
			dir, err = openOwnDirLoose(home, parts[:n])
		}
		if err != nil {
			return err
		}
		if n == len(parts) {
			defer dir.Close()
			return writeIn(dir, name, data, 0o644)
		}
		dir.Close()
	}
	return nil
}

// SnapBrowserManifest is the host manifest name as it is there.
func SnapBrowserManifest(home, name string) ([]byte, error) {
	dir, err := openOwnDirLoose(home, snapManifestParts())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return readIn(dir, name, snapManifestMax)
}

// SnapBrowserRemoveManifest takes the host manifest name away.
func SnapBrowserRemoveManifest(home, name string) error {
	dir, err := openOwnDirLoose(home, snapManifestParts())
	if err != nil {
		return nil
	}
	defer dir.Close()
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}
