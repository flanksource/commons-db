package eval_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons-db/types/eval"
)

var _ = Describe("eval", func() {
	env := map[string]any{"app": "checkout", "ns": "payments"}

	It("returns a static value without evaluating the expression", func() {
		Expect(eval.Value(env, types.ValueExpression{Value: "fixed", Expr: "not a valid cel ("})).To(Equal("fixed"))
	})

	It("evaluates a CEL expression against the environment", func() {
		Expect(eval.Value(env, types.ValueExpression{Expr: `app + "-api"`})).To(Equal("checkout-api"))
	})

	It("renders a Go template against the environment", func() {
		Expect(eval.Template(env, types.GoTemplate("{{.ns}}/{{.app}}"))).To(Equal("payments/checkout"))
	})

	It("hydrates every selector field from static values and expressions", func() {
		selector := types.EnvVarResourceSelector{
			Scope:     "scope-a",
			Cache:     "no-cache",
			Name:      types.ValueExpression{Expr: "app"},
			Namespace: types.ValueExpression{Value: "payments"},
			Types:     []types.ValueExpression{{Value: "Kubernetes::Pod"}, {Expr: `"Kubernetes::" + "Deployment"`}},
			Healths:   []types.ValueExpression{{Value: "healthy"}, {Value: "warning"}},
		}

		Expect(eval.ResourceSelector(env, selector)).To(Equal(&types.ResourceSelector{
			Scope:     "scope-a",
			Cache:     "no-cache",
			Name:      "checkout",
			Namespace: "payments",
			Types:     types.Items{"Kubernetes::Pod", "Kubernetes::Deployment"},
			Health:    types.MatchExpression("healthy,warning"),
		}))
	})

	It("names the selector field whose expression fails", func() {
		_, err := eval.ResourceSelector(env, types.EnvVarResourceSelector{
			Namespace: types.ValueExpression{Expr: "missing_variable"},
		})
		Expect(err).To(MatchError(ContainSubstring("namespace")))
	})
})
