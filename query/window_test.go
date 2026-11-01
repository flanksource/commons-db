package query_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

var _ = Describe("WindowProfile", func() {
	start := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	window := query.ReadWindow{Start: start, End: start.Add(time.Hour)}

	It("replaces a provider range with the window and keeps its declared step", func() {
		profile := query.Profile{Name: "cpu", Provider: query.ProviderConfig{Type: "prometheus", Options: map[string]any{
			"range": map[string]any{"start": "now-7d", "end": "now", "step": "30s"},
		}}}
		windowed, params, err := query.WindowProfile(profile, nil, window)
		Expect(err).ToNot(HaveOccurred())
		Expect(windowed.Provider.Options["range"]).To(Equal(map[string]any{
			"start": "2026-10-05T09:00:00.000Z", "end": "2026-10-05T10:00:00.000Z", "step": "30s",
		}))
		Expect(params).To(BeEmpty())
		Expect(profile.Provider.Options["range"]).To(HaveKeyWithValue("start", "now-7d"), "the caller's profile is untouched")
	})

	It("takes the window's step over the range's", func() {
		profile := query.Profile{Provider: query.ProviderConfig{Type: "prometheus", Options: map[string]any{"range": map[string]any{}}}}
		windowed, _, err := query.WindowProfile(profile, nil, query.ReadWindow{Start: start, End: start.Add(time.Minute), Step: 15 * time.Second})
		Expect(err).ToNot(HaveOccurred())
		Expect(windowed.Provider.Options["range"]).To(HaveKeyWithValue("step", "15s"))
	})

	It("refuses a range that names no step when the window has none", func() {
		profile := query.Profile{Name: "cpu", Provider: query.ProviderConfig{Type: "prometheus", Options: map[string]any{"range": map[string]any{}}}}
		_, _, err := query.WindowProfile(profile, nil, window)
		Expect(err).To(MatchError(ContainSubstring(`profile "cpu" range declares no step`)))
	})

	It("binds the window onto the params the read references, declaring an undeclared params.from", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "sqlite"},
			Query:    "SELECT * FROM t WHERE at >= {{.params.from}}",
			Params:   []query.ParamDef{{Name: "until", Role: query.ParamRoleTimeTo}},
		}
		windowed, params, err := query.WindowProfile(profile, map[string]any{"pod": "a"}, window)
		Expect(err).ToNot(HaveOccurred())
		Expect(params).To(Equal(map[string]any{
			"pod": "a", "from": "2026-10-05T09:00:00.000Z", "until": "2026-10-05T10:00:00.000Z",
		}))
		Expect(windowed.Params).To(Equal([]query.ParamDef{{Name: "until", Role: query.ParamRoleTimeTo}, {Name: "from"}}))
		Expect(profile.Params).To(HaveLen(1), "the caller's profile is untouched")
	})

	It("refuses a profile without a range whose read names neither bound", func() {
		profile := query.Profile{Name: "all", Provider: query.ProviderConfig{Type: "sqlite"}, Query: "SELECT * FROM t"}
		_, _, err := query.WindowProfile(profile, nil, window)
		Expect(err).To(MatchError(query.ErrUnwindowed))
		Expect(err).To(MatchError(ContainSubstring(`profile "all"`)))
	})
})
