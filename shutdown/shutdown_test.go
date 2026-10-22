package shutdown

import "testing"

type shutdownOrder struct {
	t          *testing.T
	lastClosed int
}

func (order *shutdownOrder) add(label string, priority int) {
	AddHookWithPriority(label, priority, func() {
		if order.lastClosed > priority {
			order.t.Fatalf("something higher priority (%d) was closed earlier than (%d)", order.lastClosed, priority)
		} else {
			order.lastClosed = priority
		}
	})
}

func TestShutdownPriority(t *testing.T) {
	order := shutdownOrder{t: t}
	order.add("database", PriorityCritical)
	order.add("gRPC", PriorityIngress)
	order.add("checkJob", PriorityJobs)
	order.add("echo", PriorityIngress)
	order.add("topologyJob", PriorityJobs)

	Shutdown()
}
