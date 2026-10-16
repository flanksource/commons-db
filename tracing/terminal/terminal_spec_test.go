package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

const liveParams = `{"width": 40, "height": 10, "snapshotInterval": "20ms", "command": ["/bin/sh", "-c", "echo hi"]}`

func newEnv(configure ...func(*tracing.Runtime)) tracingtest.Env {
	return tracingtest.NewEnv(map[string]tracing.TracePlugin{Kind: NewKind()}, configure...)
}

func startCapture(env tracingtest.Env, params string) (*query.ManagedSession, string) {
	managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: Kind, Params: json.RawMessage(params)})
	Expect(err).ToNot(HaveOccurred())
	tracingtest.Running(managed.Session())
	return managed, managed.Session().Snapshot().Events.Stream
}

func exportCast(env tracingtest.Env, ref tracing.TraceRef) string {
	var out bytes.Buffer
	Expect(env.Runtime.Export(context.Background(), ref, Cast.Name, &out)).To(Succeed())
	return out.String()
}

func records(env tracingtest.Env, ref tracing.TraceRef) []Event {
	var rows []Event
	Expect(tracing.ReadRecords(context.Background(), env.Runtime, ref, func(e Event) error {
		rows = append(rows, e)
		return nil
	})).To(Succeed())
	return rows
}

var _ = Describe("terminal", func() {
	It("records the bytes a host writes, its resizes and its exit, and exports a window as a cast", func() {
		env := newEnv()
		managed, stream := startCapture(env, liveParams)
		w, err := Writer(stream)
		Expect(err).ToNot(HaveOccurred())

		_, err = w.Write([]byte("first\r\nfirst\r\n"))
		Expect(err).ToNot(HaveOccurred())
		mark, err := env.Runtime.Mark(context.Background(), managed)
		Expect(err).ToNot(HaveOccurred())
		Expect(mark).To(BeNumerically(">", 0))

		_, err = w.Write([]byte("second\r\n"))
		Expect(err).ToNot(HaveOccurred())
		Expect(Resize(stream, 60, 20)).To(Succeed())
		Expect(SetExitCode(stream, 7)).To(Succeed())
		Expect(w.Close()).To(Succeed())
		info := tracingtest.Ended(managed.Session())
		Expect(info.Error).To(BeEmpty())

		whole := tracing.TraceRef{Kind: Kind, Stream: stream}
		var fed strings.Builder
		var kinds []string
		for _, row := range records(env, whole) {
			kinds = append(kinds, row.Kind)
			if row.Kind == KindOutput {
				fed.WriteString(row.Data)
			}
		}
		Expect(fed.String()).To(Equal("first\r\nfirst\r\nsecond\r\n"))
		Expect(kinds[len(kinds)-2:]).To(Equal([]string{KindSnapshot, KindExit}))

		summary, err := env.Runtime.Summarize(context.Background(), whole)
		Expect(err).ToNot(HaveOccurred())
		Expect(summary).To(And(
			HaveKeyWithValue("exit_code", 7),
			HaveKeyWithValue("width", 40),
			HaveKeyWithValue("height", 10),
			HaveKeyWithValue("final", "first\nfirst\nsecond\n"),
			HaveKeyWithValue("has_duplicates", true),
			HaveKeyWithValue("truncated", false),
		))

		after := exportCast(env, tracing.TraceRef{Kind: Kind, Stream: stream, AfterSeq: mark})
		Expect(after).To(ContainSubstring(`"second\r\n"`))
		events := strings.Split(strings.TrimSpace(after), "\n")[1:]
		Expect(strings.Join(events, "\n")).ToNot(ContainSubstring("first"), "a window holds only the output after its mark")
		Expect(after).To(ContainSubstring(`"r","60x20"`))
		Expect(exportCast(env, whole)).To(ContainSubstring(`"command":"/bin/sh -c echo hi"`))
	})

	It("refuses a writer for a stream no capture is feeding", func() {
		_, err := Writer("no-such-stream")
		Expect(err).To(MatchError(ContainSubstring(`no terminal capture for stream "no-such-stream"`)))
		Expect(Resize("no-such-stream", 1, 1)).To(MatchError(ContainSubstring("no terminal capture")))
		Expect(SetExitCode("no-such-stream", 0)).To(MatchError(ContainSubstring("no terminal capture")))
	})

	It("unregisters the stream once the session ends, so its writers fail", func() {
		env := newEnv()
		managed, stream := startCapture(env, liveParams)
		w, err := Writer(stream)
		Expect(err).ToNot(HaveOccurred())
		Expect(w.Close()).To(Succeed())
		tracingtest.Ended(managed.Session())

		Eventually(func() error { _, err := Writer(stream); return err }).Should(HaveOccurred())
		_, err = w.Write([]byte("late"))
		Expect(err).To(MatchError(ContainSubstring("has ended")))
	})

	It("never blocks the host: events a full buffer cannot take are dropped, counted and marked truncated", func() {
		env := newEnv(func(r *tracing.Runtime) { r.BufferRows = 1 })
		managed, stream := startCapture(env, liveParams)
		w, err := Writer(stream)
		Expect(err).ToNot(HaveOccurred())

		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 500 {
				_, _ = w.Write([]byte("x"))
			}
		}()
		Eventually(done).WithTimeout(5 * time.Second).Should(BeClosed())
		Expect(w.Close()).To(Succeed())
		info := tracingtest.Ended(managed.Session())

		Expect(tracingtest.Summary(info).Dropped).To(BeNumerically(">", 0))
		summary, err := env.Runtime.Summarize(context.Background(), tracing.TraceRef{Kind: Kind, Stream: stream})
		Expect(err).ToNot(HaveOccurred())
		Expect(summary).To(HaveKeyWithValue("truncated", true))
	})

	It("stops recording output past maxBytes but goes on settling the screen", func() {
		env := newEnv()
		managed, stream := startCapture(env, `{"width": 40, "height": 10, "maxBytes": "8B"}`)
		w, err := Writer(stream)
		Expect(err).ToNot(HaveOccurred())
		_, _ = w.Write([]byte("12345\r\n"))
		_, _ = w.Write([]byte("beyond the cap\r\n"))
		Expect(w.Close()).To(Succeed())
		tracingtest.Ended(managed.Session())

		summary, err := env.Runtime.Summarize(context.Background(), tracing.TraceRef{Kind: Kind, Stream: stream})
		Expect(err).ToNot(HaveOccurred())
		Expect(summary).To(And(
			HaveKeyWithValue("events", 1),
			HaveKeyWithValue("truncated", true),
			HaveKeyWithValue("final", "12345\nbeyond the cap\n"),
		))
	})

	It("refuses params that give no usable terminal", func() {
		kind := NewKind()
		for _, params := range []string{`{"width": -1}`, `{"snapshotInterval": "soon"}`, `{"maxBytes": "lots"}`, `{"height": 0}`} {
			Expect(kind.ValidateParams(json.RawMessage(params))).ToNot(Succeed(), params)
		}
		Expect(kind.ValidateParams(nil)).To(Succeed())
	})
})
