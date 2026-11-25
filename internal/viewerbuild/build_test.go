// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package viewerbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func spec(t *testing.T) Spec {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "client", "web", "build.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ReadSpec(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The pages, sources and build lines below are made from build.json's own
// list of scripts, so these tests hold whatever the list is.

// order is the order a page here marks s's scripts in, which is not the
// list's: the first, then the rest from the last.
func order(s Spec) []string {
	out := []string{s.Scripts[0]}
	for i := len(s.Scripts) - 1; i >= 1; i-- {
		out = append(out, s.Scripts[i])
	}
	return out
}

// pageOf is head and then a marker for each of s's scripts, each line ended
// with nl.
func pageOf(s Spec, head, nl string) string {
	for _, sc := range order(s) {
		head += "<script data-build=\"" + sc + "\"></script>" + nl
	}
	return head
}

// sources is a source for each of s's scripts, the first one's as given.
func sources(s Spec, first string) map[string][]byte {
	src := map[string][]byte{}
	for _, sc := range s.Scripts {
		src[sc] = []byte("// " + sc + "\n")
	}
	src[s.Scripts[0]] = []byte(first)
	return src
}

// buildLine is the build line of src's scripts: their names in the list's order.
func buildLine(s Spec, src map[string][]byte) string {
	names := make([]string, 0, len(s.Scripts))
	for _, sc := range s.Scripts {
		names = append(names, Name(sc, src[sc]))
	}
	return strings.Join(names, " ")
}

const pageHead = "<!doctype html><title>t</title>\n"

// A script is served by a name its content makes, and the page's tag for it
// names that build and carries its SRI: a new build is a new name, so the
// old one can be cached for good.
func TestAScriptIsNamedByItsContentAndItsTagStamped(t *testing.T) {
	s := spec(t)
	page := pageOf(s, pageHead, "\n")
	src := sources(s, "console.log('one')\n")
	p, err := Render(s, []byte(page), src)
	if err != nil {
		t.Fatal(err)
	}
	for sc, js := range src {
		name := Name(sc, js)
		if !strings.HasPrefix(name, sc+".") || len(name) != len(sc)+1+16+3 {
			t.Fatalf("%s's name: %q", sc, name)
		}
		if string(p.Assets["assets/"+name]) != string(js) {
			t.Fatalf("assets/%s is not %s.js", name, sc)
		}
		tag := `<script src="/assets/` + name + `" integrity="` + SRI(js) + `" crossorigin="anonymous"></script>`
		if strings.Count(string(p.HTML), tag) != 1 {
			t.Fatalf("the page does not carry %s once:\n%s", tag, p.HTML)
		}
	}
	if strings.Contains(string(p.HTML), "data-build") {
		t.Fatal("a marker was left in the page")
	}
	if p.Build != buildLine(s, src) {
		t.Fatalf("its build line: %q", p.Build)
	}
	src[s.Scripts[0]] = []byte("console.log('two')\n")
	q, _ := Render(s, []byte(page), src)
	if q.Build == p.Build || string(q.HTML) == string(p.HTML) {
		t.Fatalf("a new %s.js was not a new build", s.Scripts[0])
	}
}

// The list is build.json's, whatever it holds: with a script more at its
// end, the page is stamped with it too and it ends the build line.
func TestAListWithAScriptAddedAtItsEndRenders(t *testing.T) {
	s := spec(t)
	before := buildLine(s, sources(s, "one\n"))
	s.Scripts = append(append([]string{}, s.Scripts...), "extra")
	src := sources(s, "one\n")
	p, err := Render(s, []byte(pageOf(s, pageHead, "\n")), src)
	if err != nil {
		t.Fatal(err)
	}
	if p.Build != before+" "+Name("extra", src["extra"]) {
		t.Fatalf("its build line %q: want the line before it (%q), then the script added", p.Build, before)
	}
	if strings.Count(string(p.HTML), `<script src="/assets/`+Name("extra", src["extra"])+`"`) != 1 || strings.Contains(string(p.HTML), "data-build") {
		t.Fatalf("the page does not carry the script added once:\n%s", p.HTML)
	}
	// A page with no marker for it is refused, as for any script of the list.
	if _, err := Render(s, []byte(pageOf(spec(t), pageHead, "\n")), src); err == nil {
		t.Fatal("rendered a page with no marker for a script of the list")
	}
}

// A page that does not mark each script exactly once is refused, never
// served half stamped.
func TestAPageWithoutExactlyOneMarkerIsRefused(t *testing.T) {
	s := spec(t)
	page := pageOf(s, pageHead, "\n")
	src := sources(s, "a")
	for _, bad := range []string{
		"<script data-build=\"" + s.Scripts[0] + "\"></script>",
		page + "<script data-build=\"" + s.Scripts[len(s.Scripts)-1] + "\"></script>",
	} {
		if _, err := Render(s, []byte(bad), src); err == nil {
			t.Fatalf("rendered %q", bad)
		}
	}
	last := s.Scripts[len(s.Scripts)-1]
	if _, err := Render(s, []byte(page), map[string][]byte{last: []byte("a")}); err == nil {
		t.Fatalf("rendered with %s.js alone", last)
	}
}

// A build.json naming a rule this code does not follow is refused, so the
// binary and the Worker's build can only change their rule together.
func TestARuleThisBuildDoesNotKnowIsRefused(t *testing.T) {
	good, _ := os.ReadFile(filepath.Join("..", "client", "web", "build.json"))
	first := `"` + spec(t).Scripts[0] + `"`
	for _, c := range []struct{ from, to string }{
		{`"sha384"`, `"sha256"`},
		{`{sha256:16}`, `{sha256:12}`},
		{`"keep": 3`, `"keep": 0`},
		{first, `"../x"`},
		{first, strings.ToUpper(first[:2]) + first[2:]},
		{`"version": 1`, `"version": 2`},
		{`"keep": 3`, `"keep": 3, "minify": true`},
		{`"sw.js"`, `"../sw.js"`},
		{`"sw.js"`, `"index.html"`},
		{`"sw.js"`, `"assets/x.js"`},
	} {
		if !strings.Contains(string(good), c.from) {
			t.Fatalf("build.json has no %s to change", c.from)
		}
		if _, err := ReadSpec([]byte(strings.Replace(string(good), c.from, c.to, 1))); err == nil {
			t.Fatalf("took a build.json with %s for %s", c.to, c.from)
		}
	}
}

// The page in the tree renders, and Write lays it out as a directory serves
// it: what build.json copies, the stamped page, its scripts and builds.txt.
func TestTheViewerInTheTreeRendersAndIsWritten(t *testing.T) {
	web := filepath.Join("..", "client", "web")
	out := filepath.Join(t.TempDir(), "not", "yet")
	if err := Write(web, out); err != nil {
		t.Fatal(err)
	}
	_, p, err := FromFS(os.DirFS(web), ".")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := os.ReadFile(filepath.Join(out, "index.html"))
	if string(html) != string(p.HTML) {
		t.Fatal("the written page is not the rendered one")
	}
	for name := range p.Assets {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"sw.js", "manifest.webmanifest", "icons/icon-192.png", "vendor/ristretto255.js", "assets/builds.txt"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Fatalf("%s not written: %v", f, err)
		}
	}
}

// Nothing but the page names a build: the service worker caches nothing and
// names no script by its hash, nor builds.txt, so it never needs stamping.
// If it ever does, it must be stamped here and in build-viewer.mjs alike
// (and the parity test then covers it, as it covers every built file).
func TestOnlyThePageNamesABuild(t *testing.T) {
	for _, f := range []string{"sw.js", "reload.js", "manifest.webmanifest"} {
		b, err := os.ReadFile(filepath.Join("..", "client", "web", f))
		if err != nil {
			t.Fatal(err)
		}
		for _, sc := range spec(t).Scripts {
			if strings.Contains(string(b), "/assets/"+sc+".") || strings.Contains(string(b), "builds.txt") {
				t.Fatalf("%s names a build of %s or builds.txt: stamp it in both builders", f, sc)
			}
		}
	}
}

// The page is stamped as the bytes it is, whatever they are: what is not
// UTF-8, a BOM or CRLFs, and a script with $& in it, come out as they went in.
func TestThePageIsStampedAsItsBytes(t *testing.T) {
	s := spec(t)
	const head = "\xef\xbb\xbf<html>\xe9\xff\r\n"
	src := sources(s, "a('$&$1')\n")
	last := s.Scripts[len(s.Scripts)-1]
	src[last] = []byte("a('\xe9')\r\n")
	p, err := Render(s, []byte(pageOf(s, head, "\r\n")), src)
	if err != nil {
		t.Fatal(err)
	}
	want := head
	for _, sc := range order(s) {
		want += "<script src=\"/assets/" + Name(sc, src[sc]) + "\" integrity=\"" + SRI(src[sc]) + "\" crossorigin=\"anonymous\"></script>\r\n"
	}
	if string(p.HTML) != want {
		t.Fatalf("got %q", p.HTML)
	}
}
