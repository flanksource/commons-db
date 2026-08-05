package profiles

import (
	"context"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type resolverStore map[string]query.Profile

func (s resolverStore) List(context.Context) ([]query.Profile, error) {
	profiles := make([]query.Profile, 0, len(s))
	for _, profile := range s {
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func (s resolverStore) Get(_ context.Context, name string) (query.Profile, error) {
	return s[name], nil
}

func (s resolverStore) Save(_ context.Context, profile query.Profile) error {
	s[profile.Name] = profile
	return nil
}

func (s resolverStore) Update(_ context.Context, original string, profile query.Profile, _ UpdateOptions) error {
	delete(s, original)
	s[profile.Name] = profile
	return nil
}

func (s resolverStore) Delete(_ context.Context, name string) error {
	delete(s, name)
	return nil
}

var _ = Describe("Resolve", func() {
	It("merges imports left to right and reports the profile that owns the connection", func() {
		store := resolverStore{
			"jaeger": {
				Name: "jaeger",
				Provider: query.ProviderConfig{Type: "opentelemetry", Options: map[string]any{
					"format": "jaeger", "params": map[string]any{"namespace": map[string]any{"field": "namespace"}},
				}},
				Params:  []query.ParamDef{{Name: "namespace", Description: "base namespace"}},
				Aliases: []query.AliasDef{{Name: "service", CEL: `span["service.name"]`}},
			},
			"jms": {
				Name:    "jms",
				Imports: []string{"jaeger"},
				Provider: query.ProviderConfig{Options: map[string]any{
					"params": map[string]any{"namespace": map[string]any{"template": "{value}-api"}},
				}},
				Params: []query.ParamDef{{Name: "namespace", Required: true}},
				Ignore: []string{"internal"},
			},
		}

		resolved, err := Resolve(context.Background(), store, "jms")
		Expect(err).ToNot(HaveOccurred())
		Expect(resolved.ConnectionProfile).To(Equal("jaeger"))
		Expect(resolved.Profile.Name).To(Equal("jms"))
		Expect(resolved.Profile.Imports).To(BeEmpty())
		Expect(resolved.Profile.Provider.Type).To(Equal("opentelemetry"))
		Expect(resolved.Profile.Provider.Options).To(HaveKey("params"))
		Expect(resolved.Profile.Params).To(Equal([]query.ParamDef{{Name: "namespace", Description: "base namespace", Required: true}}))
		Expect(resolved.Profile.Aliases).To(HaveLen(1))
		Expect(resolved.Profile.Ignore).To(Equal([]string{"internal"}))
	})

	It("rejects cycles with the complete import path", func() {
		store := resolverStore{
			"a": {Name: "a", Imports: []string{"b"}},
			"b": {Name: "b", Imports: []string{"a"}},
		}

		_, err := Resolve(context.Background(), store, "a")
		Expect(err).To(MatchError(ContainSubstring("a -> b -> a")))
	})

	It("keeps the first connection owner when a later import has no connection", func() {
		store := resolverStore{
			"owner":   {Name: "owner", Provider: query.ProviderConfig{Type: "opentelemetry", Connection: "connection://traces"}},
			"overlay": {Name: "overlay", Provider: query.ProviderConfig{Type: "opentelemetry"}},
			"profile": {Name: "profile", Imports: []string{"owner", "overlay"}},
		}

		resolved, err := Resolve(context.Background(), store, "profile")
		Expect(err).ToNot(HaveOccurred())
		Expect(resolved.ConnectionProfile).To(Equal("owner"))
	})

	// Each cap answers its own question, so an overlay that raises the export
	// ceiling says nothing about the page the base declared.
	It("layers row limits one cap at a time", func() {
		merged := mergeProfile(
			query.Profile{Limits: &query.RowLimits{PageSize: 25, MaxExportRows: 5000}},
			query.Profile{Limits: &query.RowLimits{MaxExportRows: 250_000}},
		)
		Expect(*merged.Limits).To(Equal(query.RowLimits{PageSize: 25, MaxExportRows: 250_000}))

		Expect(mergeProfile(
			query.Profile{Limits: &query.RowLimits{PageSize: 25}},
			query.Profile{},
		).Limits).To(Equal(&query.RowLimits{PageSize: 25}))
	})

	It("clears the inherited session kind when an overlay selects the other kind", func() {
		merged := mergeProfile(
			query.Profile{Trace: &query.TraceSpec{}},
			query.Profile{Top: &query.TopSpec{}},
		)
		Expect(merged.Trace).To(BeNil())
		Expect(merged.Top).ToNot(BeNil())

		merged = mergeProfile(
			query.Profile{Top: &query.TopSpec{}},
			query.Profile{Trace: &query.TraceSpec{}},
		)
		Expect(merged.Top).To(BeNil())
		Expect(merged.Trace).ToNot(BeNil())
	})
})
