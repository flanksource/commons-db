// The HTTP traffic tap: a collector observing a feature receives, as a HAR
// entry, every exchange the transports of that feature make while it observes.

package connection

import (
	netHTTP "net/http"
	"slices"
	"strings"
	"sync"

	"github.com/flanksource/commons/har"
	"github.com/flanksource/commons/http/middlewares"
)

type httpObservers struct {
	mu         sync.RWMutex
	next       uint64
	collectors map[string]map[uint64]*har.Collector
}

var httpTraffic = &httpObservers{collectors: map[string]map[uint64]*har.Collector{}}

// ObserveHTTP hands collector every exchange that ApplyHTTPObservability and
// ApplyHTTPClientObservability transports of feature make, whatever the
// feature's HAR level, until release is called. Feature names match as the
// HAR level's do: case-insensitively, with an empty name meaning "http".
func ObserveHTTP(feature string, collector *har.Collector) (release func()) {
	feature = normalizeFeature(feature)
	httpTraffic.mu.Lock()
	httpTraffic.next++
	id := httpTraffic.next
	if httpTraffic.collectors[feature] == nil {
		httpTraffic.collectors[feature] = map[uint64]*har.Collector{}
	}
	httpTraffic.collectors[feature][id] = collector
	httpTraffic.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			httpTraffic.mu.Lock()
			defer httpTraffic.mu.Unlock()
			delete(httpTraffic.collectors[feature], id)
			if len(httpTraffic.collectors[feature]) == 0 {
				delete(httpTraffic.collectors, feature)
			}
		})
	}
}

// observing returns the collectors observing feature, oldest first.
func (o *httpObservers) observing(feature string) []*har.Collector {
	o.mu.RLock()
	defer o.mu.RUnlock()
	byID := o.collectors[feature]
	if len(byID) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	collectors := make([]*har.Collector, 0, len(ids))
	slices.Sort(ids)
	for _, id := range ids {
		collectors = append(collectors, byID[id])
	}
	return collectors
}

// trafficMiddleware captures an exchange only while a collector observes
// feature, deciding per request so a transport built before a collector
// arrived is observed too. The exchange is captured once, as the oldest
// observer's config asks, and added to every observer.
func trafficMiddleware(feature string) middlewares.Middleware {
	feature = normalizeFeature(feature)
	return func(next netHTTP.RoundTripper) netHTTP.RoundTripper {
		return middlewares.RoundTripperFunc(func(req *netHTTP.Request) (*netHTTP.Response, error) {
			collectors := httpTraffic.observing(feature)
			if len(collectors) == 0 {
				return next.RoundTrip(req)
			}
			return har.NewMiddleware(collectors[0].Config, func(entry *har.Entry) {
				for _, collector := range collectors {
					collector.Add(entry)
				}
			})(next).RoundTrip(req)
		})
	}
}

func normalizeFeature(feature string) string {
	feature = strings.TrimSpace(strings.ToLower(feature))
	if feature == "" {
		return "http"
	}
	return feature
}
