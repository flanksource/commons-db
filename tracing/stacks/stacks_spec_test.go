package stacks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/process"
	"github.com/flanksource/commons-db/tracing/stacks"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

// nativeTool is the first native stack sampler the host's OS is sampled with.
func nativeTool() string {
	return map[string]string{"darwin": "sample", "linux": "eu-stack"}[goruntime.GOOS]
}

// addresses answers the pprof and gops dumpers from what children announced.
type addresses map[int]string

func (a addresses) of(pid int) (string, bool) {
	addr, ok := a[pid]
	return addr, ok
}

func newEnv(dumpers ...stacks.Dumper) tracingtest.Env {
	registry, err := stacks.NewDumpers(dumpers...)
	Expect(err).ToNot(HaveOccurred())
	return tracingtest.NewEnv(map[string]tracing.TracePlugin{
		stacks.Kind:      stacks.NewKind(registry),
		process.TreeKind: process.NewTreeKind(process.NewSampler(), process.TreeOptions{Dumpers: registry}),
	})
}

func start(env tracingtest.Env, kind string, rootPID int) *query.ManagedSession {
	managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{
		Kind: kind, Params: json.RawMessage(`{"rootPid": ` + strconv.Itoa(rootPID) + `}`),
	})
	Expect(err).ToNot(HaveOccurred())
	tracingtest.Running(managed.Session())
	return managed
}

func checkpoint(env tracingtest.Env, managed *query.ManagedSession, params string) error {
	return env.Runtime.Checkpoint(tracing.WithCheckpointParams(context.Background(), json.RawMessage(params)), managed)
}

func ref(managed *query.ManagedSession) tracing.TraceRef {
	return tracing.TraceRef{Kind: stacks.Kind, Stream: managed.Session().Snapshot().Events.Stream}
}

func dumped(env tracingtest.Env, managed *query.ManagedSession) []stacks.Stack {
	var records []stacks.Stack
	Expect(tracing.ReadRecords(context.Background(), env.Runtime, ref(managed), func(record stacks.Stack) error {
		records = append(records, record)
		return nil
	})).To(Succeed())
	return records
}

// parked matches the record of the child's goroutine parked on a channel,
// its frames innermost first.
func parked(transport, innermost string) OmegaMatcher {
	return MatchFields(IgnoreExtras, Fields{
		"Transport": Equal(transport), "Format": Equal(stacks.FormatGo), "State": Equal("chan receive"),
		"Frames": And(
			WithTransform(func(frames []stacks.Frame) string { return frames[0].Func }, Equal(innermost)),
			ContainElement(HaveField("Func", "main.parkedForever")),
		),
		"CreatedBy": PointTo(HaveField("Func", "main.main")),
		"Raw":       And(HavePrefix("goroutine "), ContainSubstring("main.parkedForever")),
		"Error":     BeEmpty(),
	})
}

var _ = Describe("stacks", func() {
	It("dumps the host's own goroutines in process, one record each, and summarizes them by state", func() {
		env := newEnv(stacks.InProcess())
		managed := start(env, stacks.Kind, os.Getpid())
		Expect(checkpoint(env, managed, `{"reason": "manual"}`)).To(Succeed())

		records := dumped(env, managed)
		Expect(len(records)).To(BeNumerically(">", 1))
		Expect(records).To(HaveEach(MatchFields(IgnoreExtras, Fields{
			"Transport": Equal("inprocess"), "PID": Equal(os.Getpid()), "Reason": Equal("manual"),
			"Key": MatchRegexp(`^%d@\d+$`, os.Getpid()), "CheckpointID": Equal(records[0].CheckpointID),
		})))
		Expect(records).To(ContainElement(HaveField("State", "running")))
		summary, err := env.Runtime.Summarize(context.Background(), ref(managed))
		Expect(err).ToNot(HaveOccurred())
		Expect(summary).To(MatchAllKeys(Keys{
			"count": Equal(len(records)), "checkpoints": Equal(1), "goroutines": Equal(len(records)),
			"byState": HaveKeyWithValue("running", BeNumerically(">=", 1)),
		}))
	})

	It("dumps a Go service live over its pprof server and gops agent, leaving it running", func() {
		child := tracingtest.StartGoChild(goChild, "-pprof", "-gops", GinkgoT().TempDir())
		pprof, gops := addresses{child.PID: child.Announced["pprof"]}, addresses{child.PID: child.Announced["gops"]}
		env := newEnv(stacks.InProcess(), stacks.PProf(pprof.of), stacks.Gops(gops.of))
		tree := start(env, process.TreeKind, child.PID)
		managed := start(env, stacks.Kind, child.PID)

		Expect(checkpoint(env, managed, `{"pid": `+strconv.Itoa(child.PID)+`, "reason": "periodic"}`)).To(Succeed())
		Expect(checkpoint(env, managed, `{"transport": "gops"}`)).To(Succeed())

		records := dumped(env, managed)
		// A live dump leaves out the runtime's own frames.
		Expect(records).To(ContainElement(parked("pprof", "main.parkedForever")), "the first available non-destructive transport")
		Expect(records).To(ContainElement(parked("gops", "main.parkedForever")))
		Expect(syscall.Kill(child.PID, 0)).To(Succeed(), "neither dump killed it")
		Eventually(func() []recordstore.Row { return env.Rows(tree.Session().Snapshot()) }).Should(ContainElement(And(
			HaveKeyWithValue("pid", int64(child.PID)),
			HaveKeyWithValue("stackTransports", ConsistOf(
				map[string]any{"transport": "pprof", "destructive": false},
				map[string]any{"transport": "gops", "destructive": false},
			)),
		)), "the tree lists the transports that can dump the child")

		var text bytes.Buffer
		Expect(env.Runtime.Export(context.Background(), ref(managed), stacks.Text.Name, &text)).To(Succeed())
		Expect(text.String()).To(And(ContainSubstring("transport=pprof reason=periodic"), ContainSubstring("transport=gops"), ContainSubstring("main.parkedForever")))
	})

	It("stores the goroutines a Go process printed as SIGQUIT killed it, as a dump taken elsewhere", func() {
		child := tracingtest.StartGoChild(goChild)
		env := newEnv(stacks.InProcess())
		managed := start(env, stacks.Kind, os.Getpid())
		Expect(syscall.Kill(child.PID, syscall.SIGQUIT)).To(Succeed())
		Eventually(child.Exited()).WithTimeout(10 * time.Second).Should(BeClosed())
		dump, err := json.Marshal(stacks.Dump{Format: stacks.FormatGo, Text: child.Stderr.String(), CollectedAt: time.Now().UTC()})
		Expect(err).ToNot(HaveOccurred())

		key := strconv.Itoa(child.PID) + "@1"
		Expect(checkpoint(env, managed, `{"key": "`+key+`", "reason": "timeout", "dump": `+string(dump)+`}`)).To(Succeed())
		Expect(dumped(env, managed)).To(ContainElement(And(parked("", "runtime.gopark"), HaveField("Key", key), HaveField("PID", child.PID), HaveField("Reason", "timeout"))))
	})

	It("samples a real process's threads with the OS's native sampler", func() {
		path, err := exec.LookPath(nativeTool())
		if err != nil {
			Skip("no native stack sampler on PATH: " + nativeTool())
		}
		child := tracingtest.StartGoChild(goChild)
		env := newEnv(stacks.InProcess(), stacks.Native(stacks.NativeOptions{}))
		managed := start(env, stacks.Kind, child.PID)
		Expect(checkpoint(env, managed, `{"reason": "manual"}`)).To(Succeed())
		Expect(dumped(env, managed)).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
			"Transport": Equal("native"), "Format": Equal(stacks.FormatNative), "PID": Equal(child.PID),
			"Raw": ContainSubstring(strconv.Itoa(child.PID)), "Frames": BeEmpty(), "Error": BeEmpty(),
		})), "sampled with %s", path)
	})

	It("records a dump that fails, and why no transport can dump a process", func() {
		child := tracingtest.StartGoChild(goChild)
		failing := stacks.Native(stacks.NativeOptions{
			LookPath: func(file string) (string, error) { return "/fake/" + file, nil },
			Run:      func(context.Context, string, ...string) (string, error) { return "", errors.New("task_for_pid denied") },
		})
		env := newEnv(stacks.InProcess(), failing)
		managed := start(env, stacks.Kind, child.PID)
		Expect(checkpoint(env, managed, `{"transport": "native"}`)).To(Succeed())
		Expect(dumped(env, managed)).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
			"Transport": Equal("native"), "Error": ContainSubstring("task_for_pid denied"), "TakenAt": Not(BeZero()), "Format": BeEmpty(),
		})))

		bare := newEnv(stacks.InProcess())
		session := start(bare, stacks.Kind, child.PID)
		Expect(checkpoint(bare, session, `{}`)).To(Succeed())
		Expect(dumped(bare, session)).To(ConsistOf(HaveField("Error", And(ContainSubstring("no stack transport"), ContainSubstring("not the host process")))))
	})

	DescribeTable("refuses a checkpoint it cannot take",
		func(params, refusal string) {
			child := tracingtest.StartGoChild(goChild)
			env := newEnv(stacks.InProcess())
			err := checkpoint(env, start(env, stacks.Kind, child.PID), params)
			Expect(err).To(MatchError(tracing.ErrCheckpointParams))
			Expect(err).To(MatchError(ContainSubstring(refusal)))
		},
		Entry("an unknown key", `{"key": "1@1"}`, `no live process "1@1"`),
		Entry("a pid outside the tree", `{"pid": 1}`, "no live process with pid 1"),
		Entry("both a key and a pid", `{"key": "1@1", "pid": 1}`, "not both"),
		Entry("an unknown transport", `{"transport": "sigquit"}`, `unknown stack transport "sigquit"`),
		Entry("in-process stacks of another process", `{"transport": "inprocess"}`, "not the host process"),
		Entry("a dump and a transport", `{"pid": 1, "transport": "inprocess", "dump": {"format": "go", "text": "x", "collectedAt": "2026-10-16T09:30:00Z"}}`, "takes no transport"),
		Entry("a dump with no collection time", `{"pid": 1, "dump": {"format": "go", "text": "x"}}`, "collectedAt"),
		Entry("a dump in an unknown format", `{"pid": 1, "dump": {"format": "jstack", "text": "x", "collectedAt": "2026-10-16T09:30:00Z"}}`, `format "jstack"`),
		Entry("an unknown field", `{"mode": "native"}`, `unknown field "mode"`),
	)
})
