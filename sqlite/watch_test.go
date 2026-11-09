package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Shared data-version monitoring", func() {
	It("serves more watchers than the reader pool has slots without reserving query connections", func() {
		const watchers = 24
		path := filepath.Join(GinkgoT().TempDir(), "watched.sqlite")
		writer, err := Open(Options{Path: path})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(writer.Close)
		Expect(writer.Write(func(db *sql.DB) error { _, err := db.Exec("CREATE TABLE changes (value INTEGER)"); return err })).To(Succeed())
		reader, err := Open(Options{Path: path, ReadOnly: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(reader.Close)
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		started, ended := make(chan struct{}, watchers), make(chan error, watchers)
		seen := make([]atomic.Bool, watchers)
		for index := range watchers {
			go func() {
				started <- struct{}{}
				ended <- reader.WatchDataVersion(ctx, 10*time.Millisecond, func() { seen[index].Store(true) })
			}()
		}
		for range watchers {
			Eventually(started).Should(Receive())
		}
		Consistently(func() int { return reader.Reader().Stats().InUse }, 150*time.Millisecond).Should(BeZero())
		Expect(writer.Write(func(db *sql.DB) error { _, err := db.Exec("INSERT INTO changes VALUES (1)"); return err })).To(Succeed())
		Eventually(func() int {
			count := 0
			for index := range seen {
				if seen[index].Load() {
					count++
				}
			}
			return count
		}).Should(Equal(watchers))
		var count int
		Expect(reader.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM changes").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
		cancel()
		for range watchers {
			Eventually(ended).Should(Receive(BeNil()))
		}
		Expect(reader.Close()).To(Succeed())
	})
})
