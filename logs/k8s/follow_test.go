package k8s_test

import (
	gocontext "context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/logs/k8s"
)

// tailingKubelet serves a container's log stream the way a kubelet does: what
// the container has already written, and then — under follow — the connection
// held open with each further line written as it appears.
//
// The fake clientset cannot stand in for it here: it answers a log read with one
// fixed body, so a spec about a line arriving after the read began could not
// fail against it.
type tailingKubelet struct {
	server  *httptest.Server
	history []string
	live    chan string
	queries chan url.Values
}

func newTailingKubelet(history ...string) *tailingKubelet {
	stub := &tailingKubelet{
		history: history,
		live:    make(chan string, 8),
		queries: make(chan url.Values, 4),
	}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer GinkgoRecover()
		stub.queries <- r.URL.Query()

		w.Header().Set("Content-Type", "text/plain")
		flusher, ok := w.(http.Flusher)
		Expect(ok).To(BeTrue(), "the stub cannot tail without flushing")
		for _, line := range stub.history {
			_, err := fmt.Fprintln(w, line)
			Expect(err).ToNot(HaveOccurred())
		}
		flusher.Flush()

		if r.URL.Query().Get("follow") != "true" {
			return
		}
		for {
			select {
			case line := <-stub.live:
				_, err := fmt.Fprintln(w, line)
				Expect(err).ToNot(HaveOccurred())
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}))
	return stub
}

func (s *tailingKubelet) client() kubernetes.Interface {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: s.server.URL})
	Expect(err).ToNot(HaveOccurred())
	return client
}

func (s *tailingKubelet) query() url.Values {
	GinkgoHelper()
	var values url.Values
	Eventually(s.queries).Should(Receive(&values))
	return values
}

var _ = Describe("Kubernetes log tails", func() {
	tailed := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "billing-1", Labels: map[string]string{"app": "billing"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}

	// follow starts a tail in the background, because the call blocks for as
	// long as the container keeps writing — which is the property under test.
	follow := func(stub *tailingKubelet, request k8s.Request) (chan logs.LogResult, chan error, gocontext.CancelFunc) {
		base, cancel := gocontext.WithCancel(gocontext.Background())
		results := make(chan logs.LogResult, 16)
		done := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			done <- k8s.Follow(dbcontext.NewContext(base), stub.client(),
				k8s.Target{Pod: tailed}, request, func(result logs.LogResult) { results <- result })
		}()
		return results, done, cancel
	}

	received := func(results chan logs.LogResult) logs.LogResult {
		GinkgoHelper()
		var result logs.LogResult
		Eventually(results, "5s", "10ms").Should(Receive(&result))
		Expect(result.Logs).To(HaveLen(1), "a tail delivers one line at a time")
		return result
	}

	It("delivers each line as the container writes it rather than at the end of the stream", func() {
		stub := newTailingKubelet()
		DeferCleanup(stub.server.Close)

		results, done, cancel := follow(stub, k8s.Request{})
		DeferCleanup(cancel)
		Expect(stub.query().Get("follow")).To(Equal("true"))

		stub.live <- "2026-04-19T11:23:40.207Z settlement gateway rejected batch 88213"
		first := received(results)
		Expect(first.Logs[0].Message).To(Equal("settlement gateway rejected batch 88213"))
		Expect(first.Logs[0].ID).To(Equal("prod/billing-1/app#0"))
		Expect(first.Metadata).To(Equal(map[string]any{
			"pod": "billing-1", "namespace": "prod", "container": "app",
		}))

		// A collected read would have returned by now; a tail has not, which is
		// the whole difference between them.
		Consistently(done, "200ms").ShouldNot(Receive())

		stub.live <- "2026-04-19T11:23:41.310Z retry scheduled"
		Expect(received(results).Logs[0].Message).To(Equal("retry scheduled"))
	})

	// The ids a tail stamps are the ids a page stamped, so the position a page
	// handed back names a line the tail can find and skip past.
	It("repeats nothing the page it resumes from already served", func() {
		stub := newTailingKubelet(
			"2026-04-19T11:23:40.207Z settlement gateway rejected batch 88213",
			"2026-04-19T11:23:40.207Z settlement retried",
			"2026-04-19T11:23:41.310Z retry scheduled",
		)
		DeferCleanup(stub.server.Close)

		served, err := time.Parse(time.RFC3339, "2026-04-19T11:23:40.207Z")
		Expect(err).ToNot(HaveOccurred())
		results, _, cancel := follow(stub, k8s.Request{
			After: &k8s.Position{Timestamp: served, ID: "prod/billing-1/app#0"},
		})
		DeferCleanup(cancel)

		Expect(received(results).Logs[0].ID).To(Equal("prod/billing-1/app#1"))
		Expect(received(results).Logs[0].Message).To(Equal("retry scheduled"))
	})

	// The collected read is the same walk with a different consumer, so it must
	// still ask for the whole body at once and still end on its own.
	It("leaves a collected read unfollowed", func() {
		stub := newTailingKubelet(
			"2026-04-19T11:23:40.207Z settlement gateway rejected batch 88213",
			"2026-04-19T11:23:41.310Z retry scheduled",
		)
		DeferCleanup(stub.server.Close)

		results, err := k8s.Fetch(dbcontext.New(), stub.client(), []corev1.Pod{tailed}, k8s.Request{})
		Expect(err).ToNot(HaveOccurred())
		Expect(results).To(HaveLen(1))
		Expect(results[0].Logs).To(HaveLen(2))
		Expect(results[0].Metadata).To(HaveKeyWithValue("container", "app"))
		Expect(stub.query().Get("follow")).To(BeEmpty())
	})
})
