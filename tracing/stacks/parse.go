package stacks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/maruel/panicparse/v2/stack"
)

// goroutineHeader opens a goroutine's block in a Go dump.
var goroutineHeader = regexp.MustCompile(`^goroutine (\d+)\b`)

var createdIn = regexp.MustCompile(` in goroutine \d+$`)

// Records are the records of dump, each a copy of head: one per goroutine of
// a Go dump, innermost frame first, one for a native dump, or, for a Go dump
// that cannot be parsed, one failed record holding its text, with no format
// as every failed dump has.
func Records(head Stack, dump Dump) []Stack {
	head.TakenAt = dump.CollectedAt
	if dump.Format != FormatGo {
		head.Format, head.Raw = dump.Format, dump.Text
		return []Stack{head}
	}
	goroutines, err := parseGoroutines(dump.Text)
	if err != nil {
		head.Raw, head.Error = dump.Text, err.Error()
		return []Stack{head}
	}
	head.Format = FormatGo
	raws := goroutineBlocks(dump.Text)
	records := make([]Stack, 0, len(goroutines))
	for _, goroutine := range goroutines {
		record := head
		record.GoroutineID, record.State, record.Raw = goroutine.ID, goroutine.State, raws[goroutine.ID]
		record.WaitMinutes, record.LockedToThread = goroutine.SleepMax, goroutine.Locked
		record.Frames = frames(goroutine.Stack.Calls)
		if calls := goroutine.CreatedBy.Calls; len(calls) > 0 {
			record.CreatedBy = &frames(calls[:1])[0]
		}
		records = append(records, record)
	}
	return records
}

// parseGoroutines parses every goroutine of a Go dump, without reading the
// sources it names.
func parseGoroutines(text string) ([]*stack.Goroutine, error) {
	opts := &stack.Opts{}
	var goroutines []*stack.Goroutine
	for in := io.Reader(strings.NewReader(text)); ; {
		snapshot, suffix, err := stack.ScanSnapshot(in, io.Discard, opts)
		if snapshot != nil {
			goroutines = append(goroutines, snapshot.Goroutines...)
		}
		switch {
		case err != nil && !errors.Is(err, io.EOF):
			return nil, fmt.Errorf("parse goroutine dump: %w", err)
		case err != nil || len(suffix) == 0:
			if len(goroutines) == 0 {
				return nil, errors.New("parse goroutine dump: it holds no goroutine")
			}
			return goroutines, nil
		}
		in = bytes.NewReader(suffix)
	}
}

// goroutineBlocks are the text of each goroutine of a Go dump, by id: its
// header through the blank line that ends it.
func goroutineBlocks(text string) map[int]string {
	blocks := map[int]string{}
	id, start := 0, -1
	lines := strings.SplitAfter(text, "\n")
	flush := func(end int) {
		if start >= 0 {
			blocks[id] = strings.TrimRight(strings.Join(lines[start:end], ""), "\n")
		}
		start = -1
	}
	for index, line := range lines {
		if match := goroutineHeader.FindStringSubmatch(line); match != nil {
			flush(index)
			id, _ = strconv.Atoi(match[1])
			start = index
		} else if strings.TrimSpace(line) == "" {
			flush(index)
		}
	}
	flush(len(lines))
	return blocks
}

func frames(calls []stack.Call) []Frame {
	frames := make([]Frame, 0, len(calls))
	for _, call := range calls {
		// A creator's call ends with the goroutine it ran in, as Go 1.21 on
		// prints it ("created by main.main in goroutine 1").
		name := createdIn.ReplaceAllString(call.Func.Complete, "")
		frames = append(frames, Frame{Func: name, File: call.RemoteSrcPath, Line: call.Line})
	}
	return frames
}
