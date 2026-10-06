// Specs for the trace runtime: a plugin's capture runs as a managed session,
// its records reach a real results store, and stop, deadline and failure end it.

package traces_test

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

	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
)

// scripted is a tick kind whose capture each spec writes.
type scripted struct {
	handle func(ctx dbcontext.Context, params tickParams, records traces.Emitter[tick], store traces.Records[tick]) error
}

func (scripted) Params() tickParams { return tickParams{Count: 3} }

func (scripted) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Ticks", TimeColumn: "at"}
}

func (s scripted) Handle(ctx dbcontext.Context, params tickParams, records traces.Emitter[tick], store traces.Records[tick]) error {
	return s.handle(ctx, params, records, store)
}

// emitTicks emits ticks 1..count.
func emitTicks(ctx context.Context, records traces.Emitter[tick], count int) error {
	for n := 1; n <= count; n++ {
		if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: n}); err != nil {
			return err
		}
	}
	return nil
}

func liveTicks() traces.TracePlugin {
	return traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, p tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
		if err := emitTicks(ctx, records, p.Count); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}}, traces.Capabilities{Live: true})
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
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"ticks": liveTicks()})
		session := env.Start("ticks", `{"count": 3}`)

		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(5 * time.Second).Should(Equal(int64(3)))
		running := session.Snapshot()
		Expect(running.State).To(Equal(query.SessionRunning))
		Expect(running.Profile).To(Equal("traces/ticks"))
		Expect(running.Kind).To(Equal(query.KindCapture))
		Expect(running.Params).To(Equal(map[string]any{"count": float64(3)}))
		Expect(running.Events.Kind).To(Equal("ticks"))

		session.Stop("user")
		info := tracestest.Ended(session)
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2", "3"}))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("ends a historical kind's session by itself once its records are stored", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, p tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
				return emitTicks(ctx, records, p.Count)
			}}, traces.Capabilities{Historical: true}),
		})
		info := tracestest.Ended(env.Start("ticks", ""))
		Expect(info.State).To(Equal(query.SessionCompleted))
		Expect(info.EventCount).To(Equal(int64(3)))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2", "3"}))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("keeps the records a handler emits as it stops", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
				<-ctx.Done()
				return emitTicks(context.Background(), records, 2)
			}}, traces.Capabilities{Live: true}),
		})
		session := env.Start("ticks", "")
		tracestest.Running(session)
		session.Stop("user")
		info := tracestest.Ended(session)
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2"}))
	})

	It("stops a session at its deadline", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"ticks": liveTicks()})
		stopAt := time.Now().Add(300 * time.Millisecond)
		managed, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "ticks", StopAt: &stopAt})
		Expect(err).ToNot(HaveOccurred())
		info := tracestest.Ended(managed.Session())
		Expect(info.State).To(Equal(query.SessionCompleted))
		Expect(info.StopReason).To(Equal(query.StopReasonDeadline))
		Expect(env.Rows(info)).To(HaveLen(3))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("fails the session with its handler's error, keeping and sealing the records it emitted", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
				if err := emitTicks(ctx, records, 1); err != nil {
					return err
				}
				return errors.New("source broke")
			}}, traces.Capabilities{Historical: true}),
		})
		info := tracestest.Ended(env.Start("ticks", ""))
		Expect(info.State).To(Equal(query.SessionFailed))
		Expect(info.Error).To(ContainSubstring("source broke"))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1"}))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("refuses an unknown kind and invalid params before any session starts", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"ticks": liveTicks()})
		_, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "nope"})
		Expect(err).To(MatchError(traces.ErrUnknownKind))
		_, err = env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "ticks", Params: json.RawMessage(`{"cnt": 1}`)})
		Expect(err).To(MatchError(traces.ErrInvalidParams))
		_, err = env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "ticks", Params: json.RawMessage(`{"count": -1}`)})
		Expect(err).To(MatchError(traces.ErrInvalidParams))
		Expect(env.Sessions.List()).To(BeEmpty())
	})

	It("reports what its capture emitted, deduplicated and dropped in the session summary", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
				for _, n := range []int{1, 1, 2} {
					if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: n}); err != nil {
						return err
					}
				}
				return nil
			}}, traces.Capabilities{Historical: true}).
				WithDeduplication(func(t tick) string { return string(rune('0' + t.N)) }, func(tickParams) time.Duration { return time.Hour }),
		})
		info := tracestest.Ended(env.Start("ticks", ""))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2"}))
		Expect(tracestest.Summary(info)).To(Equal(traces.Summary{Emitted: 2, Deduplicated: 1}))
	})

	It("keeps repeated records when the kind deduplicates nothing", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
				for range 3 {
					if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: 1}); err != nil {
						return err
					}
				}
				return nil
			}}, traces.Capabilities{Historical: true}),
		})
		Expect(numbers(env.Rows(tracestest.Ended(env.Start("ticks", ""))))).To(Equal([]string{"1", "1", "1"}))
	})

	It("truncates, parses embedded JSON and masks secrets before a record is stored", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"exchanges": traces.NewHandler[tickParams, exchange](exchanges{}, traces.Capabilities{Historical: true}).
				WithTruncation(64).WithJSONProcessor().WithSecretMasking(),
		})
		info := tracestest.Ended(env.Start("exchanges", ""))
		Expect(info.Error).To(BeEmpty())
		rows := env.Rows(info)
		Expect(rows).To(HaveLen(1))
		Expect(rows[0]["body"]).To(Equal(map[string]any{"password": "****", "n": json.Number("1")}))
		Expect(rows[0]["note"]).To(Equal(strings.Repeat("n", 64) + "…[truncated]"))
		Expect(rows[0]["truncated"]).To(Equal([]any{"note"}))
		Expect(rows[0]["url"]).ToNot(ContainSubstring("hunter2"))
	})

	It("refuses truncation for a kind whose records cannot list what was cut", func() {
		Expect(traces.NewKinds().RegisterKind("ticks", traces.NewHandler[tickParams, tick](ticker{}, traces.Capabilities{Live: true}).WithTruncation(16))).
			To(MatchError(ContainSubstring("traces.Truncation")))
	})

	It("waits for room in a full buffer, and continues once its rows commit", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
				return emitTicks(ctx, records, 10)
			}}, traces.Capabilities{Historical: true}),
		}, func(r *traces.Runtime) { r.BufferRows = 2 })
		info := tracestest.Ended(env.Start("ticks", ""))
		Expect(info.State).To(Equal(query.SessionCompleted))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}))
	})

	It("drops what a full buffer cannot take when a handler must not wait, and counts it", func() {
		attempts := make(chan []bool, 1)
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
				var taken []bool
				for n := 1; n <= 3; n++ {
					taken = append(taken, records.TryEmit(tick{At: time.Now().UTC(), N: n}))
				}
				attempts <- taken
				<-ctx.Done()
				return nil
			}}, traces.Capabilities{Live: true}),
		}, func(r *traces.Runtime) { r.BufferRows = 2; r.PollEvery = time.Hour })
		session := env.Start("ticks", "")
		Eventually(attempts).Should(Receive(Equal([]bool{true, true, false})))
		session.Stop("user")
		info := tracestest.Ended(session)
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2"}))
		Expect(tracestest.Summary(info)).To(Equal(traces.Summary{Emitted: 2, Dropped: 1}))
		Expect(info.Warning).To(ContainSubstring("dropped 1"))
	})

	It("lets a handler read back the records its session has committed", func() {
		seen := make(chan []int, 1)
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], store traces.Records[tick]) error {
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
			}}, traces.Capabilities{Live: true}),
		})
		session := env.Start("ticks", "")
		Eventually(seen).WithTimeout(5*time.Second).Should(Receive(Equal([]int{1, 2})), func() string { return fmt.Sprintf("%+v", session.Snapshot().SessionStatus) })
		session.Stop("user")
		tracestest.Ended(session)
	})

	It("stops every running capture when its registry stops all sessions", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"ticks": liveTicks()})
		first, second := env.Start("ticks", ""), env.Start("ticks", "")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		Expect(env.Sessions.StopAll(ctx)).To(Succeed())
		for _, session := range []*query.Session{first, second} {
			info := tracestest.Ended(session)
			Expect(info.State).To(Equal(query.SessionStopped))
			Expect(env.Sealed(info)).To(BeTrue())
		}
	})

	It("runs a prepared capture only once it observes its source, handing Handle what Prepare set up", func() {
		released := make(chan struct{})
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](prepared{released: released}, traces.Capabilities{Live: true}),
		})
		managed, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "ticks"})
		Expect(err).ToNot(HaveOccurred())
		session := managed.Session()
		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(5 * time.Second).Should(Equal(int64(2)))
		Consistently(released).ShouldNot(BeClosed())

		session.Stop("user")
		info := tracestest.Ended(session)
		Expect(env.Rows(info)).To(HaveLen(2))
		Expect(env.Rows(info)[1]["label"]).To(Equal("set up by prepare"))
		Expect(released).To(BeClosed())
	})

	It("refuses to start a capture it cannot prepare", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](prepared{fail: errors.New("server refused")}, traces.Capabilities{Live: true}),
		})
		_, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "ticks"})
		Expect(err).To(MatchError(ContainSubstring("server refused")))
		sessions := env.Sessions.List()
		Expect(sessions).To(HaveLen(1))
		Expect(sessions[0].State).To(Equal(query.SessionFailed))
	})

	It("restarts an ended session as a new capture into a new stream", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, p tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
				return emitTicks(ctx, records, p.Count)
			}}, traces.Capabilities{Historical: true}),
		})
		previous := tracestest.Ended(env.Start("ticks", `{"count": 2}`))
		restarted, err := env.Sessions.Restart(context.Background(), previous.ID, query.RestartOverrides{})
		Expect(err).ToNot(HaveOccurred())
		info := tracestest.Ended(restarted)
		Expect(info.RestartOf).To(Equal(previous.ID))
		Expect(info.Events.Stream).ToNot(Equal(previous.Events.Stream))
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2"}))
	})
})

type exchange struct {
	traces.Truncation
	At   time.Time `json:"at"`
	URL  string    `json:"url"`
	Note string    `json:"note"`
	Body any       `json:"body"`
}

type exchanges struct{}

func (exchanges) Params() tickParams { return tickParams{} }

func (exchanges) Schema() recordresults.ResultType[exchange] {
	return recordresults.ResultType[exchange]{Title: "Exchanges", TimeColumn: "at"}
}

func (exchanges) Handle(ctx dbcontext.Context, _ tickParams, records traces.Emitter[exchange], _ traces.Records[exchange]) error {
	return records.Emit(ctx, exchange{
		At: time.Now().UTC(), URL: "https://example.com/?password=hunter2",
		Note: strings.Repeat("n", 80), Body: `{"password": "x", "n": 1}`,
	})
}

type preparedLabel struct{}

// prepared emits a tick as it prepares, before its session runs, and one more
// from Handle labelled by what Prepare put on its context.
type prepared struct {
	fail     error
	released chan struct{}
}

func (prepared) Params() tickParams { return tickParams{} }

func (prepared) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Ticks", TimeColumn: "at"}
}

func (p prepared) Prepare(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick]) (dbcontext.Context, func(), error) {
	if p.fail != nil {
		return ctx, nil, p.fail
	}
	records.TryEmit(tick{At: time.Now().UTC(), N: 1})
	return ctx.WithValue(preparedLabel{}, "set up by prepare"), func() { close(p.released) }, nil
}

func (prepared) Handle(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
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

func (keyed) Handle(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
	for n, label := range []string{"a", "b", "a", "c", "b"} {
		if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: n + 1, Label: label}); err != nil {
			return err
		}
	}
	return nil
}

var _ = Describe("Runtime with a keyed kind", func() {
	It("stores a key emitted twice once, however its records were batched", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](keyed{}, traces.Capabilities{Historical: true}),
		})
		info := tracestest.Ended(env.Start("ticks", ""))
		Expect(info.Error).To(BeEmpty())
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1", "2", "4"}))
	})
})

// repeating emits count ticks all labelled "same", numbered from one.
type repeating struct {
	count      int
	onConflict recordstore.OnConflict
}

func (repeating) Params() tickParams { return tickParams{} }

func (r repeating) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Ticks", TimeColumn: "at", KeyColumn: "label", OnConflict: r.onConflict}
}

func (r repeating) Handle(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
	for n := 1; n <= r.count; n++ {
		if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: n, Label: "same"}); err != nil {
			return err
		}
	}
	return nil
}

var _ = Describe("Runtime with a key repeated many times", func() {
	It("stores a long run of one key once rather than failing its capture", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](repeating{count: 500}, traces.Capabilities{Historical: true}),
		})
		info := tracestest.Ended(env.Start("ticks", ""))
		Expect(info.Error).To(BeEmpty())
		Expect(numbers(env.Rows(info))).To(Equal([]string{"1"}))
	})

	It("stores the last of a key's copies for a kind that replaces", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](repeating{count: 500, onConflict: recordstore.OnConflictReplace}, traces.Capabilities{Historical: true}),
		})
		info := tracestest.Ended(env.Start("ticks", ""))
		Expect(info.Error).To(BeEmpty())
		Expect(numbers(env.Rows(info))).To(Equal([]string{"500"}))
	})
})

var _ = Describe("Runtime stopping a capture with a large final drain", func() {
	It("commits what one drain can, reports the rest dropped, and seals the stream", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"ticks": traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
				<-ctx.Done()
				return emitTicks(context.Background(), records, 70_000)
			}}, traces.Capabilities{Live: true}),
		})
		session := env.Start("ticks", "")
		tracestest.Running(session)
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
