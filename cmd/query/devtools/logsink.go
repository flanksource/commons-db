package devtools

// The process-wide log tail.
//
// This is the coarser of the two log sources and says so on every line it
// produces. An armed request captures its own lines structurally, with their
// fields intact and the operation they belong to attached; the tail can only
// read what the process logger already wrote, which by then is rendered text.
// The tail exists for what no request owns — background reconciles, snapshot
// refreshes, and requests nobody armed.

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons/logger"
)

// TeeProcessLogs sends every line the process logs to store as well as to
// wherever it was already going, and returns a function restoring the previous
// writer.
//
// logger.SetOutput is global and atomic and affects every named logger, existing
// and future, which is what makes this reach background work. The original
// writer stays first in the chain, so the operator's terminal is byte-for-byte
// unchanged.
func TeeProcessLogs(store *Store) func() {
	previous := logger.GetOutput()
	logger.SetOutput(io.MultiWriter(previous, &logSink{store: store}))
	return func() { logger.SetOutput(previous) }
}

// logSink turns a byte stream into lines. A logger writes a record per Write in
// practice, but nothing promises that, so partial writes are buffered until a
// newline rather than filed as truncated lines.
type logSink struct {
	store *Store

	mu      sync.Mutex
	pending bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending.Write(p)
	for {
		line, err := s.pending.ReadString('\n')
		if err != nil {
			// No newline yet: put the fragment back and wait for the rest.
			s.pending.Reset()
			s.pending.WriteString(line)
			break
		}
		if trimmed := strings.TrimRight(line, "\r\n"); strings.TrimSpace(trimmed) != "" {
			s.store.Log(processLine(trimmed))
		}
	}
	return len(p), nil
}

// levelPattern finds the level a rendered log line reports, so the console can
// filter by it. A line whose level cannot be read is reported as info rather
// than dropped — losing a line to a formatting change nobody anticipated is
// worse than mislabelling one.
var levelPattern = regexp.MustCompile(`(?i)\b(FATAL|ERROR|WARN(?:ING)?|INFO|DEBUG|TRACE[1-4]?)\b`)

func processLine(text string) query.LogLine {
	level := "info"
	if match := levelPattern.FindStringSubmatch(stripANSI(text)); match != nil {
		level = strings.ToLower(match[1])
		if level == "warning" {
			level = "warn"
		}
	}
	return query.LogLine{
		Time: time.Now(), Level: level, Source: "process", Message: text,
	}
}

// ansiPattern matches SGR escapes. The message keeps them — the console renders
// ANSI — but the level has to be read from the text underneath.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(text string) string { return ansiPattern.ReplaceAllString(text, "") }
