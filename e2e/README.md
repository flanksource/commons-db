# Commons-DB E2E Testing Framework

Comprehensive end-to-end testing framework for Commons-DB using Ginkgo/Gomega BDD testing patterns.

## Overview

This E2E test suite validates all major components of Commons-DB:
- **Log backends**: OpenSearch, Loki, Kubernetes, CloudWatch, GCP Cloud Logging, BigQuery
- **Connection types**: S3, SFTP, SMB, GCS, Azure Blob Storage, Kubernetes, Git, HTTP
- **Secret management**: Retrieval, caching, redaction, rotation, encryption
- **Query grammar**: Field queries, wildcards, date math, label selectors, complex queries

## Prerequisites

### System Requirements
- Go 1.22+
- Docker and Docker Compose
- 4GB RAM minimum (8GB recommended)
- macOS (Darwin) or Linux

### Dependencies

#### Native Services (via flanksource/deps)
- Postgres 16.1.0
- Redis 7.2.4
- OpenSearch 2.11.1
- Loki 2.9.3
- LocalStack CLI 3.0.2
- envtest (latest Kubernetes version)

#### Docker Containers
- atmoz/sftp:latest
- filesysorg/jfileserver:latest
- fsouza/fake-gcs-server:latest
- mcr.microsoft.com/azure-storage/azurite:latest

## Installation

### 1. Clone the Repository

```bash
git clone https://github.com/flanksource/commons-db.git
cd commons-db
```

### 2. Install Dependencies

```bash
go mod download
```

### 3. Install Ginkgo CLI (optional, for running tests directly)

```bash
go install github.com/onsi/ginkgo/v2/ginkgo@latest
```

## Running Tests

### Quick Start - Run All E2E Tests

```bash
# Using go test (standard)
go test -v ./e2e -timeout=10m

# Using Ginkgo CLI (recommended, with better output)
ginkgo -v ./e2e --timeout=10m
```

### Run Specific Test Suites

```bash
# Log backends only
ginkgo -v ./e2e --focus="Log Backends" --timeout=10m

# Connections only
ginkgo -v ./e2e --focus="Connections" --timeout=10m

# Secret management only
ginkgo -v ./e2e --focus="Secret Management" --timeout=10m

# Query grammar only
ginkgo -v ./e2e --focus="Query Grammar" --timeout=10m
```

### Run Specific Test Descriptions

```bash
# OpenSearch tests only
ginkgo -v ./e2e --focus="OpenSearch" --timeout=10m

# SFTP connection tests
ginkgo -v ./e2e --focus="SFTP" --timeout=10m

# Query parsing tests
ginkgo -v ./e2e --focus="Field Query Parsing" --timeout=10m
```

### Run with Coverage

```bash
go test -v ./e2e -coverprofile=coverage.out
go tool cover -html=coverage.out
```

### Run with Detailed Output

```bash
ginkgo -v ./e2e --timeout=10m --poll-progress-after=1m
```

## Test Organization

### Directory Structure

```
e2e/
├── e2e_suite_test.go       # Main suite setup and teardown
├── logs_test.go            # Log backend tests
├── connections_test.go     # Connection tests
├── secrets_test.go         # Secret management tests
├── query_test.go           # Query grammar tests
├── helpers/
│   ├── services.go         # Service lifecycle management
│   ├── docker.go           # Docker container management
│   ├── fixtures.go         # Test data generation
│   └── assertions.go       # Custom Gomega matchers
├── deps.yaml               # Service dependencies configuration
└── README.md               # This file
```

### Test Files

#### e2e_suite_test.go
- **Purpose**: Suite initialization and teardown
- **Setup**: Starts all services before tests
- **Teardown**: Stops all services after tests
- **Timeout**: 2 minutes for service startup

#### logs_test.go
- **OpenSearch**: Ingestion, searching, wildcard queries
- **Loki**: Label filtering, time range queries
- **Kubernetes**: Pod log retrieval, namespace filtering
- **CloudWatch**: Stream creation, event ingestion
- **GCP Cloud Logging**: Log write/read operations
- **BigQuery**: Query execution validation

#### connections_test.go
- **SFTP**: File upload/download, directory listing
- **SMB**: Share access, file operations
- **S3/LocalStack**: Bucket operations, multipart uploads
- **GCS**: Bucket and object operations
- **Azure Blob Storage**: Container and blob operations
- **Kubernetes**: Resource CRUD operations
- **Git**: Clone, fetch, authentication
- **HTTP**: GET/POST with authentication

#### secrets_test.go
- **Secret Retrieval**: Secure retrieval and caching
- **Sensitive Data Redaction**: Password, API key, token redaction
- **SecretKeeper Interface**: Get, Set, Delete, List operations
- **Secret Rotation**: Old/new secret validation
- **Secret Encryption**: AES-256-GCM, IV generation, key derivation
- **Multi-Backend Support**: AWS Secrets Manager, Vault, Kubernetes, Env Vars

#### query_test.go
- **Field Queries**: Exact matches, comparisons (>=, <=, !=, >, <)
- **Wildcards**: Prefix (*), suffix (*), contains (*)
- **Date Math**: now±Xd/h/m/s, absolute dates
- **Label Selectors**: Simple labels, in/notin operators, existence checks
- **Complex Queries**: AND/OR combinations, nested queries
- **Query Execution**: Filtering, ordering, validation
- **Query Validation**: Syntax checking, format validation

## Service Management

### ServiceManager
Manages native binary services:
- Starts/stops services in order
- Health checks for readiness
- Port management
- Connection URLs

### DockerManager
Manages Docker containers:
- Pulls and runs container images
- Port mapping
- Health checks
- Automatic cleanup

### Port Allocations

| Service | Port | Type |
|---------|------|------|
| PostgreSQL | 5432 | Native |
| Redis | 6379 | Native |
| OpenSearch | 9200 | Native |
| Loki | 3100 | Native |
| LocalStack | 4566 | Native |
| SFTP | 2222 | Docker |
| SMB | 445 | Docker |
| GCS | 4443 | Docker |
| Azurite Blob | 10000 | Docker |
| Azurite Queue | 10001 | Docker |
| Azurite Table | 10002 | Docker |

## Test Fixtures

### GenerateTestLogs
Generate realistic test log data:
```go
// Generate 100 logs with mixed levels and services
logs := helpers.GenerateTestLogs(100)

// Generate logs with specific level
errorLogs := helpers.GenerateTestLogsWithLevel(50, "error")

// Generate logs for specific service
apiLogs := helpers.GenerateTestLogsWithService(50, "api")
```

### GenerateTestFile
Generate test file content:
```go
data := helpers.GenerateTestFile("test.txt", 10)
```

### Resource Naming
```go
bucketName := helpers.GenerateBucketName("prefix")
containerName := helpers.GenerateContainerName("prefix")
```

## Debugging

### Enable Verbose Logging

```bash
ginkgo -v ./e2e --timeout=10m --poll-progress-after=1m
```

### Run Single Test

```bash
ginkgo -v ./e2e --focus="should successfully ingest 100 log entries"
```

### Check Service Connectivity

```bash
# Check if services are running
lsof -i :5432   # PostgreSQL
lsof -i :6379   # Redis
lsof -i :9200   # OpenSearch
lsof -i :3100   # Loki
lsof -i :4566   # LocalStack
```

### View Docker Container Logs

```bash
docker logs <container-id>
docker stats
```

## CI Integration

### GitHub Actions

E2E tests run automatically on:
- Push to `main` and `develop` branches
- Pull requests targeting `main` and `develop`
- Changes to Go files, E2E tests, or workflow files

#### Run Workflows Locally

```bash
# Install act (GitHub Actions locally)
brew install act

# Run E2E workflow
act -j e2e-tests
```

## Troubleshooting

### Port Already in Use

```bash
# Find process using port
lsof -i :PORT_NUMBER

# Kill process
kill -9 PID
```

### Docker Connection Errors

```bash
# Check Docker daemon
docker ps

# Restart Docker
brew services restart docker
# or
service docker restart
```

### Service Startup Timeout

- Increase timeout in test: `--timeout=20m`
- Check available disk space
- Verify network connectivity
- Review service logs

### Memory Issues

- Close other applications
- Increase available RAM
- Run tests sequentially instead of parallel

## Performance

### Typical Test Execution Times

| Metric | Time |
|--------|------|
| Service startup | ~30s |
| Log backend tests | ~1m |
| Connection tests | ~2m |
| Secret tests | ~30s |
| Query tests | ~1m |
| Service teardown | ~10s |
| **Total** | **~5-10 minutes** |

## Contributing

### Adding New Tests

1. Create test spec in appropriate file (logs_test.go, connections_test.go, etc.)
2. Use Ginkgo/Gomega syntax:
   ```go
   It("should do something", func() {
       Expect(result).To(Equal(expected))
   })
   ```
3. Run tests locally: `ginkgo -v ./e2e`
4. Ensure tests pass before submitting PR

### Test Naming Conventions

- **Describe blocks**: Feature name (e.g., "Log Backends", "SFTP")
- **Context blocks**: Scenario (e.g., "when service is running", "when uploading files")
- **It blocks**: Specific behavior (e.g., "should successfully ingest logs")

## References

### Documentation
- [Ginkgo Testing Framework](https://onsi.github.io/ginkgo/)
- [Gomega Matchers](https://onsi.github.io/gomega/)
- [Commons-DB Documentation](../README.md)

### Service Documentation
- [OpenSearch](https://opensearch.org/docs/)
- [Loki](https://grafana.com/docs/loki/)
- [LocalStack](https://docs.localstack.cloud/)
- [Azurite](https://github.com/Azure/Azurite)

## License

This project is licensed under the Apache License 2.0 - see LICENSE file for details.

## Support

For issues or questions:
1. Check this README
2. Review test output and logs
3. Open GitHub issue with test output
4. Contact team at support@flanksource.com
