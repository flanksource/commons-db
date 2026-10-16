package process_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

const interval = `"250ms"`

// spawnTree starts a shell running a short and a long sleep, in its own
// process group, which the spec kills when it ends.
func spawnTree(script string) int {
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	Expect(cmd.Start()).To(Succeed())
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	DeferCleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	return pid
}

func newEnv(options ...process.TreeOptions) tracingtest.Env {
	sampler := process.NewSampler()
	var treeOptions process.TreeOptions
	if len(options) > 0 {
		treeOptions = options[0]
	}
	return tracingtest.NewEnv(map[string]tracing.TracePlugin{
		process.TreeKind:    process.NewTreeKind(sampler, treeOptions),
		process.SamplesKind: process.NewSamplesKind(sampler),
	})
}

func start(env tracingtest.Env, kind string, rootPID int) *query.ManagedSession {
	managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{
		Kind: kind, Params: json.RawMessage(`{"rootPid": ` + strconv.Itoa(rootPID) + `, "interval": ` + interval + `}`),
	})
	Expect(err).ToNot(HaveOccurred())
	tracingtest.Running(managed.Session())
	return managed
}

// nodes are the stored tree rows of a running session, by command.
func nodes(env tracingtest.Env, session *query.Session) map[string]recordstore.Row {
	info := session.Snapshot()
	Expect(info.Events).ToNot(BeNil())
	byCommand := map[string]recordstore.Row{}
	Expect(env.Results.Backend.Scan(context.Background(), info.Events.Stream, 0, func(_ int64, row recordstore.Row) error {
		byCommand[row["command"].(string)] = row
		return nil
	})).To(Succeed())
	return byCommand
}

var _ = Describe("process_tree", func() {
	It("stores each process under the root once, keeps one that exited, and ends when the root exits", func() {
		root := spawnTree("sleep 1 & sleep 30 & wait")
		env := newEnv()
		session := start(env, process.TreeKind, root).Session()

		Eventually(func() map[string]recordstore.Row { return nodes(env, session) }).WithTimeout(5 * time.Second).Should(And(
			HaveKeyWithValue("sleep 1", HaveKeyWithValue("exitedAt", Not(BeNil()))),
			HaveKeyWithValue("sleep 30", HaveKeyWithValue("exitedAt", BeNil())),
		))
		stored := nodes(env, session)
		var rootKey string
		for _, row := range stored {
			if row["pid"] == int64(root) {
				rootKey = row["key"].(string)
				Expect(row["isRoot"]).To(BeTrue())
			}
		}
		Expect(rootKey).To(MatchRegexp(`^%d@\d{10}$`, root), "keyed by pid and start second")
		Expect(stored["sleep 30"]).To(HaveKeyWithValue("parentKey", rootKey))
		Expect(stored["sleep 30"]).To(HaveKeyWithValue("name", "sleep"))
		Expect(stored["sleep 1"]).To(HaveKeyWithValue("status", "exited"))

		Expect(syscall.Kill(-root, syscall.SIGKILL)).To(Succeed())
		info := tracingtest.Ended(session)
		Expect(info.Error).To(BeEmpty(), "a root that exits ends its capture cleanly")
		for command, row := range nodes(env, session) {
			Expect(row).To(HaveKeyWithValue("exitedAt", Not(BeNil())), "%s exited with its root", command)
		}
	})

	It("summarizes the tree as the processes still live and exited, and their peaks", func() {
		root := spawnTree("sleep 1 & sleep 30 & wait")
		env := newEnv()
		managed := start(env, process.TreeKind, root)
		Eventually(func() map[string]recordstore.Row { return nodes(env, managed.Session()) }).WithTimeout(5 * time.Second).
			Should(HaveKeyWithValue("sleep 1", HaveKeyWithValue("exitedAt", Not(BeNil()))))
		stream := managed.Session().Snapshot().Events.Stream
		summarize := func() (map[string]any, error) {
			return env.Runtime.Summarize(context.Background(), tracing.TraceRef{Kind: process.TreeKind, Stream: stream})
		}
		Eventually(summarize).WithTimeout(5*time.Second).Should(MatchAllKeys(Keys{
			"count": Equal(3), "live": Equal(2), "exited": Equal(1),
			"peak_cpu_percent": BeNumerically(">=", 0), "peak_rss_bytes": BeNumerically(">", 0),
		}), "the CPU peak appears once a process was rated over a full second")
	})

	It("dumps the host's goroutines onto its root node at a checkpoint", func() {
		env := newEnv()
		managed := start(env, process.TreeKind, os.Getpid())
		Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())
		var root recordstore.Row
		for _, row := range nodes(env, managed.Session()) {
			if row["isRoot"] == true {
				root = row
			}
		}
		Expect(root).To(HaveKeyWithValue("stack", ContainSubstring("goroutine ")))
		Expect(root).To(HaveKeyWithValue("stackCollectedAt", Not(BeNil())))
		Expect(root).To(HaveKeyWithValue("captureModes", ContainElement("inprocess")))
	})

	It("records why another process's stacks cannot be dumped without a native sampler", func() {
		root := spawnTree("sleep 30")
		env := newEnv(process.TreeOptions{LookPath: func(string) (string, error) { return "", exec.ErrNotFound }})
		managed := start(env, process.TreeKind, root)
		Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())
		// Whether sh execs its only command (bash) or forks it (dash), the
		// root is the process at root's pid.
		var rootRow recordstore.Row
		for _, row := range nodes(env, managed.Session()) {
			if row["isRoot"] == true {
				rootRow = row
			}
		}
		Expect(rootRow).To(HaveKeyWithValue("pid", int64(root)))
		Expect(rootRow).To(HaveKeyWithValue("stackError", And(ContainSubstring("not the host process"), ContainSubstring(nativeTool()))))
		Expect(rootRow).To(HaveKeyWithValue("stack", BeNil()))
		Expect(rootRow).To(HaveKeyWithValue("captureModes", BeNil()))
	})

	DescribeTable("refuses params it cannot capture under before a session exists",
		func(params string) {
			_, err := newEnv().Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: process.TreeKind, Params: json.RawMessage(params)})
			Expect(err).To(MatchError(tracing.ErrInvalidParams))
		},
		Entry("a negative root", `{"rootPid": -1}`),
		Entry("an interval below the minimum", `{"interval": "10ms"}`),
		Entry("an interval that is not a duration", `{"interval": "soon"}`),
	)

	It("refuses a root that is not running", func() {
		root := spawnTree("exit 0")
		Eventually(func() error { return syscall.Kill(root, 0) }).WithTimeout(5 * time.Second).Should(HaveOccurred())
		_, err := newEnv().Runtime.Start(dbcontext.New(), tracing.StartRequest{
			Kind: process.TreeKind, Params: json.RawMessage(`{"rootPid": ` + strconv.Itoa(root) + `}`),
		})
		Expect(err).To(MatchError(ContainSubstring("is not running")))
	})
})

var _ = Describe("process_samples", func() {
	It("samples every live process of the tree at each tick, keyed as its tree node", func() {
		root := spawnTree("sleep 30 & wait")
		env := newEnv()
		tree := start(env, process.TreeKind, root).Session()
		samples := start(env, process.SamplesKind, root).Session()
		Eventually(func() int64 { return samples.Snapshot().EventCount }).WithTimeout(5 * time.Second).Should(BeNumerically(">=", 6))
		samples.Stop("spec")
		ticks := map[string]map[int64]bool{}
		for _, row := range env.Rows(tracingtest.Ended(samples)) {
			key := row["key"].(string)
			if ticks[key] == nil {
				ticks[key] = map[int64]bool{}
			}
			ticks[key][row["tick"].(int64)] = true
		}
		treeKeys := map[string]bool{}
		for _, row := range nodes(env, tree) {
			treeKeys[row["key"].(string)] = true
		}
		Expect(ticks).To(HaveLen(2), "the shell and its sleep")
		for key, seen := range ticks {
			Expect(treeKeys).To(HaveKey(key))
			Expect(len(seen)).To(BeNumerically(">=", 2), "%s sampled at two ticks or more", key)
		}
	})

	It("rates a busy process's CPU once a second has passed since its first observation, never as a fake 0", func() {
		const busy = 50.0
		root := spawnTree("while :; do :; done")
		env := newEnv()
		tree := start(env, process.TreeKind, root).Session()
		samples := start(env, process.SamplesKind, root).Session()
		rootNode := func() recordstore.Row {
			for _, row := range nodes(env, tree) {
				if row["isRoot"] == true {
					return row
				}
			}
			return nil
		}
		Eventually(rootNode).WithTimeout(10 * time.Second).Should(And(
			HaveKeyWithValue("cpuPercent", BeNumerically(">", busy)),
			HaveKeyWithValue("peakCpuPercent", BeNumerically(">", busy)),
		))
		samples.Stop("spec")
		byTick := map[int64]any{}
		for _, row := range env.Rows(tracingtest.Ended(samples)) {
			byTick[row["tick"].(int64)] = row["cpuPercent"]
		}
		Expect(byTick).To(HaveKeyWithValue(int64(1), BeNil()), "unknown at the first observation")
		Expect(byTick).To(ContainElement(BeNumerically(">", busy)))
	})
})
