// The multi-process matrix: real child processes owning, reading, crashing
// and taking over one store, checked for exactly-once, in-order writes.

//go:build unix

package owner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/owner"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/spool"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// childWait is how long a spec waits for a child to report a step.
const childWait = 20 * time.Second

type child struct {
	session *gexec.Session
	stdin   io.WriteCloser
}

// startChild runs script in a child process, crashing or pausing at
// failpoint when one is named.
func startChild(script childScript, failpoint string) *child {
	encoded, err := json.Marshal(script)
	Expect(err).ToNot(HaveOccurred())
	command := exec.Command(os.Args[0])
	command.Env = append(os.Environ(), "RECORDSTORE_CHILD="+string(encoded), "RECORDSTORE_FAILPOINT="+failpoint)
	stdin, err := command.StdinPipe()
	Expect(err).ToNot(HaveOccurred())
	session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
	Expect(err).ToNot(HaveOccurred())
	started := &child{session: session, stdin: stdin}
	DeferCleanup(func() {
		_ = stdin.Close()
		session.Kill().Wait(childWait)
	})
	return started
}

// lines are the steps the child reported so far.
func (c *child) lines() []childLine {
	var lines []childLine
	for _, text := range strings.Split(string(c.session.Out.Contents()), "\n") {
		var line childLine
		if json.Unmarshal([]byte(text), &line) == nil && line.Op != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// await waits for the child to report its count-th step named op, and
// returns it.
func (c *child) await(op string, count int) childLine {
	var found childLine
	Eventually(func() bool {
		seen := 0
		for _, line := range c.lines() {
			if line.Op == op {
				seen++
				if seen == count {
					found = line
					return true
				}
			}
		}
		return false
	}, childWait).Should(BeTrue(), "child never reported %s #%d; it reported %+v", op, count, c.lines())
	return found
}

// resume lets a child past a hold step or a paused failpoint.
func (c *child) resume() {
	_, err := fmt.Fprintln(c.stdin)
	Expect(err).ToNot(HaveOccurred())
}

func (c *child) kill() {
	Expect(c.session.Command.Process.Signal(syscall.SIGKILL)).To(Succeed())
	Eventually(c.session, childWait).Should(gexec.Exit())
}

// finish closes the child's stdin, releasing every hold, and waits for it to
// exit cleanly. A child that already exited had its stdin closed with it.
func (c *child) finish() {
	_ = c.stdin.Close()
	Eventually(c.session, childWait).Should(gexec.Exit(0))
}

var _ = Describe("stores shared by processes", func() {
	var (
		ctx  context.Context
		path string
	)

	script := func(schema string, steps ...childStep) childScript {
		return childScript{Path: path, Build: "child", Schema: schema, Steps: steps}
	}
	openHere := func() *owner.Store[*sqlite.Backend] {
		store, err := owner.Open(ctx, childOptions(path, "parent", ""))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(store.Close)
		return store
	}
	backendOf := func(store *owner.Store[*sqlite.Backend]) *sqlite.Backend {
		backend, err := store.Backend()
		Expect(err).ToNot(HaveOccurred())
		return backend
	}
	names := func(backend recordstore.Backend, stream string) []string {
		var names []string
		err := backend.Scan(ctx, stream, 0, func(_ int64, row recordstore.Row) error {
			names = append(names, row["name"].(string))
			return nil
		})
		if !errors.Is(err, recordstore.ErrNotFound) {
			Expect(err).ToNot(HaveOccurred())
		}
		return names
	}
	rows := func(names ...string) []recordstore.Row {
		var rows []recordstore.Row
		for _, name := range names {
			rows = append(rows, childRow("", name))
		}
		return rows
	}
	spooled := func() spool.Dir {
		dir, err := spool.OpenDir(path + ".spool")
		Expect(err).ToNot(HaveOccurred())
		return dir
	}
	publish := func(id string, entries ...recordstore.BatchEntry) {
		schema, err := recordstoretest.Schema(recordstoretest.Kind)
		Expect(err).ToNot(HaveOccurred())
		_, err = spooled().Publish(recordstore.Batch{
			ID: id, Producer: recordstore.Producer{Instance: "raw-" + id, Seq: 1}, Schemas: []recordstore.KindSchema{schema}, Entries: entries,
		}, spool.FormatNDJSON)
		Expect(err).ToNot(HaveOccurred())
	}
	appendTo := func(stream string, names ...string) recordstore.BatchEntry {
		return recordstore.BatchEntry{Op: recordstore.BatchAppend, Stream: stream, Kind: recordstoretest.Kind, Rows: rows(names...)}
	}

	BeforeEach(func() {
		ctx = context.Background()
		path = filepath.Join(GinkgoT().TempDir(), "records.sqlite")
	})

	It("1: takes over from an owner killed between appends, keeping the windows contiguous", func() {
		owning := startChild(script("", childStep{Op: "open"}, childStep{Op: "hold"}), "")
		Expect(owning.await("open", 1).Role).To(Equal("owner"))
		reader := openHere()
		Expect(reader.Role()).To(Equal(owner.RoleReader))
		first, err := backendOf(reader).Append(ctx, "run-1", recordstoretest.Kind, rows("a", "b"))
		Expect(err).ToNot(HaveOccurred())

		owning.kill()
		second, err := backendOf(reader).Append(ctx, "run-1", recordstoretest.Kind, rows("c", "d"))
		Expect(err).ToNot(HaveOccurred())

		Expect([]recordstore.Window{first.Window, second.Window}).To(Equal([]recordstore.Window{{From: 1, To: 2}, {From: 3, To: 4}}))
		Expect(reader.Role()).To(Equal(owner.RoleOwner))
		Expect(names(backendOf(reader), "run-1")).To(Equal([]string{"a", "b", "c", "d"}))
	})

	It("2: applies the appends of a short-lived child while the owner runs", func() {
		store := openHere()
		writing := startChild(script("", childStep{Op: "open"}, childStep{Op: "append", Stream: "run-1", Names: []string{"a", "b"}}), "")

		Expect(writing.await("open", 1).Role).To(Equal("reader"))
		Expect(writing.await("append", 1).Window).To(Equal(&recordstore.Window{From: 1, To: 2}))
		writing.finish()
		Expect(names(backendOf(store), "run-1")).To(Equal([]string{"a", "b"}))
	})

	It("3: drains a backlog left with no owner, then lets the store go", func() {
		publish("b-1", appendTo("run-1", "a"))
		publish("b-2", appendTo("run-2", "b"))
		draining := startChild(script("", childStep{Op: "open"}), "")
		Expect(draining.await("open", 1).Role).To(Equal("owner"))
		draining.finish()

		store := openHere()
		Expect(store.Role()).To(Equal(owner.RoleOwner), "the child released the store")
		Expect(names(backendOf(store), "run-1")).To(Equal([]string{"a"}))
		Expect(names(backendOf(store), "run-2")).To(Equal([]string{"b"}))
	})

	It("4: does not apply a batch twice when the owner dies between committing it and trashing it", func() {
		owning := startChild(script("", childStep{Op: "open"}, childStep{Op: "hold"}), "after-ledger-commit")
		owning.await("open", 1)
		reader := openHere()

		result, err := backendOf(reader).Append(ctx, "run-1", recordstoretest.Kind, rows("a"))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Window).To(Equal(recordstore.Window{From: 1, To: 1}))
		owning.await("failpoint after-ledger-commit", 1)
		Eventually(owning.session, childWait).Should(gexec.Exit(128 + int(syscall.SIGKILL)))
		Eventually(reader.Role, childWait).Should(Equal(owner.RoleOwner))
		Eventually(func() ([]string, error) { return spooled().Incoming() }, childWait).Should(BeEmpty())
		Expect(names(backendOf(reader), "run-1")).To(Equal([]string{"a"}))
	})

	It("5: adds a producer's extra column and refuses a producer's changed type", func() {
		store := openHere()
		widening := startChild(script("extra", childStep{Op: "open"}, childStep{Op: "append", Stream: "run-1", Names: []string{"x"}}), "")
		Expect(widening.await("append", 1).OK).To(BeTrue())
		widening.finish()
		retyping := startChild(script("retyped", childStep{Op: "open"}, childStep{Op: "append", Stream: "run-2", Names: []string{"y"}}), "")
		Expect(retyping.await("append", 1).Sentinels).To(ContainElement("conflict"))
		retyping.finish()

		Expect(names(backendOf(store), "run-1")).To(Equal([]string{"x"}))
		_, err := backendOf(store).Meta(ctx, "run-2")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue())
	})

	It("6: elects exactly one of three spooling readers when the owner is killed, losing no batch", func() {
		owning := startChild(script("", childStep{Op: "open"}, childStep{Op: "hold"}), "")
		owning.await("open", 1)
		var readers []*child
		for n := range 3 {
			var batch []string
			for m := range 5 {
				batch = append(batch, fmt.Sprintf("r%d-%d", n, m))
			}
			readers = append(readers, startChild(script("",
				childStep{Op: "open"}, childStep{Op: "hold"},
				childStep{Op: "write", Stream: fmt.Sprintf("run-%d", n), Names: batch, MaxRows: 1, Wait: true},
				childStep{Op: "hold"}), ""))
		}
		for _, reader := range readers {
			Expect(reader.await("open", 1).Role).To(Equal("reader"))
		}

		owning.kill()
		for _, reader := range readers {
			reader.resume()
		}
		owners := 0
		for _, reader := range readers {
			Expect(reader.await("write", 1).OK).To(BeTrue(), "%+v", reader.lines())
			if reader.await("hold", 2).Role == "owner" {
				owners++
			}
		}
		Expect(owners).To(Equal(1))
		for _, reader := range readers {
			reader.finish()
		}

		store := openHere()
		for n := range 3 {
			Expect(names(backendOf(store), fmt.Sprintf("run-%d", n))).To(Equal([]string{
				fmt.Sprintf("r%d-0", n), fmt.Sprintf("r%d-1", n), fmt.Sprintf("r%d-2", n), fmt.Sprintf("r%d-3", n), fmt.Sprintf("r%d-4", n),
			}))
		}
	})

	It("7: ingests a batch published after the owner's final drain before letting the store go", func() {
		owning := startChild(script("", childStep{Op: "open"}, childStep{Op: "hold"}), "after-final-drain:pause")
		owning.await("open", 1)
		owning.resume()
		owning.await("failpoint after-final-drain", 1)

		publish("b-late", appendTo("run-1", "late"))
		owning.finish()

		store := openHere()
		Expect(names(backendOf(store), "run-1")).To(Equal([]string{"late"}))
	})

	It("8: commits the other entries of a bulk batch that appends to a sealed stream", func() {
		store := openHere()
		Expect(backendOf(store).Append(ctx, "sealed", recordstoretest.Kind, rows("s"))).Error().ToNot(HaveOccurred())
		Expect(backendOf(store).Seal(ctx, "sealed")).To(Succeed())
		writing := startChild(script("",
			childStep{Op: "open"},
			childStep{Op: "write", Stream: "open", Names: []string{"a"}},
			childStep{Op: "write", Stream: "sealed", Names: []string{"b"}, Wait: true}), "")

		Expect(writing.await("write", 2).Sentinels).To(ContainElement("sealed"))
		writing.finish()
		Expect(names(backendOf(store), "open")).To(Equal([]string{"a"}))
		Expect(names(backendOf(store), "sealed")).To(Equal([]string{"s"}))
	})

	It("9: refuses an entry fenced to a generation deleted and recreated since", func() {
		owning := startChild(script("", childStep{Op: "open"}, childStep{Op: "hold"}), "")
		owning.await("open", 1)
		reader := openHere()
		backend := backendOf(reader)
		Expect(backend.Append(ctx, "run-1", recordstoretest.Kind, rows("a"))).Error().ToNot(HaveOccurred())
		earlier, err := backend.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(backend.Delete(ctx, "run-1")).To(Succeed())
		Expect(backend.Append(ctx, "run-1", recordstoretest.Kind, rows("b"))).Error().ToNot(HaveOccurred())

		fenced := appendTo("run-1", "c")
		fenced.Generation = earlier.Generation
		publish("b-fenced", fenced)
		result, err := reader.Await(ctx, "b-fenced")
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Entries[0].Error).ToNot(BeNil())
		Expect(result.Entries[0].Error.Code).To(Equal(recordstore.BatchErrorNotFound))
		Expect(names(backend, "run-1")).To(Equal([]string{"b"}))
	})

	It("10: applies fifty spooled batches and a seal in the order they were written", func() {
		store := openHere()
		var batch []string
		for n := range 50 {
			batch = append(batch, fmt.Sprintf("row-%02d", n))
		}
		writing := startChild(script("", childStep{Op: "open"}, childStep{Op: "write", Stream: "run-1", Names: batch, MaxRows: 1, Seal: true}), "")
		Expect(writing.await("write", 1).OK).To(BeTrue())
		writing.finish()

		Eventually(func() bool {
			meta, err := backendOf(store).Meta(ctx, "run-1")
			return err == nil && meta.Sealed
		}, childWait).Should(BeTrue())
		Expect(names(backendOf(store), "run-1")).To(Equal(batch))
	})

	It("11: fails a batch of a manifest format it cannot read at once", func() {
		owning := startChild(script("", childStep{Op: "open"}, childStep{Op: "hold"}), "")
		owning.await("open", 1)
		name := "00000000000000000001-future-000000000001-b-future"
		staged := filepath.Join(path+".spool", "tmp", "future")
		Expect(os.MkdirAll(staged, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(staged, "manifest.json"), []byte(`{"format":2,"id":"b-future"}`), 0o600)).To(Succeed())
		Expect(os.Rename(staged, filepath.Join(path+".spool", "incoming", name))).To(Succeed())

		Eventually(filepath.Join(path+".spool", "failed", name, "error.json"), childWait).Should(BeAnExistingFile())
	})

	It("12: wakes a reader tailing a stream within a second of the owner appending to it", func() {
		owning := startChild(script("",
			childStep{Op: "open"}, childStep{Op: "append", Stream: "run-1", Names: []string{"a"}}, childStep{Op: "hold"},
			childStep{Op: "append", Stream: "run-1", Names: []string{"b"}}, childStep{Op: "hold"}), "")
		owning.await("hold", 1)
		reader := openHere()
		notifier, err := recordstore.NewNotifier(backendOf(reader), recordstore.NotifierOptions{RecheckInterval: time.Hour})
		Expect(err).ToNot(HaveOccurred())
		meta, err := notifier.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		woken := make(chan recordstore.Meta, 1)
		go func() {
			defer GinkgoRecover()
			meta, err := notifier.Wait(ctx, "run-1", 1, meta.Generation)
			Expect(err).ToNot(HaveOccurred())
			woken <- meta
		}()
		Consistently(woken, 300*time.Millisecond).ShouldNot(Receive())

		owning.resume()
		owning.await("append", 2)
		Eventually(woken, time.Second).Should(Receive(HaveField("HighSeq", int64(2))))
	})

	It("13: has an opener wait while the owner is still starting", func() {
		owning := startChild(script("", childStep{Op: "open"}, childStep{Op: "hold"}), "during-starting:pause")
		owning.await("failpoint during-starting", 1)
		opened := make(chan *owner.Store[*sqlite.Backend], 1)
		go func() {
			defer GinkgoRecover()
			opened <- openHere()
		}()
		Consistently(opened, 500*time.Millisecond).ShouldNot(Receive())

		owning.resume()
		var store *owner.Store[*sqlite.Backend]
		Eventually(opened, childWait).Should(Receive(&store))
		Expect(store.Role()).To(Equal(owner.RoleReader))
	})

	It("15: refuses a second process an exclusive store, naming the holder's pid", func() {
		path = filepath.Join(filepath.Dir(path), "ndjson")
		holding := startChild(script("", childStep{Op: "exclusive"}, childStep{Op: "hold"}), "")
		pid := holding.await("hold", 1).PID

		_, err := owner.Exclusive(path, "parent")
		var locked *owner.LockedError
		Expect(errors.As(err, &locked)).To(BeTrue(), "Exclusive: %v", err)
		Expect(locked.Owner).ToNot(BeNil())
		Expect(locked.Owner.PID).To(Equal(pid))
		Expect(err.Error()).To(ContainSubstring(fmt.Sprintf("pid %d", pid)))
	})

	It("17: ingests through the poll when the owner's socket is gone", func() {
		owning := startChild(script("", childStep{Op: "open"}, childStep{Op: "hold"}), "")
		owning.await("open", 1)
		Expect(os.Remove(owner.SocketPath(path))).To(Succeed())
		reader := openHere()

		result, err := backendOf(reader).Append(ctx, "run-1", recordstoretest.Kind, rows("a"))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Window).To(Equal(recordstore.Window{From: 1, To: 1}))
		Expect(reader.Role()).To(Equal(owner.RoleReader))
	})
})
