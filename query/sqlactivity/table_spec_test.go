package sqlactivity

import (
	"encoding/json"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL activity measurement availability", func() {
	It("exports unsupported measurements as null and retains measured zero", func() {
		data, err := json.Marshal(Sample{Engine: EnginePostgres})
		Expect(err).NotTo(HaveOccurred())
		var raw map[string]any
		Expect(json.Unmarshal(data, &raw)).To(Succeed())
		for _, key := range []string{"cpuMs", "waitMs", "logicalReads", "reads", "writes", "memoryBytes"} {
			Expect(raw).To(HaveKeyWithValue(key, BeNil()))
		}
		var measured Sample
		Expect(json.Unmarshal([]byte(`{"engine":"sqlserver","cpuMs":0,"memoryBytes":0}`), &measured)).To(Succeed())
		data, err = json.Marshal(measured)
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(data, &raw)).To(Succeed())
		Expect(raw).To(HaveKeyWithValue("cpuMs", float64(0)))
		Expect(raw).To(HaveKeyWithValue("memoryBytes", float64(0)))
	})
})
