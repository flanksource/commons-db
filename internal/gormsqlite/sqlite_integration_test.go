package sqlite

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

type sqliteNote struct {
	ID       uint   `gorm:"primaryKey"`
	Body     string `gorm:"uniqueIndex"`
	Obsolete string
}

func (sqliteNote) TableName() string { return "notes" }

var _ = Describe("GORM modernc integration", func() {
	var database *gorm.DB
	BeforeEach(func() {
		var err error
		database, err = gorm.Open(Open(filepath.Join(GinkgoT().TempDir(), "notes.db")), &gorm.Config{TranslateError: true})
		Expect(err).NotTo(HaveOccurred())
		connection, err := database.DB()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(connection.Close)
		Expect(database.AutoMigrate(&sqliteNote{})).To(Succeed())
	})

	It("retains rows through schema changes and translates unique constraint errors", func() {
		Expect(database.Create(&sqliteNote{Body: "acme", Obsolete: "remove"}).Error).To(Succeed())
		Expect(database.Create(&sqliteNote{Body: "acme"}).Error).To(MatchError(gorm.ErrDuplicatedKey))
		Expect(database.Migrator().DropColumn(&sqliteNote{}, "Obsolete")).To(Succeed())
		var body string
		Expect(database.Raw("SELECT body FROM notes").Scan(&body).Error).To(Succeed())
		Expect(body).To(Equal("acme"))
		Expect(database.Migrator().HasColumn(&sqliteNote{}, "Obsolete")).To(BeFalse())
		Expect(database.Migrator().DropTable(&sqliteNote{})).To(Succeed())
	})

	It("surfaces savepoint and rollback errors", func() {
		connection, err := database.DB()
		Expect(err).NotTo(HaveOccurred())
		Expect(connection.Close()).To(Succeed())
		dialector := database.Dialector.(gorm.SavePointerDialectorInterface)
		Expect(dialector.SavePoint(database, "checkpoint")).To(MatchError("sql: database is closed"))
		Expect(dialector.RollbackTo(database, "checkpoint")).To(MatchError("sql: database is closed"))
	})

	It("translates foreign key violations using the modernc error code", func() {
		Expect(database.Exec("PRAGMA foreign_keys = ON").Error).To(Succeed())
		Expect(database.Exec("CREATE TABLE children (note_id integer REFERENCES notes(id))").Error).To(Succeed())
		Expect(database.Exec("INSERT INTO children (note_id) VALUES (1)").Error).To(MatchError(gorm.ErrForeignKeyViolated))
	})
})
