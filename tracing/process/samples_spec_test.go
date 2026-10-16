package process_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/process"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

// stored are the facet count and list columns a sample holds a value in.
func stored(row recordstore.Row) []string {
	var columns []string
	for _, column := range []string{"openFiles", "sockets", "ports", "connections", "openFileList", "socketList", "portList", "connectionList"} {
		if row[column] != nil {
			columns = append(columns, column)
		}
	}
	return columns
}

func startSamples(env tracingtest.Env, rootPID int, facets string) *query.ManagedSession {
	managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{
		Kind: process.SamplesKind, Params: json.RawMessage(fmt.Sprintf(`{"rootPid": %d, "interval": %s, "facets": %s}`, rootPID, interval, facets)),
	})
	Expect(err).ToNot(HaveOccurred())
	tracingtest.Running(managed.Session())
	return managed
}

// samplesOf are the committed samples of the process at pid, in tick order.
func samplesOf(env tracingtest.Env, managed *query.ManagedSession, pid int) []recordstore.Row {
	var rows []recordstore.Row
	Expect(env.Results.Backend.Scan(context.Background(), managed.Session().Snapshot().Events.Stream, 0, func(_ int64, row recordstore.Row) error {
		if row["pid"] == int64(pid) {
			rows = append(rows, row)
		}
		return nil
	})).To(Succeed())
	return rows
}

func sampled(env tracingtest.Env, managed *query.ManagedSession, pid int) func() int {
	return func() int { return len(samplesOf(env, managed, pid)) }
}

func names(list any) []string {
	var found []string
	for _, descriptor := range list.([]any) {
		found = append(found, descriptor.(map[string]any)["name"].(string))
	}
	return found
}

var _ = Describe("stopping a process capture mid-tick", func() {
	It("ends the tree and the samples without an error however the stop lands", func() {
		child := tracingtest.StartGoChild(goChild, "-listen")
		env := newEnv()
		var errs []string
		for attempt := range 8 {
			tree := env.Start(process.TreeKind, fmt.Sprintf(`{"rootPid": %d, "interval": %s}`, child.PID, interval))
			samples := startSamples(env, child.PID, `["openFiles", "sockets", "ports", "connections"]`).Session()
			time.Sleep(time.Duration(attempt) * 15 * time.Millisecond)
			tree.Stop("spec")
			samples.Stop("spec")
			for _, session := range []*query.Session{tree, samples} {
				if info := tracingtest.Ended(session); info.Error != "" {
					errs = append(errs, info.Error)
				}
			}
		}
		Expect(errs).To(BeEmpty())
	})
})

var _ = Describe("process_samples facets", func() {
	It("counts every armed facet each tick, and stores a list only when it changes", func() {
		dir := GinkgoT().TempDir()
		held, opened := filepath.Join(dir, "held.txt"), filepath.Join(dir, "opened.txt")
		child := tracingtest.StartGoChild(goChild, "-hold", held, "-listen")
		env := newEnv()
		managed := startSamples(env, child.PID, `["openFiles", "sockets", "ports", "connections"]`)
		Eventually(sampled(env, managed, child.PID)).WithTimeout(5 * time.Second).Should(BeNumerically(">=", 3))
		_, err := fmt.Fprintln(child.Stdin, "open "+opened)
		Expect(err).ToNot(HaveOccurred())
		child.Await("opened")
		Eventually(sampled(env, managed, child.PID)).WithTimeout(5 * time.Second).Should(BeNumerically(">=", 6))
		managed.Session().Stop("spec")
		tracingtest.Ended(managed.Session())

		rows := samplesOf(env, managed, child.PID)
		first := rows[0]
		Expect(first).To(And(
			HaveKeyWithValue("ports", int64(1)), HaveKeyWithValue("connections", int64(2)),
			HaveKeyWithValue("sockets", BeNumerically(">=", 3)), HaveKeyWithValue("openFiles", BeNumerically(">=", 1)),
			HaveKeyWithValue("diskReadBytes", Not(BeNil())), HaveKeyWithValue("diskWriteBytes", Not(BeNil())),
		))
		Expect(names(first["openFileList"])).To(ContainElement(HaveSuffix("held.txt")))
		Expect(names(first["portList"])).To(ConsistOf(child.Announced["listen"]))
		for _, row := range rows {
			Expect(stored(row)).To(ContainElements(process.Facets), "counted at tick %v", row["tick"])
		}
		var changed []recordstore.Row
		for _, row := range rows[1:] {
			Expect(stored(row)).ToNot(ContainElements(Or(Equal("socketList"), Equal("portList"), Equal("connectionList"))), "unchanged at tick %v", row["tick"])
			if row["openFileList"] != nil {
				changed = append(changed, row)
			}
		}
		Expect(changed).To(HaveLen(1), "stored once more, as the child opened a file")
		Expect(names(changed[0]["openFileList"])).To(ContainElement(HaveSuffix("opened.txt")))
		Expect(changed[0]["openFiles"]).To(Equal(first["openFiles"].(int64) + 1))
	})

	It("reads nothing of the facets left unarmed, and a checkpoint by key lists the armed ones in full", func() {
		child := tracingtest.StartGoChild(goChild, "-listen")
		env := newEnv()
		managed := startSamples(env, child.PID, `["ports"]`)
		Eventually(sampled(env, managed, child.PID)).WithTimeout(5 * time.Second).Should(BeNumerically(">=", 2))
		key := samplesOf(env, managed, child.PID)[0]["key"].(string)
		Expect(env.Runtime.Checkpoint(tracing.WithCheckpointParams(context.Background(), json.RawMessage(`{"key": "`+key+`"}`)), managed)).To(Succeed())
		rows := samplesOf(env, managed, child.PID)
		var listed []any
		for _, row := range rows {
			if row["portList"] != nil {
				listed = append(listed, row["portList"])
			}
		}
		Expect(listed).To(HaveExactElements(HaveLen(1), HaveLen(1)), "listed at the first tick, then again at the checkpoint's")
		for _, row := range rows {
			Expect(row).To(HaveKeyWithValue("ports", int64(1)))
			Expect(stored(row)).To(BeElementOf([]string{"ports"}, []string{"ports", "portList"}), "nothing of the unarmed facets")
		}

		err := env.Runtime.Checkpoint(tracing.WithCheckpointParams(context.Background(), json.RawMessage(`{"key": "1@1"}`)), managed)
		Expect(err).To(MatchError(tracing.ErrCheckpointParams))
		Expect(err).To(MatchError(ContainSubstring(`no live process "1@1"`)))
	})

	It("refuses a facet it does not read", func() {
		_, err := newEnv().Runtime.Start(dbcontext.New(), tracing.StartRequest{
			Kind: process.SamplesKind, Params: json.RawMessage(`{"facets": ["environment"], "rootPid": ` + strconv.Itoa(1) + `}`),
		})
		Expect(err).To(MatchError(tracing.ErrInvalidParams))
		Expect(err).To(MatchError(ContainSubstring(`facet "environment"`)))
	})
})
