package localfiles

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FileEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Problem   string `json:"problem,omitempty"`
}

type Listing struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent"`
	Entries []FileEntry `json:"entries"`
}

// Handler serves metadata only. It deliberately ignores forwarded client addresses.
func Handler(prefix string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != strings.TrimRight(prefix, "/")+"/connections/files" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if !localRequest(r) {
			http.Error(w, "file browsing requires local access", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", 405)
			return
		}
		listing, err := list(r.URL.Query().Get("path"))
		if err != nil {
			status := http.StatusUnprocessableEntity
			if errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			if errors.Is(err, os.ErrPermission) {
				status = http.StatusForbidden
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(listing)
	})
}

func localRequest(r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(peer) == nil || !net.ParseIP(peer).IsLoopback() {
		return false
	}
	host := r.Host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	if !localHost(host) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !localHost(u.Hostname()) {
			return false
		}
	}
	return true
}

func localHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

func list(path string) (Listing, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Listing{}, err
		}
		path = home
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Listing{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Listing{}, err
	}
	if !info.IsDir() {
		absolute = filepath.Dir(absolute)
	}
	files, err := os.ReadDir(absolute)
	if err != nil {
		return Listing{}, err
	}
	result := Listing{Path: absolute, Parent: filepath.Dir(absolute), Entries: []FileEntry{}}
	for _, file := range files {
		entry := FileEntry{Name: file.Name(), Path: filepath.Join(absolute, file.Name())}
		target, err := os.Stat(entry.Path)
		if err != nil {
			entry.Problem = err.Error()
		} else if !target.IsDir() && !target.Mode().IsRegular() {
			continue
		} else {
			entry.Directory = target.IsDir()
		}
		result.Entries = append(result.Entries, entry)
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if a.Directory != b.Directory {
			return a.Directory
		}
		return a.Name < b.Name
	})
	return result, nil
}
