// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

// Package viewerbuild makes the viewer's page from its sources: each script,
// internal/client/web/<name>.js, served as assets/<name>.<hash>.js — named by
// its content, so it can be cached for good — and the page's tag for it
// stamped with that name and its SRI hash. Nothing it makes is committed: the
// binary renders the page from what it embeds as it starts, and the Worker's
// copy is built by cloudflare/scripts/build-viewer.mjs at every wrangler run.
// Both follow internal/client/web/build.json, and a test holds them to the
// same output byte for byte.
package viewerbuild

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Spec is build.json: the rule both builders follow.
type Spec struct {
	Scripts  []string `json:"scripts"`
	Name     string   `json:"name"`
	SRI      string   `json:"sri"`
	Template string   `json:"template"`
	Tag      string   `json:"tag"`
	Keep     int      `json:"keep"`
	Copy     []string `json:"copy"`
}

// The rule this code knows. A build.json saying anything else is refused,
// never half followed: changing the rule is changing both builders.
const (
	knownName = "assets/{script}.{sha256:16}.js"
	knownSRI  = "sha384"
)

// ReadSpec reads and checks build.json.
func ReadSpec(b []byte) (Spec, error) {
	var s Spec
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("viewerbuild: build.json: %w", err)
	}
	switch {
	case len(s.Scripts) == 0:
		return s, errors.New("viewerbuild: build.json names no scripts")
	case s.Name != knownName:
		return s, fmt.Errorf("viewerbuild: build.json names its assets %q; this build knows only %q", s.Name, knownName)
	case s.SRI != knownSRI:
		return s, fmt.Errorf("viewerbuild: build.json's SRI is %q; this build knows only %q", s.SRI, knownSRI)
	case !strings.Contains(s.Template, "{script}"):
		return s, errors.New("viewerbuild: build.json's template does not name {script}")
	case !strings.Contains(s.Tag, "{name}") || !strings.Contains(s.Tag, "{sri}"):
		return s, errors.New("viewerbuild: build.json's tag does not carry {name} and {sri}")
	case s.Keep < 1:
		return s, errors.New("viewerbuild: build.json keeps no builds")
	}
	for _, sc := range s.Scripts {
		if sc == "" || strings.ContainsAny(sc, "/\\.") {
			return s, fmt.Errorf("viewerbuild: build.json's script %q is not a plain name", sc)
		}
	}
	return s, nil
}

// Name is the asset name of a script's build.
func Name(script string, js []byte) string {
	sum := sha256.Sum256(js)
	return script + "." + hex.EncodeToString(sum[:])[:16] + ".js"
}

// SRI is the integrity value of a script.
func SRI(js []byte) string {
	sum := sha512.Sum384(js)
	return "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
}

// Page is the page as it is served: its HTML, and its scripts by asset path
// ("assets/app.<hash>.js").
type Page struct {
	HTML   []byte
	Assets map[string][]byte
	// Build is this build's line in builds.txt: its scripts' names, in order.
	Build string
}

// Render stamps index (its sources' page) with each script's tag.
func Render(s Spec, index []byte, src map[string][]byte) (Page, error) {
	p := Page{Assets: map[string][]byte{}}
	html := string(index)
	var names []string
	for _, sc := range s.Scripts {
		js, ok := src[sc]
		if !ok {
			return p, fmt.Errorf("viewerbuild: no %s.js", sc)
		}
		marker := strings.ReplaceAll(s.Template, "{script}", sc)
		if n := strings.Count(html, marker); n != 1 {
			return p, fmt.Errorf("viewerbuild: index.html has %d of %s, want exactly 1", n, marker)
		}
		name := Name(sc, js)
		tag := strings.NewReplacer("{name}", name, "{sri}", SRI(js)).Replace(s.Tag)
		html = strings.Replace(html, marker, tag, 1)
		p.Assets["assets/"+name] = js
		names = append(names, name)
	}
	p.HTML = []byte(html)
	p.Build = strings.Join(names, " ")
	return p, nil
}

// FromFS renders the page in dir of fsys (internal/client/web: build.json,
// index.html and the scripts).
func FromFS(fsys fs.FS, dir string) (Spec, Page, error) {
	read := func(name string) ([]byte, error) { return fs.ReadFile(fsys, path.Join(dir, name)) }
	b, err := read("build.json")
	if err != nil {
		return Spec{}, Page{}, err
	}
	s, err := ReadSpec(b)
	if err != nil {
		return s, Page{}, err
	}
	index, err := read("index.html")
	if err != nil {
		return s, Page{}, err
	}
	src := map[string][]byte{}
	for _, sc := range s.Scripts {
		if src[sc], err = read(sc + ".js"); err != nil {
			return s, Page{}, err
		}
	}
	p, err := Render(s, index, src)
	return s, p, err
}

// Write puts the page, as served, in out: what the spec copies from web (a
// directory), then the stamped index.html, its scripts and builds.txt (this
// build alone). For rigs that serve a directory, and the parity check.
func Write(web, out string) error {
	s, p, err := FromFS(os.DirFS(web), ".")
	if err != nil {
		return err
	}
	for _, c := range s.Copy {
		if err := copyTree(filepath.Join(web, c), filepath.Join(out, c)); err != nil {
			return err
		}
	}
	files := map[string][]byte{"index.html": p.HTML, "assets/builds.txt": []byte(p.Build + "\n")}
	for k, v := range p.Assets {
		files[k] = v
	}
	for rel, b := range files {
		f := filepath.Join(out, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}
