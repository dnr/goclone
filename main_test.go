package main

import (
	"archive/zip"
	"bytes"
	"go/parser"
	"go/token"
	modfile "golang.org/x/mod/modfile"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRewriteGoImports(t *testing.T) {
	src := []byte("package p\n\nimport (\n    \"fmt\"\n    \"old/mod/pkg\"\n)")
	out, err := rewriteGoImports(src, map[string]string{"old/mod": "new/mod"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "", out, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Imports) != 2 {
		t.Fatalf("expected 2 imports, got %d", len(f.Imports))
	}
	got := f.Imports[1].Path.Value
	if got != "\"new/mod/pkg\"" {
		t.Errorf("unexpected import path: %s", got)
	}
}

func TestRewriteGoImportsNoChange(t *testing.T) {
	src := []byte("package p\nimport \"fmt\"")
	out, err := rewriteGoImports(src, map[string]string{"old/mod": "new/mod"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(src) {
		t.Errorf("expected no change")
	}
}

func TestRewriteGoImportsSubstring(t *testing.T) {
	src := []byte("package p\nimport \"somethingelse/old/mod/pkg\"")
	out, err := rewriteGoImports(src, map[string]string{"old/mod": "new/mod"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(src) {
		t.Errorf("unexpected rewrite: %s", string(out))
	}
}

func TestRewriteGoMod(t *testing.T) {
	src := []byte("module old/mod\n\nrequire old/mod/pkg v1.0.0\n")
	out, err := rewriteGoMod(src, map[string]string{"old/mod": "new/mod"})
	if err != nil {
		t.Fatal(err)
	}
	mf, err := modfile.Parse("go.mod", out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mf.Module.Mod.Path != "new/mod" {
		t.Errorf("module path not rewritten: %s", mf.Module.Mod.Path)
	}
	if len(mf.Require) != 1 || mf.Require[0].Mod.Path != "new/mod/pkg" {
		t.Errorf("require not rewritten: %#v", mf.Require)
	}
}

func TestRewriteGoModNoChange(t *testing.T) {
	src := []byte("module other/mod\n\nrequire other/mod/pkg v1.0.0\n")
	out, err := rewriteGoMod(src, map[string]string{"old/mod": "new/mod"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(src) {
		t.Errorf("expected no change")
	}
}

func TestRewriteGoModSubstring(t *testing.T) {
	src := []byte("module somethingelse/old/mod\n\nrequire somethingelse/old/mod/pkg v1.0.0\n")
	out, err := rewriteGoMod(src, map[string]string{"old/mod": "new/mod"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(src) {
		t.Errorf("unexpected rewrite: %s", string(out))
	}
}

func TestRewriteZip(t *testing.T) {
	// build zip archive with a go file and go.mod
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	gof, _ := w.Create("foo.go")
	gof.Write([]byte("package foo\nimport \"old/mod/pkg\""))
	modf, _ := w.Create("go.mod")
	modf.Write([]byte("module old/mod\n\nrequire old/mod/pkg v1.0.0\n"))
	w.Close()

	out, err := rewriteZip(buf.Bytes(), map[string]string{"old/mod": "new/mod"})
	if err != nil {
		t.Fatal(err)
	}

	r, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		switch f.Name {
		case "foo.go":
			if !bytes.Contains(data, []byte("new/mod/pkg")) {
				t.Errorf("foo.go not rewritten: %s", data)
			}
		case "go.mod":
			mf, err := modfile.Parse("go.mod", data, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mf.Module.Mod.Path != "new/mod" {
				t.Errorf("module path not rewritten: %s", mf.Module.Mod.Path)
			}
		}
	}
}

func TestRewriteZipSubstring(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	gof, _ := w.Create("foo.go")
	gof.Write([]byte("package foo\nimport \"somethingelse/old/mod/pkg\""))
	modf, _ := w.Create("go.mod")
	modf.Write([]byte("module somethingelse/old/mod\n"))
	w.Close()

	out, err := rewriteZip(buf.Bytes(), map[string]string{"old/mod": "new/mod"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		if bytes.Contains(data, []byte("new/mod")) {
			t.Errorf("unexpected rewrite in %s: %s", f.Name, data)
		}
	}
}

func TestRewriteZipWithPrefix(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	prefix := "old/mod@v1.0.0/"
	gof, _ := w.Create(prefix + "foo.go")
	gof.Write([]byte("package foo\nimport \"old/mod/pkg\""))
	modf, _ := w.Create(prefix + "go.mod")
	modf.Write([]byte("module old/mod\n\nrequire old/mod/pkg v1.0.0\n"))
	w.Close()

	out, err := rewriteZip(buf.Bytes(), map[string]string{"old/mod": "example.com/_two/old/mod"})
	if err != nil {
		t.Fatal(err)
	}

	r, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, "example.com/_two/old/mod@v1.0.0/") {
			t.Errorf("filename not rewritten: %s", f.Name)
		}
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		if strings.Contains(f.Name, "foo.go") && !bytes.Contains(data, []byte("example.com/_two/old/mod/pkg")) {
			t.Errorf("foo.go import not rewritten: %s", data)
		}
		if strings.HasSuffix(f.Name, "go.mod") {
			mf, err := modfile.Parse("go.mod", data, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mf.Module.Mod.Path != "example.com/_two/old/mod" {
				t.Errorf("module path not rewritten: %s", mf.Module.Mod.Path)
			}
		}
	}
}

func TestVanityHandler(t *testing.T) {
	host = stringPtr("example.com")
	req := httptest.NewRequest("GET", "/pkg?go-get=1", nil)
	w := httptest.NewRecorder()
	vanityHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	expected := "<meta name=\"go-import\" content=\"example.com/pkg mod https://example.com/_mod/\">"
	if w.Body.String() != expected {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
	// missing go-get should 404
	req2 := httptest.NewRequest("GET", "/pkg", nil)
	w2 := httptest.NewRecorder()
	vanityHandler(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w2.Code)
	}
}

func stringPtr(s string) *string { return &s }

func TestParseProxyPath(t *testing.T) {
	up, orig, rest, ok := parseProxyPath("_two/golang.org/x/text/@v/list")
	if !ok {
		t.Fatal("parse failed")
	}
	if up != "_two/golang.org/x/text" || orig != "golang.org/x/text" || rest != "list" {
		t.Fatalf("unexpected result %q %q %q", up, orig, rest)
	}
	_, _, _, ok = parseProxyPath("_mod/golang.org/x/text/@v/list")
	if ok {
		t.Fatal("expected failure for reserved clone name")
	}
}
