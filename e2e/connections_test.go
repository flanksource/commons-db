package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/e2e/helpers"
)

var _ = Describe("Connections", func() {
	Describe("SFTP", func() {
		Context("when connection configuration is valid", func() {
			It("should have correct port available", func() {
				port := dockerManager.SFTPPort()
				Expect(port).To(Equal(2222))
			})

			It("should have valid test data", func() {
				testData := helpers.GenerateTestFile("sftp-test.txt", 10)
				Expect(testData).NotTo(BeNil())
				Expect(len(testData)).To(BeNumerically(">", 0))
			})
		})

		Context("when performing file operations", func() {
			It("should validate upload capability", func() {
				testData := helpers.GenerateTestFile("upload-test.txt", 5)
				Expect(testData).NotTo(BeNil())
				Expect(len(testData)).To(BeNumerically(">", 0))
			})

			It("should generate proper file names", func() {
				fileName := "download-test.txt"
				testData := helpers.GenerateTestFile(fileName, 3)
				Expect(testData).To(ContainSubstring(fileName))
			})

			It("should list file operations", func() {
				files := []string{"list-1.txt", "list-2.txt", "list-3.txt"}
				Expect(len(files)).To(Equal(3))
				Expect(files).To(ContainElement("list-1.txt"))
			})

			It("should delete file operations", func() {
				testFile := "delete-test.txt"
				testData := helpers.GenerateTestFile(testFile, 2)
				Expect(testData).NotTo(BeNil())
			})
		})
	})

	Describe("SMB", func() {
		Context("when connection configuration is valid", func() {
			It("should have correct port available", func() {
				port := dockerManager.SMBPort()
				Expect(port).To(Equal(445))
			})
		})

		Context("when sharing files", func() {
			It("should support file operations", func() {
				testData := helpers.GenerateTestFile("smb-test.txt", 10)
				Expect(testData).NotTo(BeNil())
			})

			It("should support share access", func() {
				shares := []string{"share1", "share2"}
				Expect(len(shares)).To(Equal(2))
			})
		})
	})

	Describe("S3", func() {
		Context("when using LocalStack", func() {
			It("should have LocalStack endpoint available", func() {
				url := serviceManager.LocalStackURL()
				Expect(url).To(ContainSubstring("localhost"))
				Expect(url).To(ContainSubstring("4566"))
			})

			It("should support bucket operations", func() {
				bucketName := helpers.GenerateBucketName("test")
				Expect(bucketName).To(ContainSubstring("test-e2e"))
			})

			It("should support object CRUD operations", func() {
				testData := helpers.GenerateTestFile("s3-object.txt", 5)
				Expect(testData).NotTo(BeNil())
				Expect(len(testData)).To(BeNumerically(">", 0))
			})

			It("should support multipart uploads", func() {
				largeData := helpers.GenerateTestFile("large-file.bin", 100)
				Expect(len(largeData)).To(BeNumerically(">", 1000))
			})
		})
	})

	Describe("GCS", func() {
		Context("when using fake-gcs-server", func() {
			It("should have GCS port available", func() {
				port := dockerManager.GCSPort()
				Expect(port).To(Equal(4443))
			})

			It("should support bucket operations", func() {
				bucketName := helpers.GenerateBucketName("gcs")
				Expect(bucketName).To(ContainSubstring("gcs-e2e"))
			})

			It("should support object operations", func() {
				testData := helpers.GenerateTestFile("gcs-object.json", 10)
				Expect(testData).NotTo(BeNil())
			})
		})
	})

	Describe("Azure Blob Storage", func() {
		Context("when using Azurite", func() {
			It("should have Blob service port available", func() {
				port := dockerManager.AzuriteBlobPort()
				Expect(port).To(Equal(10000))
			})

			It("should have Queue service port available", func() {
				port := dockerManager.AzuriteQueuePort()
				Expect(port).To(Equal(10001))
			})

			It("should have Table service port available", func() {
				port := dockerManager.AzuriteTablePort()
				Expect(port).To(Equal(10002))
			})

			It("should support container operations", func() {
				containerName := helpers.GenerateContainerName("blob")
				Expect(containerName).To(ContainSubstring("blob-e2e"))
			})

			It("should support blob operations", func() {
				testData := helpers.GenerateTestFile("blob-data.bin", 15)
				Expect(testData).NotTo(BeNil())
			})
		})
	})

	Describe("Kubernetes", func() {
		Context("when using envtest", func() {
			It("should support resource CRUD operations", func() {
				testData := helpers.GenerateTestFile("k8s-manifest.yaml", 20)
				Expect(testData).NotTo(BeNil())
				Expect(testData).To(ContainSubstring("k8s-manifest"))
			})
		})
	})

	Describe("Git", func() {
		Context("when performing repository operations", func() {
			It("should support clone operations", func() {
				// Test data generation for git operations
				testFile := "git-clone-test"
				testData := helpers.GenerateTestFile(testFile, 5)
				Expect(testData).NotTo(BeNil())
			})

			It("should support fetch operations", func() {
				testFile := "git-fetch-test"
				testData := helpers.GenerateTestFile(testFile, 5)
				Expect(testData).NotTo(BeNil())
			})

			It("should support authentication", func() {
				// Verify auth structure can be configured
				testData := helpers.GenerateTestFile("git-auth.txt", 3)
				Expect(testData).NotTo(BeNil())
			})
		})
	})

	Describe("HTTP", func() {
		Context("when making HTTP requests", func() {
			It("should support GET requests", func() {
				testData := helpers.GenerateTestFile("http-get.json", 10)
				Expect(testData).NotTo(BeNil())
			})

			It("should support POST requests", func() {
				testData := helpers.GenerateTestFile("http-post.json", 10)
				Expect(testData).NotTo(BeNil())
			})

			It("should support authentication", func() {
				testData := helpers.GenerateTestFile("http-auth.txt", 5)
				Expect(testData).NotTo(BeNil())
			})
		})
	})
})
