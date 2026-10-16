package process_test

import (
	"context"
	"errors"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/process"
)

// nativeTool is the first native stack sampler the host's OS is sampled with.
func nativeTool() string {
	return map[string]string{"darwin": "sample", "linux": "eu-stack"}[goruntime.GOOS]
}

// fakeSampler stands in for the OS's native stack sampler, answering every
// pid with stack, or failing with fail.
type fakeSampler struct {
	mu    sync.Mutex
	calls [][]string
	stack string
	fail  error
}

func (f *fakeSampler) options() process.TreeOptions {
	return process.TreeOptions{
		LookPath: func(file string) (string, error) { return "/fake/" + file, nil },
		Run: func(_ context.Context, path string, args ...string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calls = append(f.calls, append([]string{path}, args...))
			return f.stack, f.fail
		},
	}
}

var _ = Describe("process_tree stacks", func() {
	const reason = "package-timeout:example/pkg"
	collectedAt := time.Date(2026, time.October, 16, 9, 30, 0, 0, time.UTC)

	It("writes a stack captured elsewhere onto a live process by pid and onto an exited one by key", func() {
		root := spawnTree("sleep 1 & sleep 30 & wait")
		env := newEnv()
		managed := start(env, process.TreeKind, root)
		Eventually(func() map[string]recordstore.Row { return nodes(env, managed.Session()) }).WithTimeout(5 * time.Second).
			Should(HaveKeyWithValue("sleep 1", HaveKeyWithValue("exitedAt", Not(BeNil()))))
		before := nodes(env, managed.Session())
		live, exited := before["sleep 30"], before["sleep 1"]

		Expect(process.Checkpoint(context.Background(), env.Runtime, managed, process.CheckpointRequest{
			PID: int(live["pid"].(int64)), Reason: reason,
			Stack: &process.Stack{Text: "goroutine 1 [running]:", CollectedAt: collectedAt},
		})).To(Succeed())
		Expect(process.Checkpoint(context.Background(), env.Runtime, managed, process.CheckpointRequest{
			Key: exited["key"].(string), Reason: "timeout",
			Stack: &process.Stack{Error: "SIGQUIT dump truncated", CollectedAt: collectedAt},
		})).To(Succeed())

		after := nodes(env, managed.Session())
		Expect(after["sleep 30"]).To(And(
			HaveKeyWithValue("stack", "goroutine 1 [running]:"), HaveKeyWithValue("stackReason", reason),
			HaveKeyWithValue("stackCollectedAt", Not(BeNil())), HaveKeyWithValue("exitedAt", BeNil()),
		))
		Expect(after["sleep 1"]).To(And(
			HaveKeyWithValue("stackError", "SIGQUIT dump truncated"), HaveKeyWithValue("stackReason", "timeout"),
			HaveKeyWithValue("status", "exited"), HaveKeyWithValue("exitedAt", Not(BeNil())),
		), "an exited process keeps its exit as it takes the stack")
	})

	It("samples a process's stacks with the native sampler, recording what it fails with", func() {
		root := spawnTree("sleep 30 & wait")
		sampler := &fakeSampler{stack: "Call graph: nanosleep"}
		env := newEnv(sampler.options())
		managed := start(env, process.TreeKind, root)
		Eventually(func() map[string]recordstore.Row { return nodes(env, managed.Session()) }).WithTimeout(5 * time.Second).Should(HaveKey("sleep 30"))
		sleep := nodes(env, managed.Session())["sleep 30"]
		request := process.CheckpointRequest{Key: sleep["key"].(string), Mode: process.CaptureNative, Reason: reason}

		Expect(process.Checkpoint(context.Background(), env.Runtime, managed, request)).To(Succeed())
		Expect(nodes(env, managed.Session())["sleep 30"]).To(And(
			HaveKeyWithValue("stack", "Call graph: nanosleep"), HaveKeyWithValue("stackReason", reason),
			HaveKeyWithValue("captureModes", ConsistOf("native")),
		))
		Expect(sampler.calls).To(HaveLen(1))
		Expect(sampler.calls[0][0]).To(Equal("/fake/"+nativeTool()), "the first sampler of the OS")
		Expect(sampler.calls[0][1:]).To(ContainElement(strconv.FormatInt(sleep["pid"].(int64), 10)))

		sampler.stack, sampler.fail = "", errors.New("task_for_pid denied")
		Expect(process.Checkpoint(context.Background(), env.Runtime, managed, request)).To(Succeed())
		Expect(nodes(env, managed.Session())["sleep 30"]).To(And(
			HaveKeyWithValue("stackError", ContainSubstring("task_for_pid denied")), HaveKeyWithValue("stack", BeNil()),
		))
	})

	DescribeTable("refuses a checkpoint it cannot take",
		func(request process.CheckpointRequest, refusal string) {
			env := newEnv(process.TreeOptions{LookPath: func(string) (string, error) { return "", exec.ErrNotFound }})
			managed := start(env, process.TreeKind, spawnTree("sleep 30 & wait"))
			err := process.Checkpoint(context.Background(), env.Runtime, managed, request)
			Expect(err).To(MatchError(tracing.ErrCheckpointParams))
			Expect(err).To(MatchError(ContainSubstring(refusal)))
		},
		Entry("an unknown key", process.CheckpointRequest{Key: "1@1"}, `no process "1@1"`),
		Entry("a pid outside the tree", process.CheckpointRequest{PID: 1}, "no process with pid 1"),
		Entry("both a key and a pid", process.CheckpointRequest{Key: "1@1", PID: 1}, "both"),
		Entry("an unknown mode", process.CheckpointRequest{Mode: "sigquit"}, `unknown capture mode "sigquit"`),
		Entry("in-process stacks of another process", process.CheckpointRequest{Mode: process.CaptureInProcess}, "not the host process"),
		Entry("native stacks with no sampler on PATH", process.CheckpointRequest{Mode: process.CaptureNative}, nativeTool()),
		Entry("a stack and a mode", process.CheckpointRequest{Mode: process.CaptureNative, Stack: &process.Stack{Text: "x", CollectedAt: collectedAt}}, "mode"),
		Entry("a stack with no collection time", process.CheckpointRequest{Stack: &process.Stack{Text: "x"}}, "collectedAt"),
		Entry("a stack with neither text nor error", process.CheckpointRequest{Stack: &process.Stack{CollectedAt: collectedAt}}, "neither"),
	)

	It("samples a real process with the OS's native sampler", func() {
		path, err := exec.LookPath(nativeTool())
		if err != nil {
			Skip("no native stack sampler on PATH: " + nativeTool())
		}
		root := spawnTree("sleep 30")
		env := newEnv()
		managed := start(env, process.TreeKind, root)
		Expect(process.Checkpoint(context.Background(), env.Runtime, managed, process.CheckpointRequest{Mode: process.CaptureNative, Reason: "manual"})).To(Succeed())
		var rootRow recordstore.Row
		for _, row := range nodes(env, managed.Session()) {
			if row["isRoot"] == true {
				rootRow = row
			}
		}
		Expect(rootRow).To(HaveKeyWithValue("stackError", BeNil()), "sampled with %s", path)
		Expect(rootRow).To(HaveKeyWithValue("stack", ContainSubstring(strconv.Itoa(root))))
		Expect(rootRow).To(HaveKeyWithValue("captureModes", ContainElement("native")))
	})
})
