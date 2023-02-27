package duty

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"testing"

	. "github.com/fergusstrange/embedded-postgres"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/duty/fixtures/dummy"
	"github.com/flanksource/duty/models"
	_ "github.com/flanksource/duty/types"
	ginkgo "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

func TestDuty(t *testing.T) {
	RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Duty Suite")
}

var postgresServer *EmbeddedPostgres

const pgUrl = "postgres://postgres:postgres@localhost:9876/test?sslmode=disable"

var testDB *gorm.DB

func MustDB() *sql.DB {
	db, err := NewDB(pgUrl)
	if err != nil {
		panic(err)
	}
	return db
}

var _ = ginkgo.BeforeSuite(func() {
	postgresServer = NewDatabase(DefaultConfig().
		Database("test").
		Port(9876).
		Logger(io.Discard))
	if err := postgresServer.Start(); err != nil {
		ginkgo.Fail(err.Error())
	}
	logger.Infof("Started postgres on port 9876")
	if pool != nil {
		return
	}
	if _, err := NewPgxPool(pgUrl); err != nil {
		ginkgo.Fail(err.Error())
	}
	if _, err := NewDB(pgUrl); err != nil {
		ginkgo.Fail(err.Error())
	}
	err := Migrate(pgUrl)
	Expect(err).ToNot(HaveOccurred())

	testDB, err = NewGorm(pgUrl, DefaultGormConfig())
	Expect(err).ToNot(HaveOccurred())

	populateDBWithDummyModels()
})

var _ = ginkgo.AfterSuite(func() {
	logger.Infof("Stopping postgres")
	if err := postgresServer.Stop(); err != nil {
		ginkgo.Fail(err.Error())
	}
})

func populateDBWithDummyModels() {
	var err error
	createTime := dummy.DummyCreatedAt
	for _, c := range dummy.AllDummyPeople {
		err = testDB.Create(&c).Error
		if err != nil {
			panic(err)
		}
	}
	for _, c := range dummy.AllDummyComponents {
		c.UpdatedAt = models.LocalTime(createTime)
		err = testDB.Create(&c).Error
		if err != nil {
			panic(err)
		}
	}
	for _, c := range dummy.AllDummyComponentRelationships {
		c.UpdatedAt = createTime
		err = testDB.Create(&c).Error
		if err != nil {
			panic(err)
		}
	}
	for _, c := range dummy.AllDummyConfigs {
		c.CreatedAt = createTime
		err = testDB.Create(&c).Error
		if err != nil {
			panic(err)
		}
	}
	for _, c := range dummy.AllDummyConfigAnalysis {
		c.FirstObserved = &createTime
		err = testDB.Create(&c).Error
		if err != nil {
			panic(err)
		}
	}
	for _, c := range dummy.AllDummyConfigComponentRelationships {
		err = testDB.Create(&c).Error
		if err != nil {
			panic(err)
		}
	}
	for _, c := range dummy.AllDummyIncidents {
		err = testDB.Create(&c).Error
		if err != nil {
			panic(err)
		}
	}
	for _, c := range dummy.AllDummyHypotheses {
		err = testDB.Create(&c).Error
		if err != nil {
			panic(err)
		}
	}
	for _, c := range dummy.AllDummyEvidences {
		err = testDB.Create(&c).Error
		if err != nil {
			panic(err)
		}
	}

	logger.Infof("Populated database with dummy models")
}

func readTestFile(path string) string {
	d, err := os.ReadFile(path)
	// We panic here because text fixtures should always be readable
	if err != nil {
		panic(fmt.Errorf("Unable to read file:%s due to %v", path, err))
	}
	return string(d)
}
