package db

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server uniqueidentifier normalization", func() {
	It("prints the driver's raw GUID bytes in canonical form", func() {
		raw := []byte{
			0x67, 0x45, 0x23, 0x01,
			0xAB, 0x89,
			0xEF, 0xCD,
			0x01, 0x23,
			0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF,
		}

		value, err := normalizeSQLValue("UNIQUEIDENTIFIER", raw)

		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal("01234567-89AB-CDEF-0123-456789ABCDEF"))
	})

	It("rejects malformed GUID bytes", func() {
		value, err := normalizeSQLValue("UNIQUEIDENTIFIER", []byte{0x01, 0x02})

		Expect(err).To(MatchError("mssql: invalid UniqueIdentifier length"))
		Expect(value).To(BeNil())
	})

	It("preserves a null GUID", func() {
		value, err := normalizeSQLValue("UNIQUEIDENTIFIER", nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(BeNil())
	})
})
