package query

import "time"

type QueryNotice struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}
type MetricSource struct {
	Statistics []string  `json:"statistics,omitempty"`
	Expression string    `json:"expression"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Step       string    `json:"step"`
	Samples    int       `json:"samples"`
}
type MetricMetadata struct {
	Sources []MetricSource `json:"sources,omitempty"`
	Notices []QueryNotice  `json:"notices,omitempty"`
}
