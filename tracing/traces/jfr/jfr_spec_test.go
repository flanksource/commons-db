// Specs for the jfr trace kind: a recording a host's recorder starts is dumped
// at each checkpoint and once more as the capture stops, and only what each
// dump adds is stored.

package jfr_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"

	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/jfr"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
)

// fakeRecorder starts one fakeRecording, whose dumps return its recordings in
// turn, the last one again once they run out.
type fakeRecorder struct {
	mu        sync.Mutex
	dumps     [][]byte
	startErr  error
	stopErr   error
	started   []jfr.Options
	names     []string
	recording *fakeRecording
}

func (r *fakeRecorder) Start(_ context.Context, options jfr.Options, name string) (jfr.Recording, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started, r.names = append(r.started, options), append(r.names, name)
	if r.startErr != nil {
		return nil, r.startErr
	}
	r.recording = &fakeRecording{recorder: r}
	return r.recording, nil
}

func (r *fakeRecorder) starts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.started)
}

type fakeRecording struct {
	recorder *fakeRecorder
	dumped   int
	stopped  int
}

func (f *fakeRecording) Dump(context.Context) ([]byte, error) {
	f.recorder.mu.Lock()
	defer f.recorder.mu.Unlock()
	dumps := f.recorder.dumps
	f.dumped++
	return dumps[min(f.dumped, len(dumps))-1], nil
}

func (f *fakeRecording) Stop(context.Context) error {
	f.recorder.mu.Lock()
	defer f.recorder.mu.Unlock()
	f.stopped++
	return f.recorder.stopErr
}

func (f *fakeRecording) Handle() string { return "recording-7" }

func (f *fakeRecording) Metadata() []query.SessionMetadata {
	return []query.SessionMetadata{{Name: "jfr.target", Label: "Target", Value: "oipa"}}
}

func (f *fakeRecording) counts() (dumped, stopped int) {
	f.recorder.mu.Lock()
	defer f.recorder.mu.Unlock()
	return f.dumped, f.stopped
}

// hostParams are a host's own form for a capture, which maps them onto the
// options its recorder starts.
type hostParams struct {
	Engine  string `json:"engine"`
	Event   string `json:"event,omitempty"`
	Every   string `json:"every,omitempty"`
	Threads bool   `json:"threads,omitempty"`
}

func (p hostParams) JFROptions() (jfr.Options, error) {
	options := jfr.Options{Engine: jfr.Engine(p.Engine), Event: p.Event, Threads: p.Threads}
	if options.Engine == jfr.EngineJFR {
		options.Settings = "profile"
	}
	if p.Every != "" {
		every, err := time.ParseDuration(p.Every)
		if err != nil {
			return options, err
		}
		options.Every = every
	}
	return options, nil
}

func (p hostParams) Validate() error {
	options, err := p.JFROptions()
	if err != nil {
		return err
	}
	return options.Validate()
}

// laxParams map themselves onto options without validating them.
type laxParams struct {
	Engine string `json:"engine"`
}

func (p laxParams) JFROptions() (jfr.Options, error) {
	return jfr.Options{Engine: jfr.Engine(p.Engine)}, nil
}

func startEnv(recorder *fakeRecorder) tracestest.Env {
	return tracestest.NewEnv(map[string]traces.TracePlugin{"jvm_jfr": jfr.NewKindFor[hostParams](recorder)})
}

// sampleCounts adds up the stored rows' samples by event.
func sampleCounts(rows []recordstore.Row) map[string]int64 {
	counts := map[string]int64{}
	for _, row := range rows {
		samples, ok := row["samples"].(int64)
		Expect(ok).To(BeTrue(), "samples is an integer column, got %T", row["samples"])
		counts[row["event"].(string)] += samples
	}
	return counts
}

var (
	jdkCounts   = map[string]int64{jfr.EventCPU: 172, jfr.EventAlloc: 720, jfr.EventLock: 67, jfr.EventPark: 72}
	asyncCounts = map[string]int64{jfr.EventCPU: 582, jfr.EventAlloc: 10210, jfr.EventLock: 1773, jfr.EventPark: 435}
)

var _ = Describe("jfr trace kind", func() {
	It("describes its session by the recording, stores the final dump's samples and stops the recording", func() {
		recorder := &fakeRecorder{dumps: [][]byte{fixture(jdkProfile)}}
		env := startEnv(recorder)
		session := env.Start("jvm_jfr", `{"engine": "jfr"}`)
		tracestest.Running(session)
		running := session.Snapshot()
		Expect(running.Handle).To(Equal("recording-7"))
		Expect(running.Metadata).To(Equal([]query.SessionMetadata{
			{Name: "jfr.options", Label: "Profiling", Value: "jfr settings=profile"},
			{Name: "jfr.target", Label: "Target", Value: "oipa"},
		}))
		Expect(recorder.started).To(Equal([]jfr.Options{{Engine: jfr.EngineJFR, Settings: "profile"}}))
		Expect(recorder.names).To(HaveLen(1))
		Expect(recorder.names[0]).ToNot(BeEmpty())

		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.Error).To(BeEmpty())
		Expect(sampleCounts(env.Rows(info))).To(Equal(jdkCounts))
		dumped, stopped := recorder.recording.counts()
		Expect(dumped).To(Equal(1), "the final drain dumps once")
		Expect(stopped).To(Equal(1))

		report := tracestest.Summary(info).Report
		Expect(report).To(HaveKeyWithValue("engine", "jfr"))
		Expect(report).To(HaveKeyWithValue("recording", "recording-7"))
		Expect(report).To(HaveKeyWithValue("dumps", float64(1)))
		Expect(report).To(HaveKeyWithValue("bytes", float64(len(fixture(jdkProfile)))))
		Expect(report).To(HaveKeyWithValue("events", HaveKeyWithValue(jfr.EventCPU, float64(172))))
		Expect(report).To(HaveKeyWithValue("latest", "2026-10-13T11:06:14.956490708Z"))
	})

	It("stores at each checkpoint only what the recording added since the dump before", func() {
		async, jdk := fixture(asyncCPULock), fixture(jdkProfile)
		grown := append(append([]byte{}, async...), jdk...)
		recorder := &fakeRecorder{dumps: [][]byte{async, grown}}
		env := startEnv(recorder)
		managed, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "jvm_jfr", Params: json.RawMessage(`{"engine": "async-profiler", "event": "cpu"}`)})
		Expect(err).ToNot(HaveOccurred())
		session := managed.Session()
		tracestest.Running(session)

		Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())
		first := session.Snapshot().EventCount
		Expect(first).To(BeNumerically(">", 0))
		Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())
		second := session.Snapshot().EventCount
		Expect(second).To(BeNumerically(">", first))
		Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())
		Expect(session.Snapshot().EventCount).To(Equal(second), "a dump that added nothing stores nothing")

		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.Error).To(BeEmpty())
		rows := env.Rows(info)
		Expect(int64(len(rows))).To(Equal(second), "the final drain added nothing either")
		total := map[string]int64{}
		for event, n := range asyncCounts {
			total[event] = n + jdkCounts[event]
		}
		Expect(sampleCounts(rows)).To(Equal(total))
		dumped, _ := recorder.recording.counts()
		Expect(dumped).To(Equal(4))
		Expect(tracestest.Summary(info).Report).To(HaveKeyWithValue("dumps", float64(4)))
	})

	It("dumps on its own every interval it is given, storing each sample once", func() {
		recorder := &fakeRecorder{dumps: [][]byte{fixture(jdkProfile)}}
		env := startEnv(recorder)
		session := env.Start("jvm_jfr", `{"engine": "jfr", "every": "20ms", "threads": true}`)
		tracestest.Running(session)
		Eventually(func() int {
			dumped, _ := recorder.recording.counts()
			return dumped
		}).WithTimeout(5 * time.Second).Should(BeNumerically(">=", 3))
		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.Error).To(BeEmpty())
		rows := env.Rows(info)
		Expect(sampleCounts(rows)).To(Equal(jdkCounts))
		Expect(rows).To(ContainElement(HaveKeyWithValue("thread", "burner")))
	})

	It("records on its session that the recording could not be stopped, keeping its samples", func() {
		recorder := &fakeRecorder{dumps: [][]byte{fixture(jdkProfile)}, stopErr: errors.New("connection refused")}
		env := startEnv(recorder)
		session := env.Start("jvm_jfr", `{"engine": "jfr"}`)
		tracestest.Running(session)
		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(info.Error).To(And(ContainSubstring("recording-7"), ContainSubstring("connection refused")))
		Expect(sampleCounts(env.Rows(info))).To(Equal(jdkCounts))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("ends its session with the error of a dump it cannot decode", func() {
		recorder := &fakeRecorder{dumps: [][]byte{[]byte("<html>502 Bad Gateway</html>")}}
		env := startEnv(recorder)
		session := env.Start("jvm_jfr", `{"engine": "jfr"}`)
		tracestest.Running(session)
		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.Error).To(ContainSubstring("not a JFR recording"))
		_, stopped := recorder.recording.counts()
		Expect(stopped).To(Equal(1), "the recording is stopped all the same")
	})

	It("refuses to start when the recording cannot be started", func() {
		recorder := &fakeRecorder{startErr: errors.New("JFR unavailable on this JVM")}
		env := startEnv(recorder)
		_, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "jvm_jfr", Params: json.RawMessage(`{"engine": "jfr"}`)})
		Expect(err).To(MatchError(ContainSubstring("JFR unavailable on this JVM")))
	})

	DescribeTable("refuses options a recording could not honour before a session exists",
		func(params, message string) {
			recorder := &fakeRecorder{}
			env := startEnv(recorder)
			_, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "jvm_jfr", Params: json.RawMessage(params)})
			Expect(err).To(MatchError(traces.ErrInvalidParams))
			Expect(err).To(MatchError(ContainSubstring(message)))
			Expect(recorder.starts()).To(BeZero())
			Expect(env.Sessions.List()).To(BeEmpty())
		},
		Entry("an unknown engine", `{"engine": "perf"}`, `engine "perf"`),
		Entry("async-profiler without its one event", `{"engine": "async-profiler"}`, "event"),
		Entry("async-profiler with an event it does not profile", `{"engine": "async-profiler", "event": "park"}`, `event "park"`),
		Entry("jfr with an event of its own", `{"engine": "jfr", "event": "cpu"}`, "event"),
		Entry("a negative dump interval", `{"engine": "jfr", "every": "-1s"}`, "every"),
	)

	It("refuses at prepare the options a host did not validate, starting no recording", func() {
		recorder := &fakeRecorder{}
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"jvm_jfr": jfr.NewKindFor[laxParams](recorder)})
		_, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "jvm_jfr", Params: json.RawMessage(`{"engine": "jfr"}`)})
		Expect(err).To(MatchError(ContainSubstring("settings")))
		Expect(recorder.starts()).To(BeZero())
	})
})

var _ = Describe("jfr result profile", func() {
	It("follows a capture's stream and searches its frames, stacks and threads", func() {
		env := startEnv(&fakeRecorder{})
		profile, err := env.Results.Registry.Get(context.Background(), "traces/jvm_jfr")
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Provider.Type).To(Equal(recordresults.ProviderType))
		var roles []query.ParamRole
		for _, param := range profile.Params {
			roles = append(roles, param.Role)
		}
		Expect(roles).To(ContainElement(query.ParamRoleSearch))
	})

	It("prefills a host's form from its defaults", func() {
		kind := jfr.NewKindFor[hostParams](&fakeRecorder{}).WithDefaults(hostParams{Engine: "jfr"})
		schema, err := kind.Params()
		Expect(err).ToNot(HaveOccurred())
		Expect(schema.Properties["engine"].Default).To(Equal("jfr"))
	})
})
