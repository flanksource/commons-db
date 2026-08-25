package shell

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
)

const redactedValue = "[REDACTED]"

type redactingWriter struct {
	destination io.Writer
	secrets     [][]byte
	maxLength   int
	pending     []byte
}

type redactingReadCloser struct {
	source      io.ReadCloser
	redactor    *redactingWriter
	output      bytes.Buffer
	terminalErr error
}

func newRedactingWriter(destination io.Writer, values []string) *redactingWriter {
	writer := &redactingWriter{destination: destination}
	for _, value := range uniqueValues(values) {
		secret := []byte(value)
		writer.secrets = append(writer.secrets, secret)
		if len(secret) > writer.maxLength {
			writer.maxLength = len(secret)
		}
	}
	return writer
}

func redactingWriterFor(destination io.Writer, values []string) io.Writer {
	if destination == nil {
		return nil
	}
	return newRedactingWriter(destination, values)
}

func newRedactingReadCloser(source io.ReadCloser, values []string) io.ReadCloser {
	reader := &redactingReadCloser{source: source}
	reader.redactor = newRedactingWriter(&reader.output, values)
	return reader
}

func (r *redactingReadCloser) Read(output []byte) (int, error) {
	for r.output.Len() == 0 && r.terminalErr == nil {
		buffer := make([]byte, 32*1024)
		read, readErr := r.source.Read(buffer)
		if read > 0 {
			_, _ = r.redactor.Write(buffer[:read])
		}
		if readErr != nil {
			r.terminalErr = readErr
			if closeErr := r.redactor.Close(); closeErr != nil {
				r.terminalErr = fmt.Errorf("redact artifact: %w", closeErr)
			}
		}
	}
	if r.output.Len() > 0 {
		return r.output.Read(output)
	}
	return 0, r.terminalErr
}

func (r *redactingReadCloser) Close() error {
	return r.source.Close()
}

func (w *redactingWriter) Write(input []byte) (int, error) {
	w.pending = append(w.pending, input...)
	if w.maxLength == 0 {
		_, err := w.destination.Write(w.pending)
		w.pending = nil
		return len(input), err
	}
	for len(w.pending) >= w.maxLength {
		matched := false
		for _, secret := range w.secrets {
			if bytes.HasPrefix(w.pending, secret) {
				if _, err := io.WriteString(w.destination, redactedValue); err != nil {
					return len(input), err
				}
				w.pending = w.pending[len(secret):]
				matched = true
				break
			}
		}
		if !matched {
			if _, err := w.destination.Write(w.pending[:1]); err != nil {
				return len(input), err
			}
			w.pending = w.pending[1:]
		}
	}
	return len(input), nil
}

func (w *redactingWriter) Close() error {
	if len(w.pending) == 0 {
		return nil
	}
	_, err := io.WriteString(w.destination, redactText(string(w.pending), byteValues(w.secrets)))
	w.pending = nil
	return err
}

func closeRedactors(writers ...io.Writer) error {
	for _, writer := range writers {
		if closer, ok := writer.(*redactingWriter); ok {
			if err := closer.Close(); err != nil {
				return fmt.Errorf("flush redacted output: %w", err)
			}
		}
	}
	return nil
}

func redactText(value string, secrets []string) string {
	secrets = uniqueValues(secrets)
	if len(secrets) == 0 {
		return value
	}
	replacements := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		replacements = append(replacements, secret, redactedValue)
	}
	return strings.NewReplacer(replacements...).Replace(value)
}

func uniqueValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || value == redactedValue {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return len(result[i]) > len(result[j]) })
	return result
}

func byteValues(values [][]byte) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}
