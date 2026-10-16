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

const page = "<!doctype html><title>t</title>\n  <script data-build=\"header\"></script>\n  <script data-build=\"app\"></script>\n"

// A script is served by a name its content makes, and the page's tag for it
// names that build and carries its SRI: a new build is a new name, so the
// old one can be cached for good.
func TestAScriptIsNamedByItsContentAndItsTagStamped(t *testing.T) {
	src := map[string][]byte{"header": []byte("fit()\n"), "app": []byte("console.log('one')\n")}
	p, err := Render(spec(t), []byte(page), src)
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
	if p.Build != Name("header", src["header"])+" "+Name("app", src["app"]) {
		t.Fatalf("its build line: %q", p.Build)
	}
	src["app"] = []byte("console.log('two')\n")
	q, _ := Render(spec(t), []byte(page), src)
	if q.Build == p.Build || string(q.HTML) == string(p.HTML) {
		t.Fatal("a new app.js was not a new build")
	}
}

// A page that does not mark each script exactly once is refused, never
// served half stamped.
func TestAPageWithoutExactlyOneMarkerIsRefused(t *testing.T) {
	src := map[string][]byte{"header": []byte("h"), "app": []byte("a")}
	for _, bad := range []string{
		"<script data-build=\"header\"></script>",
		page + "<script data-build=\"app\"></script>",
	} {
		if _, err := Render(spec(t), []byte(bad), src); err == nil {
			t.Fatalf("rendered %q", bad)
		}
	}
	if _, err := Render(spec(t), []byte(page), map[string][]byte{"app": []byte("a")}); err == nil {
		t.Fatal("rendered without header.js")
	}
}

// A build.json naming a rule this code does not follow is refused, so the
// binary and the Worker's build can only change their rule together.
func TestARuleThisBuildDoesNotKnowIsRefused(t *testing.T) {
	good, _ := os.ReadFile(filepath.Join("..", "client", "web", "build.json"))
	for _, c := range []struct{ from, to string }{
		{`"sha384"`, `"sha256"`},
		{`{sha256:16}`, `{sha256:12}`},
		{`"keep": 3`, `"keep": 0`},
		{`"header", "app"`, `"../x", "app"`},
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
	out := t.TempDir()
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
