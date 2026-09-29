// The child process of the multi-process specs: the test binary re-executed
// with RECORDSTORE_CHILD set runs a script of steps against a store, printing
// one JSON line per step, and may crash or pause at RECORDSTORE_FAILPOINT.

//go:build unix

package owner_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/owner"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/spool"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// childScript is what a child runs: steps against the store at Path, whose
// sample kind is declared by Schema — "" as the conformance suite declares
// it, "extra" with an added column, "retyped" with count as a string.
type childScript struct {
	Path   string      `json:"path"`
	Build  string      `json:"build"`
	Schema string      `json:"schema,omitempty"`
	Steps  []childStep `json:"steps"`
}

type childStep struct {
	Op      string   `json:"op"`
	Stream  string   `json:"stream,omitempty"`
	Names   []string `json:"names,omitempty"`
	Seal    bool     `json:"seal,omitempty"`
	Wait    bool     `json:"wait,omitempty"`
	MaxRows int      `json:"maxRows,omitempty"`
	Role    string   `json:"role,omitempty"`
}

// childLine is a step's outcome; Sentinels names the store sentinels its
// error wraps.
type childLine struct {
	Op        string              `json:"op"`
	OK        bool                `json:"ok"`
	Window    *recordstore.Window `json:"window,omitempty"`
	Role      string              `json:"role,omitempty"`
	PID       int                 `json:"pid,omitempty"`
	Sentinels []string            `json:"sentinels,omitempty"`
	Error     string              `json:"error,omitempty"`
}

func TestMain(m *testing.M) {
	if script := os.Getenv("RECORDSTORE_CHILD"); script != "" {
		os.Exit(runChild(script))
	}
	os.Exit(m.Run())
}

// childSchema resolves the sample kind as variant declares it.
func childSchema(variant string) recordstore.SchemaResolver {
	return func(kind string) (recordstore.KindSchema, error) {
		schema, err := recordstoretest.Schema(kind)
		if err != nil || kind != recordstoretest.Kind {
			return schema, err
		}
		columns := append([]query.ColumnDef(nil), schema.Columns...)
		switch variant {
		case "extra":
			columns = append(columns, query.ColumnDef{Name: "region", Type: query.ColumnTypeString})
		case "retyped":
			for index := range columns {
				if columns[index].Name == "count" {
					columns[index].Type = query.ColumnTypeString
				}
			}
		}
		schema.Columns = columns
		return schema, nil
	}
}

// childOptions opens the store at path over sqlite, polling the spool fast
// so the specs do not wait on it.
func childOptions(path, build, variant string) owner.Options[*sqlite.Backend] {
	return owner.Options[*sqlite.Backend]{
		Path: path, Build: build, Store: sqlite.VersionedPath(path), CatalogVersion: sqlite.CatalogVersion,
		StartWait: 20 * time.Second, Poll: 100 * time.Millisecond,
		Open: func(_ context.Context, readOnly bool, submit recordstore.Submitter) (*sqlite.Backend, error) {
			return sqlite.Open(sqlite.Options{
				Path: path, Schema: childSchema(variant), TTL: recordstoretest.TTL, SweepInterval: time.Hour,
				ReadOnly: readOnly, Submit: submit,
			})
		},
	}
}

// childRow is a sample row named name, as the variant declares the kind.
func childRow(variant, name string) recordstore.Row {
	row := recordstore.Row{"name": name, "count": 1}
	switch variant {
	case "extra":
		row["region"] = "eu"
	case "retyped":
		row["count"] = "one"
	}
	return row
}

func runChild(encoded string) int {
	var script childScript
	if err := json.Unmarshal([]byte(encoded), &script); err != nil {
		fmt.Fprintln(os.Stderr, "decode child script:", err)
		return 2
	}
	stdin := bufio.NewReader(os.Stdin)
	awaitParent := func() { _, _ = stdin.ReadString('\n') }
	if at := os.Getenv("RECORDSTORE_FAILPOINT"); at != "" {
		name, pause := strings.CutSuffix(at, ":pause")
		owner.SetFailpoint(func(reached string) {
			if reached != name {
				return
			}
			emit(childLine{Op: "failpoint " + reached, OK: true})
			if pause {
				awaitParent()
				return
			}
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		})
	}
	ctx := context.Background()
	var store *owner.Store[*sqlite.Backend]
	defer func() {
		if store != nil {
			emit(result("close", store.Close()))
		}
	}()
	for _, step := range script.Steps {
		switch step.Op {
		case "open":
			opened, err := owner.Open(ctx, childOptions(script.Path, script.Build, script.Schema))
			line := result(step.Op, err)
			if err == nil {
				store, line.Role = opened, string(opened.Role())
			}
			emit(line)
		case "append":
			backend, err := store.Backend()
			var appended recordstore.AppendResult
			if err == nil {
				rows := make([]recordstore.Row, len(step.Names))
				for index, name := range step.Names {
					rows[index] = childRow(script.Schema, name)
				}
				appended, err = backend.Append(ctx, step.Stream, recordstoretest.Kind, rows)
			}
			line := result(step.Op, err)
			if err == nil {
				line.Window = &appended.Window
			}
			emit(line)
		case "write":
			emit(result(step.Op, childWrite(ctx, store, script.Schema, step)))
		case "role":
			deadline := time.Now().Add(20 * time.Second)
			for string(store.Role()) != step.Role && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			emit(childLine{Op: step.Op, OK: string(store.Role()) == step.Role, Role: string(store.Role())})
		case "exclusive":
			release, err := owner.Exclusive(script.Path, script.Build)
			emit(result(step.Op, err))
			if err == nil {
				defer func() { _ = release() }()
			}
		case "hold":
			line := childLine{Op: step.Op, OK: true, PID: os.Getpid()}
			if store != nil {
				line.Role = string(store.Role())
			}
			emit(line)
			awaitParent()
		default:
			emit(childLine{Op: step.Op, Error: "unknown step"})
			return 2
		}
	}
	return 0
}

// childWrite gathers step's rows through a store Writer and flushes them.
func childWrite(ctx context.Context, store *owner.Store[*sqlite.Backend], variant string, step childStep) error {
	writer, err := store.Writer(childSchema(variant), spool.WriterOptions{MaxRows: step.MaxRows})
	if err != nil {
		return err
	}
	for _, name := range step.Names {
		if err := writer.Append(ctx, step.Stream, recordstoretest.Kind, []recordstore.Row{childRow(variant, name)}); err != nil {
			return err
		}
	}
	if step.Seal {
		if err := writer.Seal(ctx, step.Stream); err != nil {
			return err
		}
	}
	return writer.Flush(ctx, owner.FlushOptions{Wait: step.Wait})
}

func result(op string, err error) childLine {
	if err == nil {
		return childLine{Op: op, OK: true}
	}
	line := childLine{Op: op, Error: err.Error()}
	for name, sentinel := range map[string]error{
		"sealed": recordstore.ErrSealed, "not_found": recordstore.ErrNotFound, "conflict": recordstore.ErrSchemaConflict,
		"locked": owner.ErrLocked, "pending": owner.ErrPending,
	} {
		if errors.Is(err, sentinel) {
			line.Sentinels = append(line.Sentinels, name)
		}
	}
	return line
}

func emit(line childLine) {
	encoded, _ := json.Marshal(line)
	fmt.Println(string(encoded))
}
