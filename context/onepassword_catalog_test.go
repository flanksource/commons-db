package context

import (
	"encoding/json"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("1Password metadata catalog", func() {
	const (
		vaultID        = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
		itemID         = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
		untitledItemID = "cccccccccccccccccccccccccc"
	)

	ginkgo.BeforeEach(func() {
		onePasswordCommandFunc = func(_ Context, _ string, args ...string) ([]byte, error) {
			switch {
			case len(args) == 3 && args[0] == "vault":
				return []byte(`[{"id":"` + vaultID + `","name":"Production"}]`), nil
			case len(args) == 5 && args[0] == "item" && args[1] == "list":
				return []byte(`[{"id":"` + untitledItemID + `","title":""},{"id":"` + itemID + `","title":"Database"}]`), nil
			case len(args) == 6 && args[0] == "item" && args[1] == "get":
				return []byte(`{"fields":[{"id":"notesPlain","label":"notesPlain","reference":"","value":"must-not-escape"},{"id":"password","label":"Password","reference":"op://Production/Database/password","value":"must-not-escape","section":{"label":"Credentials"}}]}`), nil
			default:
				ginkgo.Fail("unexpected op command: " + args[0])
				return nil, nil
			}
		}
	})

	ginkgo.AfterEach(func() {
		onePasswordCommandFunc = runOnePasswordCommand
	})

	ginkgo.It("omits untitled items while listing sorted metadata", func() {
		vaults, err := ListOnePasswordVaults(newOnePasswordTestContext())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(vaults).To(gomega.Equal([]OnePasswordVault{{ID: vaultID, Name: "Production"}}))

		items, err := ListOnePasswordItems(newOnePasswordTestContext(), vaultID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(items).To(gomega.Equal([]OnePasswordItem{{ID: itemID, Name: "Database"}}))
	})

	ginkgo.It("returns field metadata without exposing field values", func() {
		fields, err := ListOnePasswordFields(newOnePasswordTestContext(), vaultID, itemID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fields).To(gomega.Equal([]OnePasswordField{{
			ID: "password", Label: "Password",
			Reference: "op://Production/Database/password", Section: "Credentials",
		}}))

		payload, err := json.Marshal(fields)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(payload)).NotTo(gomega.ContainSubstring("must-not-escape"))
	})

	ginkgo.It("rejects option-like resource IDs before invoking the CLI", func() {
		called := false
		onePasswordCommandFunc = func(_ Context, _ string, _ ...string) ([]byte, error) {
			called = true
			return nil, nil
		}

		_, err := ListOnePasswordItems(newOnePasswordTestContext(), "--help")
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("invalid 1password vault ID")))
		gomega.Expect(called).To(gomega.BeFalse())
	})
})
