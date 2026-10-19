package goprof_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/google/pprof/profile"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/goprof"
	"github.com/flanksource/commons-db/tracing/stacks"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

func newEnv(address stacks.AddressFunc) (tracingtest.Env, *query.ManagedSession) {
	env := tracingtest.NewEnv(map[string]tracing.TracePlugin{goprof.Kind: goprof.NewKind(address)})
	managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: goprof.Kind, Params: json.RawMessage(`{}`)})
	Expect(err).ToNot(HaveOccurred())
	tracingtest.Running(managed.Session())
	return env, managed
}

func checkpoint(env tracingtest.Env, managed *query.ManagedSession, params string) error {
	return env.Runtime.Checkpoint(tracing.WithCheckpointParams(context.Background(), json.RawMessage(params)), managed)
}

func ref(managed *query.ManagedSession) tracing.TraceRef {
	return tracing.TraceRef{Kind: goprof.Kind, Stream: managed.Session().Snapshot().Events.Stream}
}

func profiled(env tracingtest.Env, managed *query.ManagedSession) []goprof.Sample {
	var records []goprof.Sample
	Expect(tracing.ReadRecords(context.Background(), env.Runtime, ref(managed), func(record goprof.Sample) error {
		records = append(records, record)
		return nil
	})).To(Succeed())
	return records
}

// totals are each sample type's total value, and the types in order.
func totals(records []goprof.Sample) (map[string]int64, []string) {
	byType := map[string]int64{}
	var order []string
	for _, record := range records {
		if _, ok := byType[record.SampleType]; !ok {
			order = append(order, record.SampleType)
		}
		byType[record.SampleType] += record.Value
	}
	return byType, order
}

var _ = Describe("goprof", func() {
	It("ingests a test binary's -cpuprofile file by stack, and exports it back as a pprof profile", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cpu.pprof")
		run := exec.Command(goChild, "-cpuprofile", path)
		Expect(run.Run()).To(Succeed())
		original, err := os.Open(path)
		Expect(err).ToNot(HaveOccurred())
		defer original.Close()
		written, err := profile.Parse(original)
		Expect(err).ToNot(HaveOccurred())
		env, managed := newEnv(nil)
		key := strconv.Itoa(run.Process.Pid) + "@1"

		Expect(checkpoint(env, managed, fmt.Sprintf(`{"key": %q, "source": "file", "profile": "cpu", "path": %q, "reason": "exit"}`, key, path))).To(Succeed())
		records := profiled(env, managed)
		byType, order := totals(records)
		Expect(order).To(Equal([]string{"samples", "cpu"}), "the profile's own sample-type order")
		Expect(byType["cpu"]).To(BeNumerically(">", 0))
		Expect(records).To(HaveEach(And(HaveField("Key", key), HaveField("PID", run.Process.Pid), HaveField("Source", "file"),
			HaveField("Reason", "exit"), HaveField("CheckpointID", records[0].CheckpointID), HaveField("TakenAt", records[0].TakenAt))))
		Expect(records).To(ContainElement(HaveField("Frames", ContainElement(HaveField("Func", "main.burnCPU")))))

		var exported bytes.Buffer
		Expect(env.Runtime.Export(context.Background(), ref(managed), goprof.PProf.Name, &exported)).To(Succeed())
		parsed, err := profile.Parse(&exported)
		Expect(err).ToNot(HaveOccurred())
		Expect(parsed.SampleType).To(HaveExactElements(HaveField("Type", "samples"), HaveField("Type", "cpu")))
		Expect(sumOf(parsed, 1)).To(Equal(byType["cpu"]))
		Expect(sumOf(parsed, 1)).To(Equal(sumOf(written, 1)), "every CPU nanosecond the binary wrote")
		Expect(parsed.Sample[0].Location[0].Line[0].Function.Name).To(Equal(records[0].Frames[0].Func), "innermost frame first")
	})

	It("reads a live heap profile over pprof and gops", func() {
		child := tracingtest.StartGoChild(goChild, "-pprof", "-gops", GinkgoT().TempDir())
		addresses := map[string]string{goprof.SourcePProf: child.Announced["pprof"], goprof.SourceGops: child.Announced["gops"]}
		for source, addr := range addresses {
			env := tracingtest.NewEnv(map[string]tracing.TracePlugin{goprof.Kind: goprof.NewKind(func(pid int) (string, bool) { return addr, pid == child.PID })})
			managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: goprof.Kind, Params: json.RawMessage(`{"rootPid": ` + strconv.Itoa(child.PID) + `}`)})
			Expect(err).ToNot(HaveOccurred())
			Expect(checkpoint(env, managed, `{"source": "`+source+`", "profile": "heap", "reason": "manual"}`)).To(Succeed())
			_, order := totals(profiled(env, managed))
			Expect(order).To(Equal([]string{"alloc_objects", "alloc_space", "inuse_objects", "inuse_space"}), "read over %s", source)
		}
	})

	DescribeTable("refuses a checkpoint it cannot read",
		func(params, refusal string) {
			env, managed := newEnv(nil)
			err := checkpoint(env, managed, params)
			Expect(err).To(MatchError(tracing.ErrCheckpointParams))
			Expect(err).To(MatchError(ContainSubstring(refusal)))
		},
		Entry("no params", `{}`, `profile ""`),
		Entry("an unknown profile", `{"source": "pprof", "profile": "trace", "seconds": 1}`, `profile "trace"`),
		Entry("an unknown source", `{"source": "jfr", "profile": "cpu"}`, `source "jfr"`),
		Entry("a live cpu profile with no duration", `{"source": "pprof", "profile": "cpu"}`, "needs the seconds"),
		Entry("a gops profile it cannot read", `{"source": "gops", "profile": "mutex"}`, "not mutex"),
		Entry("a file with no process", `{"source": "file", "profile": "cpu", "path": "cpu.pprof"}`, "key or pid"),
		Entry("a path for a live source", `{"source": "pprof", "profile": "heap", "path": "cpu.pprof"}`, "path"),
		Entry("a pprof source with no addresses", `{"source": "pprof", "profile": "heap"}`, "has no pprof address"),
	)
})

func sumOf(parsed *profile.Profile, index int) int64 {
	var sum int64
	for _, sample := range parsed.Sample {
		sum += sample.Value[index]
	}
	return sum
}
