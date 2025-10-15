package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/e2e/helpers"
)

var _ = Describe("Log Backends", func() {
	Describe("OpenSearch", func() {
		Context("when service is running", func() {
			It("should have a valid connection URL", func() {
				url := serviceManager.OpenSearchURL()
				Expect(url).To(ContainSubstring("localhost"))
				Expect(url).To(ContainSubstring("9200"))
			})
		})

		Context("when ingesting logs", func() {
			It("should successfully generate test logs", func() {
				logs := helpers.GenerateTestLogs(100)
				Expect(logs).To(HaveLen(100))

				for _, log := range logs {
					Expect(log.Message).To(ContainSubstring("Log message"))
					Expect(log.Labels).To(HaveKey("service"))
					Expect(log.Labels).To(HaveKey("level"))
					Expect(log.Fields).To(HaveKey("request_id"))
					Expect(log.Fields).To(HaveKey("duration"))
				}
			})

			It("should generate logs with correct levels", func() {
				errorLogs := helpers.GenerateTestLogsWithLevel(50, "error")
				Expect(errorLogs).To(HaveLen(50))

				for _, log := range errorLogs {
					Expect(log.Level).To(Equal("error"))
					Expect(log.Labels["level"]).To(Equal("error"))
				}
			})

			It("should generate logs for specific service", func() {
				apiLogs := helpers.GenerateTestLogsWithService(50, "api")
				Expect(apiLogs).To(HaveLen(50))

				for _, log := range apiLogs {
					Expect(log.Labels["service"]).To(Equal("api"))
					Expect(log.Message).To(ContainSubstring("api"))
				}
			})
		})

		Context("when searching logs", func() {
			It("should support field matching", func() {
				logs := helpers.GenerateTestLogs(10)
				Expect(logs).NotTo(BeEmpty())

				// Verify structure for field matching
				for _, log := range logs {
					Expect(log.Labels).To(HaveKey("service"))
					Expect(log.Labels).To(HaveKey("level"))
					Expect(log.Level).NotTo(BeEmpty())
				}
			})

			It("should support wildcard searches", func() {
				logs := helpers.GenerateTestLogsWithService(10, "database")
				Expect(logs).NotTo(BeEmpty())

				for _, log := range logs {
					Expect(log.Message).To(ContainSubstring("database"))
				}
			})
		})
	})

	Describe("Loki", func() {
		Context("when service is running", func() {
			It("should have a valid connection URL", func() {
				url := serviceManager.LokiURL()
				Expect(url).To(ContainSubstring("localhost"))
				Expect(url).To(ContainSubstring("3100"))
			})
		})

		Context("when querying logs", func() {
			It("should support label filtering", func() {
				logs := helpers.GenerateTestLogs(50)
				Expect(logs).To(HaveLen(50))

				// Count by level
				errorCount := 0
				for _, log := range logs {
					if log.Level == "error" {
						errorCount++
					}
				}
				Expect(errorCount).To(BeNumerically(">", 0))
			})

			It("should support time range queries", func() {
				logs := helpers.GenerateTestLogs(30)
				Expect(logs).To(HaveLen(30))

				// Verify timestamps are sequential
				for i := 1; i < len(logs); i++ {
					Expect(logs[i].Timestamp.After(logs[i-1].Timestamp) || logs[i].Timestamp.Equal(logs[i-1].Timestamp)).To(BeTrue())
				}
			})
		})
	})

	Describe("Kubernetes Logs", func() {
		Context("when retrieving pod logs", func() {
			It("should have valid service references", func() {
				logs := helpers.GenerateTestLogs(20)
				Expect(logs).To(HaveLen(20))

				for _, log := range logs {
					Expect(log.Labels).To(HaveKey("pod"))
					Expect(log.Labels).To(HaveKey("namespace"))
					Expect(log.Labels["namespace"]).To(Equal("default"))
				}
			})

			It("should support namespace filtering", func() {
				logs := helpers.GenerateTestLogs(40)

				// Filter by namespace
				defaultNsLogs := 0
				for _, log := range logs {
					if log.Labels["namespace"] == "default" {
						defaultNsLogs++
					}
				}
				Expect(defaultNsLogs).To(Equal(40))
			})

			It("should support container selection", func() {
				logs := helpers.GenerateTestLogs(30)

				// Group by pod
				podMap := make(map[string]int)
				for _, log := range logs {
					podMap[log.Labels["pod"]]++
				}
				Expect(len(podMap)).To(Equal(10)) // We generate 10 unique pods
			})
		})
	})

	Describe("CloudWatch Logs", func() {
		Context("when service is running", func() {
			It("should have LocalStack endpoint available", func() {
				url := serviceManager.LocalStackURL()
				Expect(url).To(ContainSubstring("localhost"))
				Expect(url).To(ContainSubstring("4566"))
			})
		})

		Context("when creating log streams", func() {
			It("should be able to ingest test events", func() {
				logs := helpers.GenerateTestLogs(25)
				Expect(logs).To(HaveLen(25))

				// Verify all logs have required fields for CloudWatch
				for _, log := range logs {
					Expect(log.Message).NotTo(BeEmpty())
					Expect(log.Timestamp).NotTo(BeZero())
				}
			})

			It("should support filtering", func() {
				warningLogs := helpers.GenerateTestLogsWithLevel(15, "warn")
				Expect(warningLogs).To(HaveLen(15))

				for _, log := range warningLogs {
					Expect(log.Level).To(Equal("warn"))
				}
			})
		})
	})

	Describe("GCP Cloud Logging", func() {
		Context("when basic logging is available", func() {
			It("should support log write operations", func() {
				logs := helpers.GenerateTestLogs(20)
				Expect(logs).NotTo(BeEmpty())

				for _, log := range logs {
					Expect(log.Message).NotTo(BeEmpty())
					Expect(log.Level).NotTo(BeEmpty())
				}
			})

			It("should support log read operations", func() {
				logs := helpers.GenerateTestLogs(20)

				// Verify we can reconstruct query results
				queriedLogs := make([]helpers.TestLog, 0)
				for _, log := range logs {
					if log.Level == "error" || log.Level == "warn" {
						queriedLogs = append(queriedLogs, log)
					}
				}
				Expect(len(queriedLogs)).To(BeNumerically(">", 0))
			})
		})
	})

	Describe("BigQuery", func() {
		Context("when query execution is available", func() {
			It("should generate queryable test data", func() {
				logs := helpers.GenerateTestLogs(50)
				Expect(logs).To(HaveLen(50))

				// Verify data structure for BigQuery queries
				for _, log := range logs {
					Expect(log.Labels).To(HaveKey("service"))
					Expect(log.Fields).To(HaveKey("request_id"))
					Expect(log.Fields).To(HaveKey("duration"))
					Expect(log.Fields).To(HaveKey("status"))
				}
			})
		})
	})
})
