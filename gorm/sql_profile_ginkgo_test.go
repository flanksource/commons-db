package gorm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	commons "github.com/flanksource/commons/logger"
	"github.com/glebarez/sqlite"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
	_ "modernc.org/sqlite"
)

func TestSQLProfile(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL profile")
}

var _ = Describe("SQL profile export", func() {
	It("activates from SQL_PROFILE_FILE when a GORM logger is constructed", func() {
		path := filepath.Join(GinkgoT().TempDir(), "sqlprofile.jsonl")
		GinkgoT().Setenv(SQLProfileFileEnv, path)
		log := NewSqlLogger(commons.GetLogger("sql-profile-test"))
		log.Trace(context.Background(), time.Now().Add(-time.Millisecond), func() (string, int64) { return "SELECT 1", 1 }, nil)
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(data), "\n")).To(Equal(1))
	})

	It("records every statement regardless of console log level", func() {
		path := filepath.Join(GinkgoT().TempDir(), "sqlprofile.jsonl")
		writer, err := newSQLProfileWriter(path)
		Expect(err).NotTo(HaveOccurred())
		log := &SqlLogger{Config: Config{SlowThreshold: time.Second}, Logger: commons.GetLogger("sql-profile-test"), profile: writer}
		calls := 0
		log.Trace(context.Background(), time.Now().Add(-20*time.Millisecond), func() (string, int64) {
			calls++
			return "SELECT secret", 1
		}, nil)
		Expect(calls).To(Equal(1))
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var event SQLProfileEvent
		Expect(json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event)).To(Succeed())
		Expect(event.DurationNS).To(BeNumerically(">", 0))
		Expect(event.Rows).To(Equal(int64(1)))
		Expect(event.SQL).To(Equal("SELECT secret"))
		Expect(event.Params).To(BeEmpty())
		Expect(string(data)).To(ContainSubstring("\"params\":[]"))
	})

	It("records raw SQL and bound parameters for a GORM trace", func() {
		path := filepath.Join(GinkgoT().TempDir(), "sqlprofile.jsonl")
		writer, err := newSQLProfileWriter(path)
		Expect(err).NotTo(HaveOccurred())
		log := &SqlLogger{Logger: commons.GetLogger("sql-profile-test"), profile: writer}
		log.Trace(context.Background(), time.Now(), func() (string, int64) {
			_, _ = log.ParamsFilter(context.Background(), "SELECT * FROM users WHERE id = ? AND name = ?", 42, "alice")
			return "SELECT * FROM users WHERE id = 42 AND name = 'alice'", 1
		}, nil)
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var event SQLProfileEvent
		Expect(json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event)).To(Succeed())
		Expect(event.SQL).To(Equal("SELECT * FROM users WHERE id = ? AND name = ?"))
		Expect(event.Params).To(Equal([]string{"42", "alice"}))
	})

	It("exports bound values from an executed GORM statement", func() {
		path := filepath.Join(GinkgoT().TempDir(), "sqlprofile.jsonl")
		writer, err := newSQLProfileWriter(path)
		Expect(err).NotTo(HaveOccurred())
		log := &SqlLogger{Logger: commons.GetLogger("sql-profile-test"), profile: writer}
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: log})
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Exec("SELECT ? AS id, ? AS name", 42, "alice").Error).NotTo(HaveOccurred())
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var event SQLProfileEvent
		Expect(json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event)).To(Succeed())
		Expect(event.SQL).To(Equal("SELECT ? AS id, ? AS name"))
		Expect(event.Params).To(Equal([]string{"42", "alice"}))
	})

	It("keeps SQL and parameters paired across concurrent traces", func() {
		path := filepath.Join(GinkgoT().TempDir(), "sqlprofile.jsonl")
		writer, err := newSQLProfileWriter(path)
		Expect(err).NotTo(HaveOccurred())
		log := &SqlLogger{Logger: commons.GetLogger("sql-profile-test"), profile: writer}
		const count = 20
		var group sync.WaitGroup
		for i := range count {
			group.Add(1)
			go func() {
				defer group.Done()
				value := strconv.Itoa(i)
				log.Trace(context.Background(), time.Now(), func() (string, int64) {
					_, _ = log.ParamsFilter(context.Background(), "SELECT ? AS value /* "+value+" */", value)
					return "SELECT " + value + " AS value", 1
				}, nil)
			}()
		}
		group.Wait()
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		Expect(lines).To(HaveLen(count))
		values := map[string]bool{}
		for _, line := range lines {
			var event SQLProfileEvent
			Expect(json.Unmarshal([]byte(line), &event)).To(Succeed())
			Expect(event.Params).To(HaveLen(1))
			Expect(event.SQL).To(Equal("SELECT ? AS value /* " + event.Params[0] + " */"))
			values[event.Params[0]] = true
		}
		Expect(values).To(HaveLen(count))
	})

	It("marks statements that exceed the configured slow threshold", func() {
		path := filepath.Join(GinkgoT().TempDir(), "sqlprofile.jsonl")
		writer, err := newSQLProfileWriter(path)
		Expect(err).NotTo(HaveOccurred())
		log := &SqlLogger{Config: Config{SlowThreshold: time.Microsecond}, Logger: commons.GetLogger("sql-profile-test"), profile: writer}
		log.Trace(context.Background(), time.Now().Add(-time.Millisecond), func() (string, int64) { return "SELECT 1", 1 }, nil)
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var event SQLProfileEvent
		Expect(json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event)).To(Succeed())
		Expect(event.Slow).To(BeTrue())
	})
})
