// Specs for the trace runtime: a plugin's capture runs as a managed session,
// its records reach a real results store, and stop, deadline and failure end it.

package tracing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

// scripted is a tick kind whose capture each spec writes.
type scripted struct {
	handle func(ctx dbcontext.Context, params tickParams, records tracing.Emitter[tick], store tracing.Records[tick]) error
}

func (scripted) Params() tickParams { return tickParams{Count: 3} }

func (scripted) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Ticks", TimeColumn: "at"}
}

func (s scripted) Handle(ctx dbcontext.Context, params tickParams, records tracing.Emitter[tick], store tracing.Records[tick]) error {
	return s.handle(ctx, params, records, store)
}

// emitTicks emits ticks 1..count.
func emitTicks(ctx context.Context, records tracing.Emitter[tick], count int) error {
	for n := 1; n <= count; n++ {
		if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: n}); err != nil {
			return err
		}
	}
	return nil
}

func liveTicks() tracing.TracePlugin {
	return tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, p tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
		if err := emitTicks(ctx, records, p.Count); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}}, tracing.Capabilities{Live: true})
}

func numbers(rows []recordstore.Row) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, fmt.Sprint(row["n"]))
	}
	return out
}

var _ = Describe("Runtime", func() {
	It("commits a live kind's records while its session runs, and seals them when it stops", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{"ticks": liveTicks()})
		session := env.Start("ticks", `{"count": 3}`)

		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(5 * time.Second).Should(Equal(int64(3)))
		running := session.Snapshot()
		Expect(running.State).To(Equal(query.SessionRunning))
		Expect(running.Profile).To(Equal("traces/ticks"))
		Expect(running.Kind).To(Equal(query.KindCapture))
		Expect(running.Params).To(Equal(map[string]any{"count": float64(3)}))
		Expect(running.Events.Kind).To(Equal("ticks"))

		session.Stop("user")
		info := tracingtest.Ended(session)
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2", "3"}))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("ends a historical kind's session by itself once its records are stored", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, p tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
				return emitTicks(ctx, records, p.Count)
			}}, tracing.Capabilities{Historical: true}),
		})
		info := tracingtest.Ended(env.Start("ticks", ""))
		Expect(info.State).To(Equal(query.SessionCompleted))
		Expect(info.EventCount).To(Equal(int64(3)))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2", "3"}))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("keeps the records a handler emits as it stops", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
				<-ctx.Done()
				return emitTicks(context.Background(), records, 2)
			}}, tracing.Capabilities{Live: true}),
		})
		session := env.Start("ticks", "")
		tracingtest.Running(session)
		session.Stop("user")
		info := tracingtest.Ended(session)
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2"}))
	})

	It("stops a session at its deadline", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{"ticks": liveTicks()})
		stopAt := time.Now().Add(300 * time.Millisecond)
		managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "ticks", StopAt: &stopAt})
		Expect(err).ToNot(HaveOccurred())
		info := tracingtest.Ended(managed.Session())
		Expect(info.State).To(Equal(query.SessionCompleted))
		Expect(info.StopReason).To(Equal(query.StopReasonDeadline))
		Expect(env.Rows(info)).To(HaveLen(3))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("fails the session with its handler's error, keeping and sealing the records it emitted", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
				if err := emitTicks(ctx, records, 1); err != nil {
					return err
				}
				return errors.New("source broke")
			}}, tracing.Capabilities{Historical: true}),
		})
		info := tracingtest.Ended(env.Start("ticks", ""))
		Expect(info.State).To(Equal(query.SessionFailed))
		Expect(info.Error).To(ContainSubstring("source broke"))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1"}))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("refuses an unknown kind and invalid params before any session starts", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{"ticks": liveTicks()})
		_, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "nope"})
		Expect(err).To(MatchError(tracing.ErrUnknownKind))
		_, err = env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "ticks", Params: json.RawMessage(`{"cnt": 1}`)})
		Expect(err).To(MatchError(tracing.ErrInvalidParams))
		_, err = env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "ticks", Params: json.RawMessage(`{"count": -1}`)})
		Expect(err).To(MatchError(tracing.ErrInvalidParams))
		Expect(env.Sessions.List()).To(BeEmpty())
	})

	It("reports what its capture emitted, deduplicated and dropped in the session summary", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
				for _, n := range []int{1, 1, 2} {
					if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: n}); err != nil {
						return err
					}
				}
				return nil
			}}, tracing.Capabilities{Historical: true}).
				WithDeduplication(func(t tick) string { return string(rune('0' + t.N)) }, func(tickParams) time.Duration { return time.Hour }),
		})
		info := tracingtest.Ended(env.Start("ticks", ""))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2"}))
		Expect(tracingtest.Summary(info)).To(Equal(tracing.Summary{Emitted: 2, Deduplicated: 1}))
	})

	It("keeps repeated records when the kind deduplicates nothing", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
				for range 3 {
					if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: 1}); err != nil {
						return err
					}
				}
				return nil
			}}, tracing.Capabilities{Historical: true}),
		})
		Expect(numbers(env.Rows(tracingtest.Ended(env.Start("ticks", ""))))).To(Equal([]string{"1", "1", "1"}))
	})

	It("truncates, parses embedded JSON and masks secrets before a record is stored", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"exchanges": tracing.NewHandler[tickParams, exchange](exchanges{}, tracing.Capabilities{Historical: true}).
				WithTruncation(64).WithJSONProcessor("body.text").WithSecretMasking(),
		})
		info := tracingtest.Ended(env.Start("exchanges", ""))
		Expect(info.Error).To(BeEmpty())
		rows := env.Rows(info)
		Expect(rows).To(HaveLen(1))
		Expect(rows[0]["body"]).To(Equal(map[string]any{"text": map[string]any{"password": "****", "n": json.Number("1")}}))
		Expect(rows[0]["raw"]).To(Equal(`{"a": 1}`))
		Expect(rows[0]["note"]).To(Equal(strings.Repeat("n", 64) + "…[truncated]"))
		Expect(rows[0]["truncated"]).To(Equal([]any{"note"}))
		Expect(rows[0]["url"]).ToNot(ContainSubstring("hunter2"))
	})

	It("reads a record back with the JSON it parsed as text again", func() {
		read := make(chan exchange, 1)
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"exchanges": tracing.NewHandler[tickParams, exchange](readback{read: read}, tracing.Capabilities{Historical: true}).
				WithJSONProcessor("body.text").WithSecretMasking(),
		})
		info := tracingtest.Ended(env.Start("exchanges", ""))
		Expect(info.Error).To(BeEmpty())
		Expect(read).To(Receive(HaveField("Body.Text", `{"n":1,"password":"****"}`)))
	})

	DescribeTable("refuses JSON parsing of anything but text its records declare",
		func(paths []string, message string) {
			Expect(tracing.NewKinds().RegisterKind("exchanges", tracing.NewHandler[tickParams, exchange](exchanges{}, tracing.Capabilities{Historical: true}).
				WithJSONProcessor(paths...))).To(MatchError(ContainSubstring(message)))
		},
		Entry("no paths", []string{}, "names no fields"),
		Entry("a field the record lacks", []string{"body.html"}, `"body.html"`),
		Entry("a path through text", []string{"note.text"}, `"note.text"`),
		Entry("a field that is not text", []string{"body"}, `"body"`),
		Entry("a time", []string{"at"}, `"at"`),
	)

	It("refuses truncation for a kind whose records cannot list what was cut", func() {
		Expect(tracing.NewKinds().RegisterKind("ticks", tracing.NewHandler[tickParams, tick](ticker{}, tracing.Capabilities{Live: true}).WithTruncation(16))).
			To(MatchError(ContainSubstring("traces.Truncation")))
	})

	It("waits for room in a full buffer, and continues once its rows commit", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
				return emitTicks(ctx, records, 10)
			}}, tracing.Capabilities{Historical: true}),
		}, func(r *tracing.Runtime) { r.BufferRows = 2 })
		info := tracingtest.Ended(env.Start("ticks", ""))
		Expect(info.State).To(Equal(query.SessionCompleted))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}))
	})

	It("drops what a full buffer cannot take when a handler must not wait, and counts it", func() {
		attempts := make(chan []bool, 1)
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
				var taken []bool
				for n := 1; n <= 3; n++ {
					taken = append(taken, records.TryEmit(tick{At: time.Now().UTC(), N: n}))
				}
				attempts <- taken
				<-ctx.Done()
				return nil
			}}, tracing.Capabilities{Live: true}),
		}, func(r *tracing.Runtime) { r.BufferRows = 2; r.PollEvery = time.Hour })
		session := env.Start("ticks", "")
		Eventually(attempts).Should(Receive(Equal([]bool{true, true, false})))
		session.Stop("user")
		info := tracingtest.Ended(session)
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2"}))
		Expect(tracingtest.Summary(info)).To(Equal(tracing.Summary{Emitted: 2, Dropped: 1}))
		Expect(info.Warning).To(ContainSubstring("dropped 1"))
	})

	It("lets a handler read back the records its session has committed", func() {
		seen := make(chan []int, 1)
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], store tracing.Records[tick]) error {
				if err := emitTicks(ctx, records, 2); err != nil {
					return err
				}
				for {
					var ns []int
					if err := store.Scan(ctx, 0, func(_ int64, t tick) error {
						ns = append(ns, t.N)
						return nil
					}); err != nil {
						return err
					}
					if len(ns) == 2 {
						seen <- ns
						<-ctx.Done()
						return nil
					}
					select {
					case <-ctx.Done():
						return nil
					case <-time.After(10 * time.Millisecond):
					}
				}
			}}, tracing.Capabilities{Live: true}),
		})
		session := env.Start("ticks", "")
		Eventually(seen).WithTimeout(5*time.Second).Should(Receive(Equal([]int{1, 2})), func() string { return fmt.Sprintf("%+v", session.Snapshot().SessionStatus) })
		session.Stop("user")
		tracingtest.Ended(session)
	})

	It("stops every running capture when its registry stops all sessions", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{"ticks": liveTicks()})
		first, second := env.Start("ticks", ""), env.Start("ticks", "")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		Expect(env.Sessions.StopAll(ctx)).To(Succeed())
		for _, session := range []*query.Session{first, second} {
			info := tracingtest.Ended(session)
			Expect(info.State).To(Equal(query.SessionStopped))
			Expect(env.Sealed(info)).To(BeTrue())
		}
	})

	It("runs a prepared capture only once it observes its source, handing Handle what Prepare set up", func() {
		released := make(chan struct{})
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](prepared{released: released}, tracing.Capabilities{Live: true}),
		})
		managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "ticks"})
		Expect(err).ToNot(HaveOccurred())
		session := managed.Session()
		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(5 * time.Second).Should(Equal(int64(2)))
		Consistently(released).ShouldNot(BeClosed())

		session.Stop("user")
		info := tracingtest.Ended(session)
		Expect(env.Rows(info)).To(HaveLen(2))
		Expect(env.Rows(info)[1]["label"]).To(Equal("set up by prepare"))
		Expect(released).To(BeClosed())
	})

	It("records on the session that what Prepare set up could not be released, keeping its records", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](prepared{released: make(chan struct{}), releaseErr: errors.New("could not drop")}, tracing.Capabilities{Live: true}),
		})
		session := env.Start("ticks", "")
		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(5 * time.Second).Should(Equal(int64(2)))
		session.Stop("user")
		info := tracingtest.Ended(session)
		// A stop the user asked for ends stopped; its error is still recorded.
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(info.Error).To(ContainSubstring("could not drop"))
		Expect(env.Rows(info)).To(HaveLen(2))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("refuses to start a capture it cannot prepare", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](prepared{fail: errors.New("server refused")}, tracing.Capabilities{Live: true}),
		})
		_, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "ticks"})
		Expect(err).To(MatchError(ContainSubstring("server refused")))
		sessions := env.Sessions.List()
		Expect(sessions).To(HaveLen(1))
		Expect(sessions[0].State).To(Equal(query.SessionFailed))
	})

	It("restarts an ended session as a new capture into a new stream", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, p tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
				return emitTicks(ctx, records, p.Count)
			}}, tracing.Capabilities{Historical: true}),
		})
		previous := tracingtest.Ended(env.Start("ticks", `{"count": 2}`))
		restarted, err := env.Sessions.Restart(context.Background(), previous.ID, query.RestartOverrides{})
		Expect(err).ToNot(HaveOccurred())
		info := tracingtest.Ended(restarted)
		Expect(info.RestartOf).To(Equal(previous.ID))
		Expect(info.Events.Stream).ToNot(Equal(previous.Events.Stream))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2"}))
	})
})

type exchange struct {
	tracing.Truncation
	At   time.Time `json:"at"`
	URL  string    `json:"url"`
	Note string    `json:"note"`
	// Body is text a kind names for JSON parsing; Raw holds JSON it does not.
	Body content `json:"body"`
	Raw  string  `json:"raw,omitempty"`
}

// content nests its text in a json column, as a HAR body is.
type content struct {
	Text string `json:"text"`
}

type exchanges struct{}

func (exchanges) Params() tickParams { return tickParams{} }

func (exchanges) Schema() recordresults.ResultType[exchange] {
	return recordresults.ResultType[exchange]{Title: "Exchanges", TimeColumn: "at"}
}

func (exchanges) Handle(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[exchange], _ tracing.Records[exchange]) error {
	return records.Emit(ctx, exchange{
		At: time.Now().UTC(), URL: "https://example.com/?password=hunter2",
		Note: strings.Repeat("n", 80), Body: content{Text: `{"password": "x", "n": 1}`}, Raw: `{"a": 1}`,
	})
}

// readback emits one exchange with a JSON body, then reads it back through
// its own records and hands what it read to read.
type readback struct{ read chan exchange }

func (readback) Params() tickParams { return tickParams{} }

func (readback) Schema() recordresults.ResultType[exchange] {
	return recordresults.ResultType[exchange]{Title: "Exchanges", TimeColumn: "at"}
}

func (r readback) Handle(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[exchange], store tracing.Records[exchange]) error {
	if err := records.Emit(ctx, exchange{At: time.Now().UTC(), Body: content{Text: `{"password": "x", "n": 1}`}}); err != nil {
		return err
	}
	for {
		var found []exchange
		if err := store.Scan(ctx, 0, func(_ int64, record exchange) error {
			found = append(found, record)
			return nil
		}); err != nil {
			return err
		}
		if len(found) > 0 {
			r.read <- found[0]
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type preparedLabel struct{}

// prepared emits a tick as it prepares, before its session runs, and one more
// from Handle labelled by what Prepare put on its context.
type prepared struct {
	fail       error
	released   chan struct{}
	releaseErr error
}

func (prepared) Params() tickParams { return tickParams{} }

func (prepared) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Ticks", TimeColumn: "at"}
}

func (p prepared) Prepare(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick]) (dbcontext.Context, func() error, error) {
	if p.fail != nil {
		return ctx, nil, p.fail
	}
	records.TryEmit(tick{At: time.Now().UTC(), N: 1})
	return ctx.WithValue(preparedLabel{}, "set up by prepare"), func() error {
		close(p.released)
		return p.releaseErr
	}, nil
}

func (prepared) Handle(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
	label, _ := ctx.Value(preparedLabel{}).(string)
	if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: 2, Label: label}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// keyed is a tick kind keyed by label: a label stored once is skipped after.
type keyed struct{}

func (keyed) Params() tickParams { return tickParams{} }

func (keyed) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Ticks", TimeColumn: "at", KeyColumn: "label"}
}

func (keyed) Handle(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
	for n, label := range []string{"a", "b", "a", "c", "b"} {
		if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: n + 1, Label: label}); err != nil {
			return err
		}
	}
	return nil
}

var _ = Describe("Runtime with a keyed kind", func() {
	It("stores a key emitted twice once, however its records were batched", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](keyed{}, tracing.Capabilities{Historical: true}),
		})
		info := tracingtest.Ended(env.Start("ticks", ""))
		Expect(info.Error).To(BeEmpty())
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2", "4"}))
	})
})

// repeating emits count ticks all labelled "same", numbered from one, as it
// prepares: before the session's first read, so they all fall in one page.
type repeating struct {
	count      int
	onConflict recordstore.OnConflict
}

func (repeating) Params() tickParams { return tickParams{} }

func (r repeating) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Ticks", TimeColumn: "at", KeyColumn: "label", OnConflict: r.onConflict}
}

func (r repeating) Prepare(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick]) (dbcontext.Context, func() error, error) {
	for n := 1; n <= r.count; n++ {
		if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: n, Label: "same"}); err != nil {
			return ctx, nil, err
		}
	}
	return ctx, nil, nil
}

func (repeating) Handle(dbcontext.Context, tickParams, tracing.Emitter[tick], tracing.Records[tick]) error {
	return nil
}

var _ = Describe("Runtime with a key repeated many times", func() {
	It("stores a long run of one key once rather than failing its capture", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](repeating{count: 500}, tracing.Capabilities{Historical: true}),
		})
		info := tracingtest.Ended(env.Start("ticks", ""))
		Expect(info.Error).To(BeEmpty())
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1"}))
		Expect(tracingtest.Summary(info)).To(Equal(tracing.Summary{Emitted: 500, Collapsed: 499}))
	})

	It("stores the last of a key's copies for a kind that replaces", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](repeating{count: 500, onConflict: recordstore.OnConflictReplace}, tracing.Capabilities{Historical: true}),
		})
		info := tracingtest.Ended(env.Start("ticks", ""))
		Expect(info.Error).To(BeEmpty())
		Expect(numbers(env.Rows(info))).To(Equal([]string{"500"}))
		Expect(tracingtest.Summary(info)).To(Equal(tracing.Summary{Emitted: 500, Collapsed: 499}))
	})
})

var _ = Describe("Runtime stopping a capture with a large final drain", func() {
	It("commits what one drain can, reports the rest dropped, and seals the stream", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
				<-ctx.Done()
				return emitTicks(context.Background(), records, 70_000)
			}}, tracing.Capabilities{Live: true}),
		})
		// Draining tens of thousands of rows outlasts the registry's default
		// stop timeout under the race detector on a loaded machine.
		env.Runtime.Sessions = query.NewSessionRegistry(query.RegistryOptions{
			StopTimeout: func(string) time.Duration { return 2 * time.Minute },
		})
		session := env.Start("ticks", "")
		tracingtest.Running(session)
		session.Stop("spec")
		// Tens of thousands of rows: far slower under the race detector.
		Eventually(session.Done()).WithTimeout(2 * time.Minute).Should(BeClosed())
		info := session.Snapshot()
		Expect(info.Error).To(BeEmpty())
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(info.EventCount).To(Equal(int64(64_000)))
		Expect(info.Warning).To(ContainSubstring("dropped 6000"))
		Expect(env.Sealed(info)).To(BeTrue())
	})
})
