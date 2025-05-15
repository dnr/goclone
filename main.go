package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
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

// rewritePath returns the rewritten path according to the replacements map.
// If no replacement applies, the original path is returned unchanged.
func rewritePath(p string, repl map[string]string) string {
	best := ""
	for old := range repl {
		if p == old || strings.HasPrefix(p, old+"/") {
			if len(old) > len(best) {
				best = old
			}
		}
	}
	if best == "" {
		return p
	}
	return repl[best] + strings.TrimPrefix(p, best)
}

func rewriteFileName(name string, repl map[string]string) string {
	best := ""
	for old := range repl {
		if name == old || strings.HasPrefix(name, old+"/") || strings.HasPrefix(name, old+"@") {
			if len(old) > len(best) {
				best = old
			}
		}
	}
	if best == "" {
		return name
	}
	return strings.Replace(name, best, repl[best], 1)
}

func rewriteGoImports(src []byte, repl map[string]string) ([]byte, error) {
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
		newPath := rewritePath(path, repl)
		if newPath != path {
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

// recursiveDeps returns the paths of required modules that have the
// "goclone:recursive" comment.
func recursiveDeps(modData []byte) ([]string, error) {
	f, err := modfile.Parse("go.mod", modData, nil)
	if err != nil {
		return nil, err
	}
	var deps []string
	for _, r := range f.Require {
		for _, c := range r.Syntax.Suffix {
			if strings.Contains(c.Token, "goclone:recursive") {
				deps = append(deps, r.Mod.Path)
				break
			}
		}
	}
	return deps, nil
}

func extractGoModFromZip(data []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range r.File {
		if path.Base(f.Name) == "go.mod" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("go.mod not found")
}

func makeReplacements(userPath, upstreamPath string, modData []byte) (map[string]string, error) {
	repl := map[string]string{
		upstreamPath: fmt.Sprintf("%s/%s", *host, userPath),
	}
	deps, err := recursiveDeps(modData)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if strings.HasPrefix(userPath, "_") {
		segs := strings.SplitN(userPath, "/", 2)
		if len(segs) == 2 {
			prefix = segs[0]
		}
	}
	for _, d := range deps {
		newPath := fmt.Sprintf("%s/%s", *host, d)
		if prefix != "" {
			newPath = fmt.Sprintf("%s/%s/%s", *host, prefix, d)
		}
		repl[d] = newPath
	}
	return repl, nil
}

func rewriteGoMod(src []byte, repl map[string]string) ([]byte, error) {
	f, err := modfile.Parse("go.mod", src, nil)
	if err != nil {
		return nil, err
	}
	changed := false
	if f.Module != nil {
		newPath := rewritePath(f.Module.Mod.Path, repl)
		if newPath != f.Module.Mod.Path {
			if err := f.AddModuleStmt(newPath); err != nil {
				return nil, err
			}
			changed = true
		}
	}
	for _, r := range f.Require {
		newPath := rewritePath(r.Mod.Path, repl)
		if newPath != r.Mod.Path {
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

func rewriteZip(data []byte, repl map[string]string) ([]byte, error) {
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
			b, err = rewriteGoImports(b, repl)
			if err != nil {
				return nil, err
			}
		} else if path.Base(f.Name) == "go.mod" {
			b, err = rewriteGoMod(b, repl)
			if err != nil {
				return nil, err
			}
		}
		newName := rewriteFileName(f.Name, repl)
		if newName != f.Name {
			f.Name = newName
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

func parseProxyPath(p string) (userPath, upstreamPath, rest string, ok bool) {
	parts := strings.SplitN(p, "/@v/", 2)
	if len(parts) != 2 {
		return "", "", "", false
	}
	userPath = parts[0]
	rest = parts[1]
	upstreamPath = userPath
	if strings.HasPrefix(upstreamPath, "_") {
		segs := strings.SplitN(upstreamPath, "/", 2)
		if len(segs) != 2 || segs[0] == "_mod" {
			return "", "", "", false
		}
		upstreamPath = segs[1]
	}
	return userPath, upstreamPath, rest, true
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/_mod/")
	trimmed = strings.TrimPrefix(trimmed, *host+"/")
	userPath, upstreamPath, rest, ok := parseProxyPath(trimmed)
	if !ok {
		http.NotFound(w, r)
		return
	}
	upstreamURL := fmt.Sprintf("%s/%s/@v/%s", *upstream, upstreamPath, rest)
	resp, err := http.Get(upstreamURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	// Go's HTTP client automatically decompresses gzip responses. If the
	// upstream sent a gzip encoded body, resp.Body will already be
	// decompressed but the Content-Encoding header will still be present.
	// Strip it to avoid telling the client the body is gzip when it isn't.
	w.Header().Del("Content-Encoding")
	if strings.HasSuffix(trimmed, ".zip") || strings.HasSuffix(trimmed, ".mod") {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		var modData []byte
		if strings.HasSuffix(trimmed, ".mod") {
			modData = data
		} else {
			modData, err = extractGoModFromZip(data)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
		repl, err := makeReplacements(userPath, upstreamPath, modData)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if strings.HasSuffix(trimmed, ".zip") {
			data, err = rewriteZip(data, repl)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else { // .mod
			data, err = rewriteGoMod(data, repl)
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

// lambdaRequest is a minimal subset of the Lambda Function URL event.
type lambdaRequest struct {
	RawPath         string            `json:"rawPath"`
	RawQueryString  string            `json:"rawQueryString"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	IsBase64Encoded bool              `json:"isBase64Encoded"`
	RequestContext  struct {
		HTTP struct {
			Method string `json:"method"`
		} `json:"http"`
	} `json:"requestContext"`
}

type lambdaResponse struct {
	StatusCode      int               `json:"statusCode"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	IsBase64Encoded bool              `json:"isBase64Encoded"`
}

func handleLambda(req lambdaRequest) (lambdaResponse, error) {
	url := req.RawPath
	if req.RawQueryString != "" {
		url += "?" + req.RawQueryString
	}
	body := []byte(req.Body)
	if req.IsBase64Encoded {
		var err error
		body, err = base64.StdEncoding.DecodeString(req.Body)
		if err != nil {
			return lambdaResponse{}, err
		}
	}
	r, err := http.NewRequest(req.RequestContext.HTTP.Method, url, bytes.NewReader(body))
	if err != nil {
		return lambdaResponse{}, err
	}
	for k, v := range req.Headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(w, r)

	resp := lambdaResponse{
		StatusCode: w.Code,
		Headers:    map[string]string{},
	}
	for k, v := range w.Header() {
		if len(v) > 0 {
			resp.Headers[k] = v[0]
		}
	}
	respBody := w.Body.Bytes()
	ct := resp.Headers["Content-Type"]
	if !strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "json") {
		resp.Body = base64.StdEncoding.EncodeToString(respBody)
		resp.IsBase64Encoded = true
	} else {
		resp.Body = string(respBody)
	}
	return resp, nil
}

func postLambdaError(client *http.Client, api, id string, err error) {
	payload := map[string]string{"errorMessage": err.Error()}
	b, _ := json.Marshal(payload)
	client.Post("http://"+api+"/2018-06-01/runtime/invocation/"+id+"/error", "application/json", bytes.NewReader(b))
}

func lambdaLoop() {
	api := os.Getenv("AWS_LAMBDA_RUNTIME_API")
	client := &http.Client{}
	for {
		resp, err := client.Get("http://" + api + "/2018-06-01/runtime/invocation/next")
		if err != nil {
			log.Fatal(err)
		}
		id := resp.Header.Get("Lambda-Runtime-Aws-Request-Id")
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var event lambdaRequest
		if err := json.Unmarshal(data, &event); err != nil {
			postLambdaError(client, api, id, err)
			continue
		}
		out, err := handleLambda(event)
		if err != nil {
			postLambdaError(client, api, id, err)
			continue
		}
		b, _ := json.Marshal(out)
		_, err = client.Post("http://"+api+"/2018-06-01/runtime/invocation/"+id+"/response", "application/json", bytes.NewReader(b))
		if err != nil {
			log.Fatal(err)
		}
	}
}

func main() {
	flag.Parse()
	http.HandleFunc("/_mod/", proxyHandler)
	http.HandleFunc("/", indexHandler)
	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		lambdaLoop()
		return
	}
	log.Printf("listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
