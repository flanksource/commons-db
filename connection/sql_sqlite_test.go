package connection

import (
	"database/sql"
	"path/filepath"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQLite SQL connections", func() {
	It("opens a snapshot read-only through the shared SQLite driver", func() {
		path := filepath.Join(GinkgoT().TempDir(), "snapshot.sqlite")
		writer, err := sql.Open("sqlite", path)
		Expect(err).NotTo(HaveOccurred())
		_, err = writer.Exec(`CREATE TABLE reconcile_rows (row_id INTEGER PRIMARY KEY, outcome TEXT)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = writer.Exec(`INSERT INTO reconcile_rows VALUES (1, 'matched')`)
		Expect(err).NotTo(HaveOccurred())
		Expect(writer.Close()).To(Succeed())

		var connection SQLConnection
		Expect(connection.FromModel(models.Connection{
			Name: "snapshot", Type: models.ConnectionTypeSQLite, URL: "file:" + path + "?mode=ro",
		})).To(Succeed())
		client, err := connection.Client(dbcontext.New())
		Expect(err).NotTo(HaveOccurred())
		defer client.Close()

		var outcome string
		Expect(client.QueryRow(`SELECT outcome FROM reconcile_rows WHERE row_id = 1`).Scan(&outcome)).To(Succeed())
		Expect(outcome).To(Equal("matched"))
		_, err = client.Exec(`DELETE FROM reconcile_rows`)
		Expect(err).To(HaveOccurred())
	})
})
