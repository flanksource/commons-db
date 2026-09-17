package probe_test

import (
	"context"
	"errors"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/probe"
)

type fakeBackend struct {
	mu         sync.Mutex
	rows       []recordstore.Row
	appendErr  error
	appendCall int
	sealed     bool
	operations *[]string
}

func (b *fakeBackend) Append(_ context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.appendCall++
	if b.operations != nil {
		*b.operations = append(*b.operations, "append")
	}
	if b.appendErr != nil {
		err := b.appendErr
		b.appendErr = nil
		return recordstore.AppendResult{}, err
	}
	from := int64(len(b.rows) + 1)
	b.rows = append(b.rows, rows...)
	return recordstore.AppendResult{Window: recordstore.Window{From: from, To: int64(len(b.rows))}}, nil
}

func (b *fakeBackend) Meta(_ context.Context, stream string) (recordstore.Meta, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := int64(len(b.rows))
	return recordstore.Meta{Stream: stream, Kind: "probe_event", Generation: "store-generation", LowSeq: 1, HighSeq: total, Total: total, Sealed: b.sealed}, nil
}

func (b *fakeBackend) Scan(context.Context, string, int64, func(int64, recordstore.Row) error) error {
	return nil
}

func (b *fakeBackend) Trim(ctx context.Context, stream string, _ time.Time) (recordstore.Meta, error) {
	return b.Meta(ctx, stream)
}

func (b *fakeBackend) Expire(context.Context, string, time.Duration) error { return nil }

func (b *fakeBackend) Delete(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rows = nil
	b.sealed = false
	return nil
}

func (b *fakeBackend) Seal(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sealed = true
	if b.operations != nil {
		*b.operations = append(*b.operations, "seal")
	}
	return nil
}

func (b *fakeBackend) Close() error { return nil }

func (b *fakeBackend) calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.appendCall
}

type fakeSource struct {
	mu         sync.Mutex
	pages      []probe.Batch
	committed  int
	final      probe.Final
	operations *[]string
}

func (s *fakeSource) Read(context.Context, probe.Cursor) (probe.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operations != nil {
		*s.operations = append(*s.operations, "read")
	}
	if s.committed >= len(s.pages) {
		return probe.Batch{}, errors.New("unexpected read")
	}
	return s.pages[s.committed], nil
}

func (s *fakeSource) Commit(_ context.Context, _ probe.Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.committed++
	if s.operations != nil {
		*s.operations = append(*s.operations, "commit")
	}
	return nil
}

func (s *fakeSource) Freeze(context.Context) error {
	if s.operations != nil {
		*s.operations = append(*s.operations, "freeze")
	}
	return nil
}

func (s *fakeSource) Finalize(context.Context) (probe.Final, error) {
	if s.operations != nil {
		*s.operations = append(*s.operations, "finalize")
	}
	return s.final, nil
}

func (s *fakeSource) Release(context.Context) error {
	if s.operations != nil {
		*s.operations = append(*s.operations, "release")
	}
	return nil
}

func describe(backend recordstore.Backend) probe.DescribeFunc {
	return func(ctx context.Context, stream string) (*query.EventsRef, error) {
		meta, err := backend.Meta(ctx, stream)
		if err != nil {
			return nil, err
		}
		return &query.EventsRef{
			Stream: meta.Stream, Kind: meta.Kind, Generation: meta.Generation,
			Low: meta.LowSeq, High: meta.HighSeq, From: meta.LowSeq, To: meta.HighSeq, Total: meta.Total,
			Store: query.EventsStoreLocation{Backend: "memory"},
		}, nil
	}
}

func options(backend recordstore.Backend) probe.Options {
	return probe.Options{
		Identity: "route-a/probe-a/generation-7", Stream: "probe-a:7", Kind: "probe_event",
		Backend: backend, Describe: describe(backend), MaxPages: 4,
	}
}

func page(generation string, next, write int64, active bool, id string) probe.Batch {
	return probe.Batch{
		Generation: generation, Next: next, Write: write, More: active && next < write, Active: active,
		Rows: []recordstore.Row{{"id": id}}, Summary: map[string]any{"next": next},
	}
}

var _ = Describe("durable probe manager", func() {
	It("creates the stream before returning an exclusively owned run", func() {
		backend := &fakeBackend{}
		manager := probe.NewManager()
		source := &fakeSource{}
		opened := 0
		run, err := manager.Arm(context.Background(), options(backend), func(context.Context) (probe.Source, error) {
			opened++
			return source, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(backend.calls()).To(Equal(1))
		Expect(run.Status()).To(And(
			HaveField("Handle", "route-a/probe-a/generation-7"),
			HaveField("Events.Total", int64(0)),
		))

		_, err = manager.Arm(context.Background(), options(backend), func(context.Context) (probe.Source, error) {
			opened++
			return source, nil
		})
		Expect(err).To(MatchError(ContainSubstring("already managed")))
		Expect(opened).To(Equal(1))
		_, err = run.Detach(context.Background())
		Expect(err).NotTo(HaveOccurred())
	})

	It("does not advance or commit a cursor until its rows append", func() {
		appendErr := errors.New("record store unavailable")
		backend := &fakeBackend{appendErr: appendErr}
		manager := probe.NewManager()
		source := &fakeSource{pages: []probe.Batch{page("source-7", 1, 1, true, "call-1")}}
		_, err := manager.Arm(context.Background(), options(backend), func(context.Context) (probe.Source, error) { return source, nil })
		Expect(err).To(MatchError(appendErr), "the stream creation itself must be durable")

		backend = &fakeBackend{}
		run, err := manager.Arm(context.Background(), options(backend), func(context.Context) (probe.Source, error) { return source, nil })
		Expect(err).NotTo(HaveOccurred())
		backend.appendErr = appendErr
		_, err = run.Sample(context.Background())
		Expect(err).To(MatchError(appendErr))
		Expect(source.committed).To(BeZero())
		Expect(run.ProbeStatus().Cursor).To(Equal(probe.Cursor{}))

		_, err = run.Sample(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(source.committed).To(Equal(1))
		Expect(run.ProbeStatus().Cursor).To(Equal(probe.Cursor{Generation: "source-7", Next: 1}))
	})

	It("rejects a source generation change before appending it", func() {
		backend := &fakeBackend{}
		manager := probe.NewManager()
		opts := options(backend)
		opts.Cursor = probe.Cursor{Generation: "source-7", Next: 4}
		source := &fakeSource{pages: []probe.Batch{page("source-8", 5, 5, true, "wrong-generation")}}
		run, err := manager.Arm(context.Background(), opts, func(context.Context) (probe.Source, error) { return source, nil })
		Expect(err).NotTo(HaveOccurred())

		_, err = run.Sample(context.Background())
		Expect(err).To(MatchError(ContainSubstring("generation")))
		Expect(backend.calls()).To(Equal(1), "only the empty arm append is allowed")
		Expect(source.committed).To(BeZero())
	})

	It("fails a non-advancing source while backlog remains", func() {
		backend := &fakeBackend{}
		manager := probe.NewManager()
		source := &fakeSource{pages: []probe.Batch{{Generation: "source-7", Next: 3, Write: 4, More: true, Active: true}}}
		opts := options(backend)
		opts.Cursor = probe.Cursor{Generation: "source-7", Next: 3}
		run, err := manager.Arm(context.Background(), opts, func(context.Context) (probe.Source, error) { return source, nil })
		Expect(err).NotTo(HaveOccurred())

		_, err = run.Sample(context.Background())
		Expect(err).To(MatchError(ContainSubstring("did not advance")))
		Expect(source.committed).To(BeZero())
	})

	It("commits an empty page without appending an empty record batch", func() {
		backend := &fakeBackend{}
		manager := probe.NewManager()
		source := &fakeSource{pages: []probe.Batch{{Generation: "source-7", Next: 3, Write: 3, Active: true}}}
		run, err := manager.Arm(context.Background(), options(backend), func(context.Context) (probe.Source, error) { return source, nil })
		Expect(err).NotTo(HaveOccurred())

		window, err := run.Sample(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(window).To(HaveField("Cursor", probe.Cursor{Generation: "source-7", Next: 3}))
		Expect(window).To(HaveField("Rows", int64(0)))
		Expect(source.committed).To(Equal(1))
		Expect(backend.calls()).To(Equal(1), "only stream creation should append")
	})

	It("drains every page left behind by an inactive frozen source", func() {
		backend := &fakeBackend{}
		manager := probe.NewManager()
		source := &fakeSource{pages: []probe.Batch{
			{Generation: "source-7", Next: 1, Write: 2, More: true, Active: false, Rows: []recordstore.Row{{"id": "event-1"}}},
			{Generation: "source-7", Next: 2, Write: 2, Active: false, Rows: []recordstore.Row{{"id": "event-2"}}},
		}}
		run, err := manager.Arm(context.Background(), options(backend), func(context.Context) (probe.Source, error) { return source, nil })
		Expect(err).NotTo(HaveOccurred())

		window, err := run.Sample(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(window).To(HaveField("Rows", int64(2)))
		Expect(source.committed).To(Equal(2))
		Expect(run.ProbeStatus().Cursor).To(Equal(probe.Cursor{Generation: "source-7", Next: 2}))
		Expect(run.Finished()).To(BeClosed())
	})

	It("stops in freeze drain finalize seal release order", func() {
		operations := []string{}
		backend := &fakeBackend{operations: &operations}
		manager := probe.NewManager()
		source := &fakeSource{
			operations: &operations,
			pages:      []probe.Batch{page("source-7", 1, 1, false, "closed-call")},
			final:      probe.Final{Rows: []recordstore.Row{{"id": "open-call"}}, Result: "complete"},
		}
		run, err := manager.Arm(context.Background(), options(backend), func(context.Context) (probe.Source, error) { return source, nil })
		Expect(err).NotTo(HaveOccurred())
		operations = operations[:0]

		finish, err := run.Stop(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(finish.Result).To(Equal("complete"))
		Expect(operations).To(Equal([]string{"freeze", "read", "append", "commit", "finalize", "append", "seal", "release"}))
		Expect(finish.Events.Total).To(Equal(int64(2)))
	})

	It("detaches without changing the source or sealing its stream", func() {
		operations := []string{}
		backend := &fakeBackend{operations: &operations}
		manager := probe.NewManager()
		source := &fakeSource{operations: &operations}
		run, err := manager.Arm(context.Background(), options(backend), func(context.Context) (probe.Source, error) { return source, nil })
		Expect(err).NotTo(HaveOccurred())
		operations = operations[:0]

		_, err = run.Detach(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(operations).To(BeEmpty())
		Expect(backend.sealed).To(BeFalse())
		_, err = manager.Arm(context.Background(), options(backend), func(context.Context) (probe.Source, error) { return &fakeSource{}, nil })
		Expect(err).NotTo(HaveOccurred(), "a successor may claim the detached identity")
	})
})
