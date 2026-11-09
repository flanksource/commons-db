package recordresults_test

import (
	"context"
	"errors"
	"time"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type observedSource struct {
	recordstore.Backend
	started         chan struct{}
	stopped         chan struct{}
	closed          chan struct{}
	watchingAtClose bool
}

func (s *observedSource) WatchChanges(ctx context.Context, _ func()) error {
	close(s.started)
	defer close(s.stopped)
	select {
	case <-ctx.Done():
	case <-s.closed:
	}
	return nil
}

func (s *observedSource) Close() error {
	select {
	case <-s.stopped:
	default:
		s.watchingAtClose = true
	}
	close(s.closed)
	return s.Backend.Close()
}

var _ = Describe("Results watcher lifetime", func() {
	DescribeTable("stops the watcher before releasing its source",
		func(failRegistration bool) {
			source := &observedSource{Backend: recordresultstest.KVRouter(recordstore.NewSchemas()), started: make(chan struct{}), stopped: make(chan struct{}), closed: make(chan struct{})}
			options := recordresults.OpenOptions{
				Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(""), Source: source,
				Register: func(registry *recordresults.Registry) error {
					Eventually(source.started, time.Second).Should(BeClosed())
					if failRegistration {
						return errors.New("registration failed")
					}
					return recordresultstest.RegisterSampleEvents(registry)
				},
			}
			results, err := recordresults.Open(options)
			if failRegistration {
				Expect(err).To(MatchError(ContainSubstring("registration failed")))
			} else {
				Expect(err).NotTo(HaveOccurred())
				Expect(results.Close()).To(Succeed())
			}
			Eventually(source.stopped).Should(BeClosed())
			Expect(source.watchingAtClose).To(BeFalse())
		},
		Entry("successful open", false),
		Entry("failed open", true),
	)
})
