package query

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Kubernetes runtime filters", func() {
	profile := func(selector string) Profile {
		return Profile{
			Name:     "Kubernetes logs",
			Provider: ProviderConfig{Type: "k8s"},
			Query:    selector,
		}
	}

	timeBinding := ColumnFilterBinding{
		Key: "time", Field: "timestamp", Label: "Time",
		Kind: ColumnFilterKindTime, Default: KubernetesDefaultTimeRange,
	}
	workloadBinding := ColumnFilterBinding{
		Key: "workload", Field: "workload", Label: "Workload",
		Kind: ColumnFilterKindWorkload, Lookup: true,
	}
	labelsBinding := ColumnFilterBinding{
		Key: "labels", Field: "labels", Label: "Labels",
		Kind: ColumnFilterKindLabels, Lookup: true, Multi: true,
	}

	It("offers time, workload and grouped-label controls inside a broad profile scope", func() {
		bindings, err := profile("kind=Deployment namespace=payments").RuntimeFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]ColumnFilterBinding{timeBinding, workloadBinding, labelsBinding}))
	})

	// The picker still has something to offer here: its lookup lists the pods
	// the named target resolves to, and reading one of them is the narrowing an
	// exact scope leaves open.
	It("offers the same controls when the profile already names one exact target", func() {
		bindings, err := profile("kind=Deployment namespace=payments name=api").RuntimeFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]ColumnFilterBinding{timeBinding, workloadBinding, labelsBinding}))
	})

	It("resolves runtime values without adding them to profile template params", func() {
		params, filters, err := partitionProfileInput(profile("kind=Deployment"), map[string]any{
			"time":     ">=now-15m,<now",
			"workload": "payments/Deployment/api",
			"labels":   "app=api,!tier=canary",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(params).To(BeEmpty())
		Expect(filters).To(Equal([]ColumnFilterValue{
			{Key: "time", Field: "timestamp", Kind: ColumnFilterKindTime, Range: &FilterRange{
				Min: &FilterBound{Value: "now-15m", Inclusive: true},
				Max: &FilterBound{Value: "now"},
			}},
			{Key: "workload", Field: "workload", Kind: ColumnFilterKindWorkload, Include: []string{"payments/Deployment/api"}},
			{Key: "labels", Field: "labels", Kind: ColumnFilterKindLabels, Include: []string{"app=api"}, Exclude: []string{"tier=canary"}},
		}))
	})

	It("applies the time control's default when the request bounds nothing", func() {
		_, filters, err := partitionProfileInput(profile("kind=Deployment"), nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(filters).To(Equal([]ColumnFilterValue{
			{Key: "time", Field: "timestamp", Kind: ColumnFilterKindTime, Range: &FilterRange{
				Min: &FilterBound{Value: "now-1h", Inclusive: true},
			}},
		}))
	})

	It("rejects a parameter that shadows a generated control", func() {
		candidate := profile("kind=Deployment")
		candidate.Params = []ParamDef{{Name: "time", Type: ParamTypeDate}}
		Expect(candidate.Validate()).To(MatchError(ContainSubstring("conflicts with native column filter")))
	})

	// Two controls over one window is one too many, and the declared pair is the
	// one the author asked for. The workload and label controls narrow something
	// else, so neither is affected.
	It("generates no time control for a profile that declares its own bound", func() {
		candidate := profile("kind=Deployment namespace=payments")
		candidate.Params = []ParamDef{
			{Name: "Start", Type: ParamTypeDate, Role: ParamRoleTimeFrom},
			{Name: "End", Type: ParamTypeDate, Role: ParamRoleTimeTo},
		}
		bindings, err := candidate.RuntimeFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]ColumnFilterBinding{workloadBinding, labelsBinding}))
	})

	It("generates no time control for a profile that declares only a lower bound", func() {
		candidate := profile("kind=Deployment namespace=payments")
		candidate.Params = []ParamDef{{Name: "Start", Type: ParamTypeDate, Role: ParamRoleTimeFrom}}
		bindings, err := candidate.RuntimeFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]ColumnFilterBinding{workloadBinding, labelsBinding}))
	})

	// The name is only reserved while the control that owns it is generated, and
	// declaring the pair is what stops it being generated.
	It("accepts a bound parameter named after the control it replaces", func() {
		candidate := profile("kind=Deployment")
		candidate.Params = []ParamDef{{Name: "time", Type: ParamTypeDate, Role: ParamRoleTimeFrom}}
		Expect(candidate.Validate()).To(Succeed())
	})

	It("leaves a declared bound out of the runtime filter values", func() {
		candidate := profile("kind=Deployment")
		candidate.Params = []ParamDef{
			{Name: "Start", Type: ParamTypeDate, Role: ParamRoleTimeFrom},
			{Name: "End", Type: ParamTypeDate, Role: ParamRoleTimeTo},
		}
		params, filters, err := partitionProfileInput(candidate, map[string]any{"Start": "now-15m"})
		Expect(err).ToNot(HaveOccurred())
		Expect(params).To(Equal(map[string]any{"Start": "now-15m"}))
		Expect(filters).To(BeEmpty())
	})

	It("rejects malformed grouped label selections", func() {
		_, _, err := partitionProfileInput(profile("kind=Deployment"), map[string]any{
			"labels": "api",
		})
		Expect(err).To(MatchError(ContainSubstring("key=value")))
	})

	It("accepts an explicit label-key parameter", func() {
		candidate := profile("kind=Deployment")
		candidate.Params = []ParamDef{{
			Name: "applications", Type: ParamTypeLabels, Field: "labels.app",
		}}
		Expect(candidate.Validate()).To(Succeed())

		resolved, filters, err := resolveParams(candidate.Params, map[string]any{
			"applications": "api,!worker",
		}, time.Now())
		Expect(err).ToNot(HaveOccurred())
		Expect(resolved["applications"]).To(Equal([]string{"api"}))
		Expect(filters).To(Equal([]ColumnFilterValue{{
			Key: "applications", Field: "labels.app", Kind: ColumnFilterKindTerms,
			Include: []string{"api"}, Exclude: []string{"worker"},
		}}))
	})

	It("rejects label parameters outside Kubernetes and without a label field", func() {
		candidate := profile("kind=Deployment")
		candidate.Params = []ParamDef{{Name: "applications", Type: ParamTypeLabels}}
		Expect(candidate.Validate()).To(MatchError(ContainSubstring("labels.<key>")))

		candidate.Params[0].Field = "labels.app"
		candidate.Provider.Type = "opensearch"
		Expect(candidate.Validate()).To(MatchError(ContainSubstring("only supported by provider \"k8s\"")))
	})
})
