package httpproxy

import (
	"io"
	"sync"
	"unicode/utf8"

	tracehttp "github.com/flanksource/commons-db/tracing/http"
)

const streamOpenComment = "event stream still open when recorded"

// streamCapture tees an event stream's bytes as the client reads them. Such a
// stream never ends while its page is open, so it cannot be read ahead and
// replayed like a finite body: its exchange is emitted when the stream ends,
// or with whatever has passed when the capture ends first. Every byte counts
// toward the content's size, past the bodies cap that bounds its text.
type streamCapture struct {
	mu     sync.Mutex
	limit  int
	kept   []byte
	total  int64
	closed bool
	// ended is called once, when the stream ends.
	ended func(*streamCapture)
	once  sync.Once
}

func (c *streamCapture) record(chunk []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(chunk))
	if room := c.limit - len(c.kept); room > 0 {
		c.kept = append(c.kept, chunk[:min(room, len(chunk))]...)
	}
}

func (c *streamCapture) finish() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.once.Do(func() { c.ended(c) })
}

// apply fills exchange's response content from what has streamed so far.
func (c *streamCapture) apply(exchange *tracehttp.Exchange) {
	c.mu.Lock()
	defer c.mu.Unlock()
	content := &exchange.Response.Content
	content.Size = c.total
	kept := c.kept
	if c.total > int64(len(kept)) || !c.closed {
		kept = trimPartialRune(kept)
	}
	content.Text, exchange.Encoding = encodeBody(kept)
	content.Truncated = c.total > int64(len(c.kept))
	if !c.closed {
		exchange.Comment = joinComments(exchange.Comment, streamOpenComment)
	}
}

// trimPartialRune drops a UTF-8 rune split by the cap or a chunk boundary, so
// a cut-off prefix still stores as text instead of base64.
func trimPartialRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

type teeStream struct {
	io.ReadCloser
	capture *streamCapture
}

func (t teeStream) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	t.capture.record(p[:n])
	if err != nil {
		t.capture.finish()
	}
	return n, err
}

func (t teeStream) Close() error {
	t.capture.finish()
	return t.ReadCloser.Close()
}
