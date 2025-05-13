package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// helper to build module version files
func buildModule(t *testing.T, version string) (mod, info, zipData []byte) {
	t.Helper()
	mod = []byte("module example.com/mod\n\ngo 1.20\n")
	info = []byte(fmt.Sprintf("{\"Version\":%q,\"Time\":\"2023-01-01T00:00:00Z\"}\n", version))

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	root := fmt.Sprintf("example.com/mod@%s/", version)
	f, err := w.Create(root + "go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(mod); err != nil {
		t.Fatal(err)
	}
	g, err := w.Create(root + "pkg/pkg.go")
	if err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf("package pkg\nconst Version = %q\n", version)
	if _, err := g.Write([]byte(src)); err != nil {
		t.Fatal(err)
	}
	u, err := w.Create(root + "use/use.go")
	if err != nil {
		t.Fatal(err)
	}
	useSrc := "package use\nimport _ \"example.com/mod/pkg\"\n"
	if _, err := u.Write([]byte(useSrc)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	zipData = buf.Bytes()
	return
}

func newUpstreamServer(t *testing.T) *httptest.Server {
	modV1, infoV1, zipV1 := buildModule(t, "v1.0.0")
	modV2, infoV2, zipV2 := buildModule(t, "v1.0.1")

	mux := http.NewServeMux()
	mux.HandleFunc("/example.com/mod/@v/list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v1.0.0\nv1.0.1\n")
	})
	mux.HandleFunc("/example.com/mod/@v/v1.0.0.mod", func(w http.ResponseWriter, r *http.Request) {
		w.Write(modV1)
	})
	mux.HandleFunc("/example.com/mod/@v/v1.0.0.info", func(w http.ResponseWriter, r *http.Request) {
		w.Write(infoV1)
	})
	mux.HandleFunc("/example.com/mod/@v/v1.0.0.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipV1)
	})
	mux.HandleFunc("/example.com/mod/@v/v1.0.1.mod", func(w http.ResponseWriter, r *http.Request) {
		w.Write(modV2)
	})
	mux.HandleFunc("/example.com/mod/@v/v1.0.1.info", func(w http.ResponseWriter, r *http.Request) {
		w.Write(infoV2)
	})
	mux.HandleFunc("/example.com/mod/@v/v1.0.1.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipV2)
	})
	return httptest.NewServer(mux)
}

func TestEndToEndGoToolchain(t *testing.T) {
	proxy := newUpstreamServer(t)
	defer proxy.Close()

	host = stringPtr("goclone.example.com")
	upstream = stringPtr(proxy.URL)

	mux := http.NewServeMux()
	mux.HandleFunc("/_mod/", proxyHandler)
	mux.HandleFunc("/", indexHandler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	clientDir := t.TempDir()
	modCache := t.TempDir()

	modPath := *host + "/example.com/mod"
	goMod := fmt.Sprintf("module client\n\ngo 1.20\n\nrequire %s v1.0.0\n", modPath)
	if err := os.WriteFile(filepath.Join(clientDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	mainSrc := fmt.Sprintf("package main\nimport _ %q\nfunc main(){}\n", modPath+"/pkg")
	if err := os.WriteFile(filepath.Join(clientDir, "main.go"), []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"GOMODCACHE="+modCache,
		"GOPROXY="+srv.URL+"/_mod",
		"GOSUMDB=off",
		"GOFLAGS=-buildvcs=false",
	)

	cmd := exec.Command("go", "mod", "download", "all")
	cmd.Env = env
	cmd.Dir = clientDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod download failed: %v\n%s", err, out)
	}

	cmd = exec.Command("go", "build", ".")
	cmd.Env = env
	cmd.Dir = clientDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	modFile := filepath.Join(modCache, filepath.FromSlash("goclone.example.com/example.com/mod@v1.0.0"), "go.mod")
	data, err := os.ReadFile(modFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(modPath)) {
		t.Fatalf("rewrite missing in go.mod: %s", data)
	}

	useFile := filepath.Join(modCache, filepath.FromSlash("goclone.example.com/example.com/mod@v1.0.0/use/use.go"))
	data, err = os.ReadFile(useFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(modPath+"/pkg")) {
		t.Fatalf("rewrite missing in use.go: %s", data)
	}
}
