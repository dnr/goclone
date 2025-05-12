package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
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
		if strings.HasSuffix(f.Name, ".go") || f.Name == "go.mod" {
			b = []byte(strings.ReplaceAll(string(b), old, new))
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
			data = []byte(strings.ReplaceAll(string(data), modPath, newPath))
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
	http.HandleFunc("/", vanityHandler)
	log.Printf("listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
