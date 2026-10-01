package gorm

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

const SQLProfileFileEnv = "SQL_PROFILE_FILE"

type SQLProfileEvent struct {
	SQL        string   `json:"sql"`
	Params     []string `json:"params"`
	DurationNS int64    `json:"duration_ns"`
	Rows       int64    `json:"rows"`
	Slow       bool     `json:"slow"`
	Error      bool     `json:"error"`
}

type sqlProfileWriter struct {
	path      string
	mu        sync.Mutex
	disabled  bool
	traceMu   sync.Mutex
	captureMu sync.Mutex
	current   *sqlProfileCapture
}

type sqlProfileCapture struct {
	SQL    string
	Params []string
}

func (w *sqlProfileWriter) capture(fc func() (string, int64)) (SQLProfileEvent, int64) {
	w.traceMu.Lock()
	defer w.traceMu.Unlock()
	w.captureMu.Lock()
	w.current = &sqlProfileCapture{}
	w.captureMu.Unlock()
	defer func() {
		w.captureMu.Lock()
		w.current = nil
		w.captureMu.Unlock()
	}()
	formattedSQL, rows := fc()
	w.captureMu.Lock()
	captured := *w.current
	w.captureMu.Unlock()
	if captured.SQL == "" {
		captured.SQL = formattedSQL
	}
	if captured.Params == nil {
		captured.Params = []string{}
	}
	return SQLProfileEvent{SQL: captured.SQL, Params: captured.Params}, rows
}

func (w *sqlProfileWriter) captureParams(sql string, params []interface{}) {
	w.captureMu.Lock()
	defer w.captureMu.Unlock()
	if w.current == nil {
		return
	}
	w.current.SQL = sql
	w.current.Params = make([]string, len(params))
	for i, param := range params {
		w.current.Params[i] = fmt.Sprint(param)
	}
}

func newSQLProfileWriter(path string) (*sqlProfileWriter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open SQL profile %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close SQL profile %s: %w", path, err)
	}
	return &sqlProfileWriter{path: path}, nil
}

// write appends event to the profile. Profiling is diagnostic, so the first
// failure disables the writer for the rest of the process instead of failing
// every later query with the same error.
func (w *sqlProfileWriter) write(event SQLProfileEvent) (err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.disabled {
		return nil
	}
	defer func() {
		if err != nil {
			w.disabled = true
			err = fmt.Errorf("%w; SQL profiling is disabled for the rest of this process", err)
		}
	}()
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode SQL profile event: %w", err)
	}
	file, err := os.OpenFile(w.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open SQL profile %s: %w", w.path, err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("write SQL profile %s: %w", w.path, err)
	}
	return file.Close()
}
