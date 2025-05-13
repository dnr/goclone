package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

var (
	addr     = flag.String("addr", ":8080", "listen address")
	host     = flag.String("host", "goclone.zone", "public host for vanity imports")
	upstream = flag.String("upstream", "https://proxy.golang.org", "upstream module proxy")
)

func vanityHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("go-get") != "1" {
		http.NotFound(w, r)
		return
	}
	mod := strings.TrimPrefix(r.URL.Path, "/")
	html := fmt.Sprintf("<meta name=\"go-import\" content=\"%s/%s mod https://%s/_mod/\">", *host, mod, *host)
	fmt.Fprint(w, html)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || r.URL.Query().Get("go-get") == "1" {
		vanityHandler(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<html>
<body>
<h1>goclone</h1>
<p>See <a href="https://github.com/dnr/goclone">https://github.com/dnr/goclone</a> for usage instructions.</p>
</body>
</html>`)
}

func rewriteGoImports(src []byte, old, new string) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	changed := false
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if path == old || strings.HasPrefix(path, old+"/") {
			newPath := new + strings.TrimPrefix(path, old)
			imp.Path.Value = strconv.Quote(newPath)
			changed = true
		}
	}
	if !changed {
		return src, nil
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func rewriteGoMod(src []byte, old, new string) ([]byte, error) {
	f, err := modfile.Parse("go.mod", src, nil)
	if err != nil {
		return nil, err
	}
	changed := false
	if f.Module != nil {
		if f.Module.Mod.Path == old || strings.HasPrefix(f.Module.Mod.Path, old+"/") {
			newPath := new + strings.TrimPrefix(f.Module.Mod.Path, old)
			if err := f.AddModuleStmt(newPath); err != nil {
				return nil, err
			}
			changed = true
		}
	}
	for _, r := range f.Require {
		if r.Mod.Path == old || strings.HasPrefix(r.Mod.Path, old+"/") {
			newPath := new + strings.TrimPrefix(r.Mod.Path, old)
			r.Mod.Path = newPath
			if r.Syntax.InBlock {
				if len(r.Syntax.Token) >= 1 {
					r.Syntax.Token[0] = modfile.AutoQuote(newPath)
				}
				if len(r.Syntax.Token) >= 2 {
					r.Syntax.Token[1] = r.Mod.Version
				}
			} else {
				if len(r.Syntax.Token) >= 2 {
					r.Syntax.Token[1] = modfile.AutoQuote(newPath)
				}
				if len(r.Syntax.Token) >= 3 {
					r.Syntax.Token[2] = r.Mod.Version
				}
			}
			changed = true
		}
	}
	if !changed {
		return src, nil
	}
	b, err := f.Format()
	if err != nil {
		return nil, err
	}
	return b, nil
}

func rewriteZip(data []byte, old, new string) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(f.Name, ".go") {
			b, err = rewriteGoImports(b, old, new)
			if err != nil {
				return nil, err
			}
		} else if f.Name == "go.mod" {
			b, err = rewriteGoMod(b, old, new)
			if err != nil {
				return nil, err
			}
		}
		hdr := &zip.FileHeader{
			Name:   f.Name,
			Method: f.Method,
		}
		if fw, err := w.CreateHeader(hdr); err == nil {
			if _, err := fw.Write(b); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/_mod/")
	parts := strings.SplitN(trimmed, "/@v/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	modPath := parts[0]
	upstreamURL := fmt.Sprintf("%s/%s", *upstream, trimmed)
	resp, err := http.Get(upstreamURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	if strings.HasSuffix(trimmed, ".zip") || strings.HasSuffix(trimmed, ".mod") {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		newPath := fmt.Sprintf("%s/%s", *host, modPath)
		if strings.HasSuffix(trimmed, ".zip") {
			data, err = rewriteZip(data, modPath, newPath)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else { // .mod
			data, err = rewriteGoMod(data, modPath, newPath)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(resp.StatusCode)
		w.Write(data)
		return
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func main() {
	flag.Parse()
	http.HandleFunc("/_mod/", proxyHandler)
	http.HandleFunc("/", indexHandler)
	log.Printf("listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
