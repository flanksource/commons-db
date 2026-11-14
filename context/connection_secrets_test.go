package context_test

import (
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	dbcontext "github.com/flanksource/commons-db/context"
	dbkubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

var _ = Describe("HydrateConnection named secrets", func() {
	const (
		namespace  = "deployers"
		signingKey = "jwt-signing-key-value"
	)
	secretRef := func(name, key string) types.EnvVar {
		return types.EnvVar{ValueFrom: &types.EnvVarSource{SecretKeyRef: &types.SecretKeySelector{
			LocalObjectReference: types.LocalObjectReference{Name: name}, Key: key,
		}}}
	}
	newContext := func() dbcontext.Context {
		client := fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "tomcat-jwt", Namespace: namespace},
			Data:       map[string][]byte{"key": []byte(signingKey)},
		})
		return dbcontext.New().WithLocalKubernetes(dbkubernetes.NewKubeClient(logger.GetLogger("test"), client, &rest.Config{Host: "https://example.test"}))
	}

	It("resolves every secret's valueFrom in place beside the password", func() {
		connection := &models.Connection{Name: "tomcat", Namespace: namespace, Type: models.ConnectionTypeHTTP, Password: "manager-password",
			Secrets: types.EnvVarMap{"jwt": secretRef("tomcat-jwt", "key"), "manager": {ValueStatic: "manager-login"}}}
		hydrated, err := dbcontext.HydrateConnection(newContext(), connection)
		Expect(err).NotTo(HaveOccurred())
		Expect(hydrated.Secrets).To(Equal(types.EnvVarMap{"jwt": {ValueStatic: signingKey}, "manager": {ValueStatic: "manager-login"}}))
	})

	It("fails naming the connection and the secret when the referenced key is missing", func() {
		connection := &models.Connection{Name: "tomcat", Namespace: namespace, Type: models.ConnectionTypeHTTP,
			Secrets: types.EnvVarMap{"jwt": secretRef("tomcat-jwt", "absent")}}
		_, err := dbcontext.HydrateConnection(newContext(), connection)
		Expect(err).To(MatchError(And(ContainSubstring(`connection "tomcat"`), ContainSubstring(`secret "jwt"`), ContainSubstring("absent"))))
	})
})
