package k8s_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/logs/k8s"
	"github.com/flanksource/commons-db/types"
)

func TestK8sLogs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Kubernetes Logs Suite")
}

var _ = Describe("Kubernetes log reads", func() {
	pod := func(namespace, name, app string) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace, Name: name, UID: k8stypes.UID(namespace + "/" + name),
				Labels: map[string]string{"app": app},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		}
	}

	pods := []corev1.Pod{
		pod("prod", "billing-1", "billing"),
		pod("prod", "billing-2", "billing"),
		pod("staging", "billing-1", "billing"),
	}

	DescribeTable("SelectPods narrows already-resolved pods",
		func(selectors types.ResourceSelectors, expected []string) {
			names := make([]string, 0, len(pods))
			for _, selected := range k8s.SelectPods(pods, selectors) {
				names = append(names, selected.Namespace+"/"+selected.Name)
			}
			Expect(names).To(Equal(expected))
		},
		Entry("an empty selection narrows nothing", types.ResourceSelectors(nil),
			[]string{"prod/billing-1", "prod/billing-2", "staging/billing-1"}),
		Entry("a namespaced name picks exactly one pod",
			types.ResourceSelectors{{Namespace: "prod", Name: "billing-1"}},
			[]string{"prod/billing-1"}),
		// A bare name is ambiguous across namespaces, which is why the runtime
		// control always carries the namespace it picked in.
		Entry("a bare name matches that name in every namespace",
			types.ResourceSelectors{{Name: "billing-1"}},
			[]string{"prod/billing-1", "staging/billing-1"}),
		Entry("a selection matching nothing yields nothing",
			types.ResourceSelectors{{Namespace: "prod", Name: "absent"}},
			[]string{}),
	)

	// The bound used to be dropped whenever it failed to parse, which turned a
	// narrowing request into the widest read available.
	DescribeTable("a bound nothing can resolve fails the read",
		func(request k8s.Request, message string) {
			client := fake.NewSimpleClientset()
			_, err := k8s.Fetch(dbcontext.New(), client, pods[:1], request)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("an unparseable start", k8s.Request{
			LogsRequestBase: logs.LogsRequestBase{Start: "yesterday"},
		}, `start "yesterday" is not a time or date math`),
		Entry("an unparseable end", k8s.Request{
			LogsRequestBase: logs.LogsRequestBase{Start: "now-1h", End: "whenever"},
		}, `end "whenever" is not a time or date math`),
	)

	// A profile parameter of type datetime resolves to RFC3339Nano before the
	// read ever sees it, and go-datemath's fraction stops at milliseconds — so
	// reading the bound as date math alone rejected the instants this package is
	// handed, and a window the operator picked came back as a 400.
	DescribeTable("a resolved instant is a bound the read accepts",
		func(bound string) {
			client := fake.NewSimpleClientset()
			_, err := k8s.Fetch(dbcontext.New(), client, pods[:1], k8s.Request{
				LogsRequestBase: logs.LogsRequestBase{Start: bound, End: bound},
			})
			Expect(err).ToNot(HaveOccurred())
		},
		Entry("microsecond precision", "2026-08-18T04:00:18.185744Z"),
		Entry("nanosecond precision", "2026-08-18T04:00:18.185744321Z"),
		Entry("whole seconds", "2026-08-18T04:00:18Z"),
		Entry("date math", "now-30m"),
	)
})
