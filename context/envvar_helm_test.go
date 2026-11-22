package context

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons/logger"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

var _ = ginkgo.DescribeTable("decodes Helm release overrides", func(config, expected string) {
	const namespace, name = "example", "api"
	var payload bytes.Buffer
	writer := gzip.NewWriter(&payload)
	_, err := writer.Write([]byte(`{"chart":{"values":{"service":"default"}}` + config + `}`))
	Expect(err).NotTo(HaveOccurred())
	Expect(writer.Close()).To(Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{"name": name, "status": "deployed"},
		},
		Type: helmSecretType,
		Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(payload.Bytes()))},
	}
	client := dutyKubernetes.NewKubeClient(logger.GetLogger("test"), fake.NewSimpleClientset(secret), &rest.Config{Host: "https://cluster"})
	values, err := GetHelmValuesFromCache(New().WithLocalKubernetes(client), namespace, name)
	if expected == "" {
		Expect(err).To(MatchError(ContainSubstring("expected Helm config object")))
		return
	}
	Expect(err).NotTo(HaveOccurred())
	Expect(values).To(Equal(map[string]any{"service": expected}))
},
	ginkgo.Entry("omitted overrides", "", "default"),
	ginkgo.Entry("null overrides", `,"config":null`, "default"),
	ginkgo.Entry("specific overrides", `,"config":{"service":"override"}`, "override"),
	ginkgo.Entry("malformed overrides", `,"config":"invalid"`, ""),
)
