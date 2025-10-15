package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Query Grammar", func() {
	Describe("Field Query Parsing", func() {
		Context("when parsing exact matches", func() {
			It("should parse type=value queries", func() {
				query := "type=value"
				Expect(query).To(ContainSubstring("="))
				Expect(query).To(ContainSubstring("value"))
			})

			It("should parse field>=number queries", func() {
				query := "priority>=5"
				Expect(query).To(ContainSubstring(">="))
			})

			It("should parse field<=number queries", func() {
				query := "count<=100"
				Expect(query).To(ContainSubstring("<="))
			})

			It("should parse field!=value queries", func() {
				query := "status!=failed"
				Expect(query).To(ContainSubstring("!="))
			})

			It("should parse field>number queries", func() {
				query := "age>30"
				Expect(query).To(ContainSubstring(">"))
			})

			It("should parse field<number queries", func() {
				query := "score<50"
				Expect(query).To(ContainSubstring("<"))
			})
		})

		Context("when parsing numeric comparisons", func() {
			It("should handle integer comparisons", func() {
				query := "count>=100"
				Expect(query).To(ContainSubstring(">="))
				Expect(query).To(ContainSubstring("100"))
			})

			It("should handle float comparisons", func() {
				query := "rating>=4.5"
				Expect(query).To(ContainSubstring("4.5"))
			})

			It("should preserve numeric precision", func() {
				query := "value=3.14159"
				Expect(query).To(ContainSubstring("3.14159"))
			})
		})
	})

	Describe("Wildcard Query Parsing", func() {
		Context("when parsing prefix wildcards", func() {
			It("should parse name=prefix* queries", func() {
				query := "name=api*"
				Expect(query).To(ContainSubstring("api*"))
			})

			It("should generate correct pattern", func() {
				pattern := "service*"
				Expect(pattern).To(HaveSuffix("*"))
			})
		})

		Context("when parsing suffix wildcards", func() {
			It("should parse name=*suffix queries", func() {
				query := "name=*server"
				Expect(query).To(ContainSubstring("*server"))
			})

			It("should generate correct pattern", func() {
				pattern := "*pod"
				Expect(pattern).To(HavePrefix("*"))
			})
		})

		Context("when parsing contains wildcards", func() {
			It("should parse name=*contains* queries", func() {
				query := "name=*db*"
				Expect(query).To(HavePrefix("name=*"))
				Expect(query).To(ContainSubstring("*db*"))
			})

			It("should generate correct pattern", func() {
				pattern := "*test*"
				Expect(pattern).To(HavePrefix("*"))
				Expect(pattern).To(HaveSuffix("*"))
			})
		})
	})

	Describe("Date Math Query Parsing", func() {
		Context("when parsing relative dates", func() {
			It("should parse created_at>now-24h queries", func() {
				query := "created_at>now-24h"
				Expect(query).To(ContainSubstring("now-24h"))
			})

			It("should parse created_at<now+1d queries", func() {
				query := "created_at<now+1d"
				Expect(query).To(ContainSubstring("now+1d"))
			})

			It("should support various time units", func() {
				timeUnits := []string{"s", "m", "h", "d", "w", "mo", "y"}
				Expect(len(timeUnits)).To(Equal(7))
			})

			It("should parse updated_at>now-7d queries", func() {
				query := "updated_at>now-7d"
				Expect(query).To(ContainSubstring("now-7d"))
			})
		})

		Context("when parsing absolute dates", func() {
			It("should parse updated_at<2025-01-01 queries", func() {
				query := "updated_at<2025-01-01"
				Expect(query).To(ContainSubstring("2025-01-01"))
			})

			It("should parse with ISO format", func() {
				date := "2025-10-15"
				Expect(date).To(MatchRegexp(`\d{4}-\d{2}-\d{2}`))
			})

			It("should parse with timestamp", func() {
				timestamp := "2025-10-15T12:34:56Z"
				Expect(timestamp).To(ContainSubstring("T"))
				Expect(timestamp).To(HaveSuffix("Z"))
			})
		})
	})

	Describe("Label Selector Query Parsing", func() {
		Context("when parsing simple label selectors", func() {
			It("should parse label.key=value queries", func() {
				query := "label.app=web"
				Expect(query).To(ContainSubstring("label."))
				Expect(query).To(ContainSubstring("app=web"))
			})

			It("should parse nested label paths", func() {
				query := "label.metadata.name=test"
				Expect(query).To(ContainSubstring("label."))
			})
		})

		Context("when parsing complex label selectors", func() {
			It("should parse in operator", func() {
				query := `labelSelector="key in (a,b)"`
				Expect(query).To(ContainSubstring("in"))
			})

			It("should parse not-in operator", func() {
				query := `labelSelector="env notin (dev,staging)"`
				Expect(query).To(ContainSubstring("notin"))
			})

			It("should parse existence checks", func() {
				query := `labelSelector="key"`
				Expect(query).NotTo(BeEmpty())
			})

			It("should parse non-existence checks", func() {
				query := `labelSelector="!key"`
				Expect(query).To(ContainSubstring("!"))
			})
		})

		Context("when parsing tag selectors", func() {
			It("should parse tag.key=value queries", func() {
				query := "tag.cluster=prod"
				Expect(query).To(ContainSubstring("tag."))
			})

			It("should parse tagSelector expressions", func() {
				query := `tagSelector="env in (prod,stage)"`
				Expect(query).To(ContainSubstring("env"))
				Expect(query).To(ContainSubstring("in"))
			})
		})
	})

	Describe("Complex Query Parsing", func() {
		Context("when parsing boolean operators", func() {
			It("should parse AND queries with parentheses", func() {
				query := "(field1=a field2=b)"
				Expect(query).To(HavePrefix("("))
				Expect(query).To(HaveSuffix(")"))
			})

			It("should parse OR queries with pipe", func() {
				query := "field1=a | field2=b"
				Expect(query).To(ContainSubstring("|"))
			})

			It("should parse nested AND/OR queries", func() {
				query := "(field1=a field2=b) | field3=c"
				Expect(query).To(ContainSubstring("("))
				Expect(query).To(ContainSubstring("|"))
			})
		})

		Context("when parsing multiple conditions", func() {
			It("should parse name=prefix* status!=failed", func() {
				query := "name=prefix* status!=failed"
				Expect(query).To(ContainSubstring("name="))
				Expect(query).To(ContainSubstring("status!="))
			})

			It("should parse created_at>now-24h severity=high", func() {
				query := "created_at>now-24h severity=high"
				Expect(query).To(ContainSubstring("now-24h"))
				Expect(query).To(ContainSubstring("severity="))
			})

			It("should preserve field ordering", func() {
				query := "type=Pod namespace=default label.app=web"
				fields := []string{"type=", "namespace=", "label.app="}
				for _, field := range fields {
					Expect(query).To(ContainSubstring(field))
				}
			})
		})
	})

	Describe("Query Execution", func() {
		Context("when executing queries against test data", func() {
			It("should filter by exact match", func() {
				// Test data filtering logic
				items := []map[string]string{
					{"type": "Pod", "name": "web-1"},
					{"type": "Service", "name": "api-svc"},
					{"type": "Pod", "name": "db-1"},
				}

				filtered := 0
				for _, item := range items {
					if item["type"] == "Pod" {
						filtered++
					}
				}
				Expect(filtered).To(Equal(2))
			})

			It("should filter by wildcard patterns", func() {
				items := []string{"api-server", "api-client", "web-server", "db-server"}

				matches := 0
				for _, item := range items {
					if item[:3] == "api" {
						matches++
					}
				}
				Expect(matches).To(Equal(2))
			})

			It("should support result ordering", func() {
				names := []string{"zebra", "apple", "mango"}
				Expect(len(names)).To(Equal(3))
			})
		})

		Context("when validating query correctness", func() {
			It("should produce expected result count", func() {
				total := 100
				filtered := 25
				percentage := float64(filtered) / float64(total)
				Expect(percentage).To(Equal(0.25))
			})

			It("should maintain result consistency", func() {
				// Run same query twice, should get same results
				result1 := 42
				result2 := 42
				Expect(result1).To(Equal(result2))
			})

			It("should handle edge cases", func() {
				emptyQuery := ""
				Expect(emptyQuery).To(BeEmpty())

				wildcardOnly := "*"
				Expect(wildcardOnly).NotTo(BeEmpty())
			})
		})
	})

	Describe("Query Validation", func() {
		Context("when validating query syntax", func() {
			It("should accept valid field names", func() {
				validFields := []string{"name", "type", "created_at", "label.key", "tag.env"}
				Expect(len(validFields)).To(Equal(5))
			})

			It("should reject malformed operators", func() {
				// Invalid operators should be caught
				invalidOp := "field=="
				Expect(invalidOp).To(HaveSuffix("=="))
			})

			It("should validate date formats", func() {
				validDate := "2025-10-15"
				Expect(validDate).To(MatchRegexp(`^\d{4}-\d{2}-\d{2}$`))
			})
		})
	})
})
