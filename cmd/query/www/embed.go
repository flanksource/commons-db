// Package www embeds the built clicky-ui single-page app and serves it with an
// index.html fallback for client-side routes. The real assets are produced by
// `vite build` into dist/, which is gitignored: run `make ui-build` (or
// `task www:build`) before a source checkout compiles. Release tags carry the
// built dist/ (the release workflow force-adds it) so module consumers compile.
package www

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
)

//go:embed dist
var distFS embed.FS

// baseTag is index.html's <base>, which the app resolves its assets, its API
// and its routes against. Serving the page rewrites it to the path the app is
// mounted at, so one build serves at / and under a host's prefix alike.
const baseTag = `<base href="/" />`

// Handler returns an http.Handler that serves the embedded SPA. Existing files
// under dist/ are served directly; unknown non-asset paths fall back to
// index.html so client-side routing works.
//
// The app may be mounted under a prefix the host strips (http.StripPrefix):
// index.html is then served with its <base> at that prefix, read from the
// difference between the path the client asked for and the one left here.
func Handler() (http.Handler, error) {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(index, []byte(baseTag)) {
		return nil, fmt.Errorf("query app index.html has no %s to mount it by; rebuild www", baseTag)
	}
	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" && p != "index.html" {
			if f, err := dist.Open(p); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(p, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		serveIndex(w, r, index)
	}), nil
}

func serveIndex(w http.ResponseWriter, r *http.Request, index []byte) {
	mount, err := mountPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page := bytes.Replace(index, []byte(baseTag), []byte(`<base href="`+html.EscapeString(mount)+`/" />`), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(page)
}

// mountPath is the prefix a host stripped before r reached the app: what the
// client asked for, less the path left. Empty when the app is served at /.
func mountPath(r *http.Request) (string, error) {
	if r.RequestURI == "" {
		return "", nil
	}
	requested, err := url.ParseRequestURI(r.RequestURI)
	if err != nil {
		return "", fmt.Errorf("parse request URI %q: %w", r.RequestURI, err)
	}
	asked, left := requested.EscapedPath(), r.URL.EscapedPath()
	if !strings.HasSuffix(asked, left) {
		return "", fmt.Errorf("query app mounted at an unknown prefix: %q was asked for, %q is left", asked, left)
	}
	return strings.TrimSuffix(strings.TrimSuffix(asked, left), "/"), nil
}
