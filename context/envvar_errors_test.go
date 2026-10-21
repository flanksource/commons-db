package context_test

import (
	dbcontext "github.com/flanksource/commons-db/context"
	dbkubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

var _ = Describe("Kubernetes value lookup errors", func() {
	DescribeTable("preserves the original Kubernetes status",
		func(resource string, cause error) {
			client := fake.NewSimpleClientset()
			client.PrependReactor("get", resource, func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, cause
			})
			ctx := dbcontext.New().WithLocalKubernetes(dbkubernetes.NewKubeClient(logger.GetLogger("test"), client, &rest.Config{Host: "https://example.test"}))
			lookup := dbcontext.GetSecretFromCache
			if resource == "configmaps" {
				lookup = dbcontext.GetConfigMapFromCache
			}
			_, err := lookup(ctx, "workloads", "db", "host")
			Expect(err).To(MatchError(ContainSubstring("workloads/db")))
			Expect(apierrors.ReasonForError(err)).To(Equal(apierrors.ReasonForError(cause)))
		},
		Entry("secret not found", "secrets", apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "db")),
		Entry("secret unauthorized", "secrets", apierrors.NewUnauthorized("Unauthorized")),
		Entry("configmap not found", "configmaps", apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "db")),
		Entry("configmap unauthorized", "configmaps", apierrors.NewUnauthorized("Unauthorized")),
	)
})
