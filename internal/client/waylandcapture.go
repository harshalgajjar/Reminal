// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // register the PNG decoder for the screenshot tools' output
	"os"
	"strings"
)

// Wayland screen capture.
//
// The X11 path (`import -window root`) returns a BLACK frame on Wayland: the
// compositor doesn't expose the desktop to X11 clients, so grabbing Xwayland's
// root captures nothing. Wayland instead requires a compositor-blessed
// screenshot path, which differs per desktop — so we shell out to whichever of
// these is present, newest-first by how universal it is:
//
//	grim              wlroots (Sway, Hyprland) — PNG to stdout, supports regions
//	gnome-screenshot  GNOME / Cinnamon (Linux Mint) — whole screen to a file
//	spectacle         KDE Plasma — whole screen to a file
//
// The tool returns a full-screen (or, for grim, a region) PNG; we decode it,
// crop to the requested rectangle if the tool couldn't, downscale, and
// re-encode JPEG — the same shape the X11 path produces, so the rest of the
// mirror pipeline is unchanged. Whichever tool runs may make the compositor show
// a one-time "share your screen?" consent prompt on the host (Wayland's model);
// that's expected and out of our control.

// isWaylandSession reports whether this login session is Wayland. XDG_SESSION_TYPE
// is the canonical signal and is set even when Xwayland also set DISPLAY (the
// common case on GNOME/Cinnamon), which is exactly where the old "DISPLAY empty"
// check missed Wayland and let the X11 path capture a black root.
func isWaylandSession() bool {
	if strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "wayland") {
		return true
	}
	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// waylandShotTool returns the name of the first available Wayland screenshot
// tool, or "" if none is installed.
func waylandShotTool() string {
	for _, t := range []string{"grim", "gnome-screenshot", "spectacle"} {
		if have(t) {
			return t
		}
	}
	return ""
}

// waylandShotDeps is the human-readable install hint listing the tools we try.
const waylandShotDeps = "grim (wlroots), gnome-screenshot (GNOME/Cinnamon), or spectacle (KDE)"

// waylandGrabPNG captures the whole screen as PNG bytes with the given tool.
// grim writes to stdout; the others only write a file, so we route through a
// temp path and read it back.
func waylandGrabPNG(tool string) ([]byte, error) {
	switch tool {
	case "grim":
		return runRaw("grim", "-t", "png", "-")
	case "gnome-screenshot", "spectacle":
		f, err := os.CreateTemp("", "reminal-wl-*.png")
		if err != nil {
			return nil, err
		}
		path := f.Name()
		_ = f.Close()
		defer os.Remove(path)
		var runErr error
		if tool == "gnome-screenshot" {
			_, runErr = run("gnome-screenshot", "-f", path)
		} else {
			// -b background (no GUI), -n no notification, -f fullscreen, -o file.
			_, runErr = run("spectacle", "-b", "-n", "-f", "-o", path)
		}
		if runErr != nil {
			return nil, runErr
		}
		return os.ReadFile(path)
	}
	return nil, fmt.Errorf("no wayland screenshot tool")
}

// waylandCapture captures a region of the Wayland desktop (or the whole screen
// when region is nil) and returns a downscaled JPEG, matching the X11 path's
// output. A region is honored natively by grim; for the file-based tools we grab
// the whole screen and crop it here.
func waylandCapture(region *image.Rectangle) ([]byte, error) {
	img, err := waylandGrabImage(region)
	if err != nil {
		return nil, err
	}
	return downscaleJPEG(img, winMaxWidth, 55)
}

// waylandCaptureRaw captures the same way as waylandCapture but returns
// tightly-packed RGBA at exactly tw×th, for the ffmpeg H.264 helper (see
// capture_ffmpeg.go). ffmpeg's rawvideo input needs one fixed frame size, so the
// image is force-scaled to tw×th rather than fit-inside.
func waylandCaptureRaw(region *image.Rectangle, tw, th int) ([]byte, error) {
	img, err := waylandGrabImage(region)
	if err != nil {
		return nil, err
	}
	return scaleExactRGBA(img, tw, th), nil
}

// waylandGrabImage takes one compositor screenshot (whole screen, or a region)
// and returns it decoded and cropped — the shared front half of waylandCapture
// and waylandCaptureRaw.
func waylandGrabImage(region *image.Rectangle) (image.Image, error) {
	tool := waylandShotTool()
	if tool == "" {
		return nil, fmt.Errorf("no Wayland screenshot tool found — install one of: %s", waylandShotDeps)
	}

	var pngBytes []byte
	var err error
	if tool == "grim" && region != nil {
		pngBytes, err = runRaw("grim", "-t", "png", "-g",
			fmt.Sprintf("%d,%d %dx%d", region.Min.X, region.Min.Y, region.Dx(), region.Dy()), "-")
	} else {
		pngBytes, err = waylandGrabPNG(tool)
	}
	if err != nil {
		return nil, fmt.Errorf("%s capture failed (Wayland may have denied the screen-share, or no display is attached): %w", tool, err)
	}
	if len(pngBytes) == 0 {
		return nil, fmt.Errorf("%s returned an empty image — the compositor likely denied screen capture", tool)
	}

	img, _, err := image.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("decode %s output: %w", tool, err)
	}
	// Crop in-process when the tool grabbed the whole screen but a sub-region was
	// asked for (grim already cropped). Clamp to the image bounds so a stale
	// geometry can't panic.
	if region != nil && !(tool == "grim") {
		r := region.Intersect(img.Bounds())
		if !r.Empty() {
			if sub, ok := img.(interface {
				SubImage(image.Rectangle) image.Image
			}); ok {
				img = sub.SubImage(r)
			}
		}
	}
	return img, nil
}

// scaleExactRGBA box-averages src to EXACTLY dw×dh and returns tightly-packed
// RGBA (dw*dh*4 bytes). Like downscaleJPEG's resampling but to a forced size and
// without the JPEG round-trip — what ffmpeg's rawvideo input consumes.
func scaleExactRGBA(src image.Image, dw, dh int) []byte {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := make([]byte, dw*dh*4)
	if sw <= 0 || sh <= 0 {
		return dst
	}
	for dy := 0; dy < dh; dy++ {
		sy0, sy1 := b.Min.Y+dy*sh/dh, b.Min.Y+(dy+1)*sh/dh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < dw; dx++ {
			sx0, sx1 := b.Min.X+dx*sw/dw, b.Min.X+(dx+1)*sw/dw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var rr, gg, bb, n uint32
			for y := sy0; y < sy1; y++ {
				for x := sx0; x < sx1; x++ {
					r, g, bl, _ := src.At(x, y).RGBA() // 16-bit per channel
					rr += r >> 8
					gg += g >> 8
					bb += bl >> 8
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			o := (dy*dw + dx) * 4
			dst[o] = byte(rr / n)
			dst[o+1] = byte(gg / n)
			dst[o+2] = byte(bb / n)
			dst[o+3] = 0xFF
		}
	}
	return dst
}

// downscaleJPEG box-averages src down to fit within maxW (only shrinking, like
// ImageMagick's "NxN>") and encodes JPEG at the given quality. Pure stdlib +
// arithmetic so it needs no image dependency and runs on every platform.
func downscaleJPEG(src image.Image, maxW, quality int) ([]byte, error) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return nil, fmt.Errorf("empty image")
	}
	dw, dh := sw, sh
	if sw > maxW {
		dw = maxW
		dh = sh * maxW / sw
		if dh < 1 {
			dh = 1
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		sy0, sy1 := b.Min.Y+dy*sh/dh, b.Min.Y+(dy+1)*sh/dh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < dw; dx++ {
			sx0, sx1 := b.Min.X+dx*sw/dw, b.Min.X+(dx+1)*sw/dw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var rr, gg, bb, n uint32
			for y := sy0; y < sy1; y++ {
				for x := sx0; x < sx1; x++ {
					r, g, bl, _ := src.At(x, y).RGBA() // 16-bit per channel
					rr += r >> 8
					gg += g >> 8
					bb += bl >> 8
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			o := dy*dst.Stride + dx*4
			dst.Pix[o] = byte(rr / n)
			dst.Pix[o+1] = byte(gg / n)
			dst.Pix[o+2] = byte(bb / n)
			dst.Pix[o+3] = 0xFF
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// waylandScreenSize returns the desktop dimensions by taking one screenshot and
// reading its bounds — used to build the "display:0" entry when there's no
// Xwayland root to measure (a pure-Wayland session). Best-effort.
func waylandScreenSize() (w, h int, ok bool) {
	tool := waylandShotTool()
	if tool == "" {
		return 0, 0, false
	}
	pngBytes, err := waylandGrabPNG(tool)
	if err != nil || len(pngBytes) == 0 {
		return 0, 0, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}
