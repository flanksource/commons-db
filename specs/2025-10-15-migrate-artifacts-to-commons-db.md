# Migration Specification: Artifacts to Commons-DB

**Date:** 2025-10-15
**Scope:** Large
**Status:** Draft
**Revision:** 1.0

---

## 1. Problem Statement

The `flanksource/artifacts` repository provides generic blob storage functionality with support for multiple storage backends (S3, GCS, SFTP, SMB, local filesystem). Currently:

- `artifacts` depends on `flanksource/duty` for connection types, context, and models
- `commons-db` imports `artifacts` as a dependency
- This creates a circular dependency chain: `commons-db` → `artifacts` → `duty`
- Connection management is split across two repositories
- Filesystem operations are separated from their connection definitions

**Goal:** Consolidate artifacts functionality into `commons-db` to create a unified, self-contained common library without external duty dependencies.

---

## 2. Success Criteria

### Functional Requirements
- [ ] All 5 storage backends (S3, GCS, SFTP, SMB, Local) work identically after migration
- [ ] Filesystem operations (Read, Write, ReadDir, Stat) are integrated with connection types
- [ ] No duty dependencies remain in commons-db
- [ ] All existing artifacts tests pass after migration
- [ ] Connection types support filesystem operations without breaking existing usage

### Non-Functional Requirements
- [ ] No file exceeds 500 lines (refactor if needed)
- [ ] No function exceeds 50 lines
- [ ] All code passes `make lint` and `make build`
- [ ] Test coverage maintained or improved
- [ ] API is simple, composable, and testable

---

## 3. Current Architecture Analysis

### Artifacts Repository Structure
```
artifacts/
├── artifacts.go              # SaveArtifact (with DB persistence) - REMOVE
├── connection_filesystem.go  # GetFSForConnection factory
├── fs/
│   ├── fs.go                # Filesystem, FilesystemRW interfaces
│   ├── s3.go                # S3 implementation
│   ├── gcs.go               # GCS implementation
│   ├── ssh.go               # SFTP implementation
│   ├── smb.go               # SMB implementation
│   ├── local.go             # Local filesystem
│   └── sftp_dirfs.go        # SFTP directory helper
└── clients/
    ├── aws/
    │   ├── session.go       # DUPLICATE - commons-db has better version
    │   └── fileinfo.go      # S3FileInfo - KEEP
    ├── gcp/
    │   └── fileinfo.go      # GCSFileInfo - KEEP
    ├── sftp/
    │   └── sftp.go          # 28 lines - INLINE into fs/sftp.go
    └── smb/
        └── smb.go           # 64 lines - INLINE into fs/smb.go
```

### Commons-DB Already Has
- `connection/aws.go` → `AWSConnection.Client()` returns `aws.Config` ✓
- `connection/gcs.go` → `GCSConnection.Client()` returns `*gcs.Client` ✓
- `connection/sftp.go` → Basic SFTP connection type
- `connection/smb.go` → Basic SMB connection type
- `types/envvar.go` → EnvVar type ✓
- `types/common.go` → Authentication type ✓

### Dependencies to Remove
- `duty/context.Context` → Use `commons-db/context.Context`
- `duty/connection.*` → Use `commons-db/connection.*`
- `duty/models.Artifact` → **REMOVE** (no DB persistence)
- `duty/types.*` → Use `commons-db/types.*`

---

## 4. Target Architecture

### Package Structure
```
commons-db/
├── fs/                      # New package for filesystem operations
│   ├── fs.go               # Interfaces: Filesystem, FilesystemRW, FileInfo
│   ├── s3.go               # S3 impl + S3FileInfo
│   ├── gcs.go              # GCS impl + GCSFileInfo
│   ├── sftp.go             # SFTP impl (inline client code)
│   ├── smb.go              # SMB impl (inline client code)
│   └── local.go            # Local filesystem impl
├── connection/
│   ├── s3.go               # Add: Filesystem() method
│   ├── gcs.go              # Add: Filesystem() method
│   ├── sftp.go             # Add: Filesystem() method
│   ├── smb.go              # Add: Filesystem() method
│   └── helpers.go          # Add: GetFilesystemForConnection()
└── types/
    └── common.go           # Already has EnvVar, Authentication
```

### API Design

#### Core Interface (fs/fs.go)
```go
package fs

import (
    "context"
    "io"
    "os"
)

// Filesystem provides read-only operations
type Filesystem interface {
    Close() error
    ReadDir(name string) ([]FileInfo, error)
    Stat(name string) (os.FileInfo, error)
}

// FilesystemRW extends Filesystem with write operations
type FilesystemRW interface {
    Filesystem
    Read(ctx context.Context, path string) (io.ReadCloser, error)
    Write(ctx context.Context, path string, data io.Reader) (os.FileInfo, error)
}

// FileInfo wraps os.FileInfo with full path
type FileInfo interface {
    os.FileInfo
    FullPath() string
}
```

#### Connection Extensions
```go
// connection/s3.go
func (c *S3Connection) Filesystem(ctx context.Context) (fs.FilesystemRW, error) {
    cfg, err := c.Client(ctx)  // Reuse existing Client() method
    if err != nil {
        return nil, err
    }
    return fs.NewS3FS(ctx, c.Bucket, cfg)
}

// connection/gcs.go
func (c *GCSConnection) Filesystem(ctx context.Context) (fs.FilesystemRW, error) {
    client, err := c.Client(ctx)  // Reuse existing Client() method
    if err != nil {
        return nil, err
    }
    return fs.NewGCSFS(ctx, c.Bucket, client)
}

// connection/helpers.go
func GetFilesystemForConnection(ctx context.Context, conn models.Connection) (fs.FilesystemRW, error)
```

---

## 5. Implementation Plan

### Phase 1: Preparation (1-2 hours)

**Step 1.1: Create branch and setup**
```bash
cd /Users/moshe/go/src/github.com/flanksource/commons-db
git checkout -b feat/migrate-artifacts
go test ./... -v
```

**Verification:**
- All existing tests pass
- Branch created successfully

**Git:**
```bash
git commit --allow-empty -m "feat: start artifacts migration"
```

**Step 1.2: Create fs package with interfaces**
- Create `fs/` directory
- Copy `artifacts/fs/fs.go` to `commons-db/fs/fs.go`
- Update package name to `fs`
- Remove all duty imports
- Change `gocontext` to `context`

**Verification:**
```bash
go build ./fs
```

**Git:**
```bash
git add fs/fs.go
git commit -m "feat(fs): add filesystem interfaces"
```

---

### Phase 2: Migrate Storage Implementations (6-8 hours)

**Step 2.1: Migrate S3 filesystem**

Copy and adapt:
- `artifacts/fs/s3.go` → `commons-db/fs/s3.go`
- `artifacts/clients/aws/fileinfo.go` → Include in `fs/s3.go`

Changes:
- Remove duty imports, use `commons-db/context`
- Update to use `aws.Config` (from existing `AWSConnection.Client()`)
- Constructor: `NewS3FS(ctx context.Context, bucket string, cfg aws.Config)`
- Inline S3FileInfo type into same file

**Verification:**
```bash
go build ./fs
# Run any S3-specific tests
```

**Git:**
```bash
git add fs/s3.go
git commit -m "feat(fs): migrate S3 filesystem implementation"
```

**Step 2.2: Migrate GCS filesystem**

Copy and adapt:
- `artifacts/fs/gcs.go` → `commons-db/fs/gcs.go`
- `artifacts/clients/gcp/fileinfo.go` → Include in `fs/gcs.go`

Changes:
- Remove duty imports, use `commons-db/context`
- Update to use `*gcs.Client` (from existing `GCSConnection.Client()`)
- Constructor: `NewGCSFS(ctx context.Context, bucket string, client *gcs.Client)`
- Inline GCSFileInfo type into same file

**Verification:**
```bash
go build ./fs
```

**Git:**
```bash
git add fs/gcs.go
git commit -m "feat(fs): migrate GCS filesystem implementation"
```

**Step 2.3: Migrate SFTP filesystem**

Copy and adapt:
- `artifacts/fs/ssh.go` → `commons-db/fs/sftp.go`
- `artifacts/fs/sftp_dirfs.go` → Merge into `fs/sftp.go`
- `artifacts/clients/sftp/sftp.go` → Inline connect logic into `fs/sftp.go`

Changes:
- Inline the 28-line SSHConnect function directly
- Remove duty imports
- Constructor: `NewSFTPFS(host, username, password string)`
- Keep all logic in single file

**Verification:**
```bash
go build ./fs
```

**Git:**
```bash
git add fs/sftp.go
git commit -m "feat(fs): migrate SFTP filesystem implementation"
```

**Step 2.4: Migrate SMB filesystem**

Copy and adapt:
- `artifacts/fs/smb.go` → `commons-db/fs/smb.go`
- `artifacts/clients/smb/smb.go` → Inline SMBSession and SMBConnect into `fs/smb.go`

Changes:
- Inline the 64-line SMB client code directly
- Update to use `commons-db/types.Authentication`
- Constructor: `NewSMBFS(host, port, share string, auth types.Authentication)`
- Keep all logic in single file

**Verification:**
```bash
go build ./fs
```

**Git:**
```bash
git add fs/smb.go
git commit -m "feat(fs): migrate SMB filesystem implementation"
```

**Step 2.5: Migrate Local filesystem**

Copy and adapt:
- `artifacts/fs/local.go` → `commons-db/fs/local.go`

Changes:
- Minimal changes needed
- Constructor: `NewLocalFS(path string)`

**Verification:**
```bash
go build ./fs
```

**Git:**
```bash
git add fs/local.go
git commit -m "feat(fs): migrate local filesystem implementation"
git tag -f wip-compile
```

---

### Phase 3: Extend Connection Types (2-3 hours)

**Step 3.1: Extend S3Connection**

Edit `connection/s3.go`:
```go
import (
    "github.com/flanksource/commons-db/fs"
)

func (c *S3Connection) Filesystem(ctx context.Context) (fs.FilesystemRW, error) {
    if err := c.Populate(ctx); err != nil {
        return nil, err
    }
    cfg, err := c.Client(ctx)
    if err != nil {
        return nil, err
    }
    return fs.NewS3FS(ctx, c.Bucket, cfg)
}
```

**Verification:**
- Write integration test
- Test Read/Write/ReadDir operations

**Git:**
```bash
git add connection/s3.go
git commit -m "feat(connection): add Filesystem method to S3Connection"
```

**Step 3.2: Extend GCSConnection**

Edit `connection/gcs.go`:
```go
import "github.com/flanksource/commons-db/fs"

func (c *GCSConnection) Filesystem(ctx context.Context) (fs.FilesystemRW, error) {
    if err := c.HydrateConnection(ctx); err != nil {
        return nil, err
    }
    client, err := c.Client(ctx)
    if err != nil {
        return nil, err
    }
    return fs.NewGCSFS(ctx, c.Bucket, client)
}
```

**Verification:**
- Integration test for GCSConnection.Filesystem()

**Git:**
```bash
git add connection/gcs.go
git commit -m "feat(connection): add Filesystem method to GCSConnection"
```

**Step 3.3: Extend SFTP Connection**

Edit `connection/sftp.go`:
```go
import "github.com/flanksource/commons-db/fs"

func (c *SFTPConnection) Filesystem(ctx context.Context) (fs.FilesystemRW, error) {
    // Parse connection details and create filesystem
    return fs.NewSFTPFS(c.Host, c.Username, c.Password)
}
```

**Verification:**
- Integration test

**Git:**
```bash
git add connection/sftp.go
git commit -m "feat(connection): add Filesystem method to SFTPConnection"
```

**Step 3.4: Extend SMB Connection**

Edit `connection/smb.go`:
```go
import "github.com/flanksource/commons-db/fs"

func (c *SMBConnection) Filesystem(ctx context.Context) (fs.FilesystemRW, error) {
    return fs.NewSMBFS(c.Host, c.Port, c.Share, c.Auth)
}
```

**Verification:**
- Integration test

**Git:**
```bash
git add connection/smb.go
git commit -m "feat(connection): add Filesystem method to SMBConnection"
git tag -f wip-compile
```

---

### Phase 4: Migration Helper (1-2 hours)

**Step 4.1: Create backward-compatibility helper**

Create `connection/helpers.go`:
```go
package connection

import (
    "fmt"

    "github.com/flanksource/commons-db/context"
    "github.com/flanksource/commons-db/fs"
    "github.com/flanksource/commons-db/models"
)

// GetFilesystemForConnection creates a filesystem from a models.Connection
// This provides backward compatibility for code migrating from artifacts.GetFSForConnection
func GetFilesystemForConnection(ctx context.Context, conn models.Connection) (fs.FilesystemRW, error) {
    switch conn.Type {
    case models.ConnectionTypeFolder:
        path := conn.Properties["path"]
        return fs.NewLocalFS(path), nil

    case models.ConnectionTypeS3:
        s3Conn := &S3Connection{}
        s3Conn.FromModel(conn)
        return s3Conn.Filesystem(ctx)

    case models.ConnectionTypeGCS:
        gcsConn := &GCSConnection{}
        gcsConn.FromModel(conn)
        return gcsConn.Filesystem(ctx)

    case models.ConnectionTypeSFTP:
        sftpConn := &SFTPConnection{}
        sftpConn.FromModel(conn)
        return sftpConn.Filesystem(ctx)

    case models.ConnectionTypeSMB:
        smbConn := &SMBConnection{}
        smbConn.FromModel(conn)
        return smbConn.Filesystem(ctx)

    default:
        return nil, fmt.Errorf("unsupported connection type: %s", conn.Type)
    }
}
```

**Verification:**
- Unit tests with mock Connection objects
- Test all 5 connection types

**Git:**
```bash
git add connection/helpers.go
git commit -m "feat(connection): add GetFilesystemForConnection helper"
```

---

### Phase 5: Testing & Validation (3-4 hours)

**Step 5.1: Port artifacts tests**

- Review `artifacts/fs/fs_test.go`
- Create `fs/s3_test.go`, `fs/gcs_test.go`, etc.
- Port test patterns for each backend
- Create integration tests in `connection/*_test.go`

**Verification:**
```bash
go test ./fs/... -v
go test ./connection/... -v
```

**Git:**
```bash
git add fs/*_test.go connection/*_test.go
git commit -m "test(fs): add filesystem tests"
```

**Step 5.2: Run full test suite**
```bash
cd /Users/moshe/go/src/github.com/flanksource/commons-db
go test ./... -v
make lint
make build
```

**Verification:**
- All tests pass
- No lint errors
- Build succeeds

**Git:**
```bash
git tag -f wip-pass
```

---

### Phase 6: Update Dependencies (1-2 hours)

**Step 6.1: Update go.mod**
```bash
cd /Users/moshe/go/src/github.com/flanksource/commons-db
# Edit go.mod to remove artifacts dependency
go mod tidy
go build ./...
```

**Verification:**
```bash
grep "flanksource/artifacts" go.mod && echo "FAIL" || echo "PASS"
go mod tidy
go build ./...
```

**Git:**
```bash
git add go.mod go.sum
git commit -m "chore: remove artifacts dependency"
```

**Step 6.2: Update internal consumers**

Search for any internal usage:
```bash
git grep "flanksource/artifacts" -- '*.go' | grep -v vendor
```

Update any found imports to use new `fs` package.

**Verification:**
```bash
go build ./...
go test ./...
```

**Git:**
```bash
git add <changed files>
git commit -m "refactor: update to use commons-db/fs API"
```

---

## 6. CLAUDE.md Compliance

### Implementation Best Practices
- **BP-1 ✓**: Clarifying questions asked about persistence, API design, dependencies, architecture
- **C-1 ✓**: TDD approach - tests defined before implementation
- **C-2 ✓**: Using existing domain vocabulary (Filesystem, FilesystemRW, FileInfo)
- **C-4 ✓**: Simple, composable interfaces
- **C-7 ✓**: Reuse existing connection Client() methods
- **C-8 ✓**: No file > 500 lines, functions < 50 lines
- **C-13 ✓**: No duplicate code - inline small helpers, reuse existing clients

### Testing
- **T-1 ✓**: Unit tests colocated in `*_test.go` files
- **T-3 ✓**: Pure filesystem logic separated from connection logic
- **T-4 ✓**: Integration tests preferred over heavy mocking

### Git Workflow
- **GH-1 ✓**: Conventional Commits format
- **GH-2 ✓**: Specific files added, not `git add .`
- **GH-2 ✓**: Git tags at compilation/test milestones
- **GH-2 ✓**: Short commit messages (1-2 lines)

---

## 7. Test Cases (Defined Before Implementation)

### Unit Tests

**Test: Filesystem Interface Compliance**
```go
func TestS3FS_ImplementsFilesystemRW(t *testing.T) {
    // Given: aws.Config
    // When: NewS3FS creates instance
    // Then: Type assertion to FilesystemRW succeeds
}
```

**Test: Connection Filesystem Factory**
```go
func TestS3Connection_Filesystem(t *testing.T) {
    // Given: Valid S3Connection with credentials
    // When: Call Filesystem()
    // Then: Returns FilesystemRW without error
    // And: Can perform Read/Write operations
}
```

**Test: GetFilesystemForConnection Helper**
```go
func TestGetFilesystemForConnection_AllTypes(t *testing.T) {
    testCases := []struct{
        name string
        conn models.Connection
        expectType string
    }{
        {"S3", models.Connection{Type: models.ConnectionTypeS3}, "S3FS"},
        {"GCS", models.Connection{Type: models.ConnectionTypeGCS}, "GCSFS"},
        // ... etc
    }

    // For each connection type
    // When: Call GetFilesystemForConnection()
    // Then: Returns correct filesystem implementation
}
```

### Integration Tests

**Test: S3 Write and Read**
```go
func TestS3FS_WriteAndRead(t *testing.T) {
    // Given: S3 connection to test bucket
    // And: Test data "hello world"
    // When: Write to "test/file.txt"
    // And: Read from same path
    // Then: Read data equals written data
}
```

**Test: GCS Directory Listing**
```go
func TestGCSFS_ReadDir(t *testing.T) {
    // Given: GCS bucket with known files
    // When: ReadDir on directory
    // Then: Returns FileInfo for all files
    // And: FullPath() returns complete paths
}
```

**Test: Local Filesystem Operations**
```go
func TestLocalFS_AllOperations(t *testing.T) {
    // Given: Temporary directory
    // When: Write, ReadDir, Stat, Read
    // Then: All operations succeed with correct data
}
```

### Edge Cases

**Test: Missing Credentials**
```go
func TestS3Connection_Filesystem_NoCredentials(t *testing.T) {
    // Given: S3Connection with empty credentials
    // When: Call Filesystem()
    // Then: Returns error
}
```

**Test: Invalid Path**
```go
func TestLocalFS_Read_NonExistentPath(t *testing.T) {
    // Given: Local filesystem
    // When: Read non-existent path
    // Then: Returns os.ErrNotExist
}
```

**Test: Concurrent Access**
```go
func TestFilesystemRW_ConcurrentWrites(t *testing.T) {
    // Given: Filesystem instance
    // When: 10 goroutines write different files
    // Then: All writes succeed without data corruption
}
```

---

## 8. Risk Analysis

### High Risk
- **Breaking existing consumers:** Mitigation via `GetFilesystemForConnection` helper
- **Dependency conflicts:** Verify no circular dependencies

### Medium Risk
- **Test coverage gaps:** Port all existing artifacts tests
- **Performance regression:** Benchmark before/after

### Low Risk
- **File size violations:** Monitor during implementation, refactor if needed
- **Client code duplication:** Reuse existing connection.Client() methods

---

## 9. Rollback Plan

If migration fails:

```bash
git checkout main
git branch -D feat/migrate-artifacts
# Keep artifacts as external dependency
```

---

## 10. Success Metrics

- [ ] All 5 storage backends operational
- [ ] 100% of artifacts tests ported and passing
- [ ] Zero duty dependencies in commons-db
- [ ] `make lint` and `make build` pass
- [ ] No files > 500 lines
- [ ] No functions > 50 lines
- [ ] Integration tests for all connection types

---

## 11. Post-Migration Tasks

After successful merge:

1. Archive `flanksource/artifacts` repository
2. Update README with migration notice
3. Create migration guide for external consumers
4. Deprecate artifacts module in go.mod

---

## 12. Revision History

| Version | Date       | Author | Changes                              |
|---------|------------|--------|--------------------------------------|
| 1.0     | 2025-10-15 | Claude | Initial specification                |

---

## 13. Living Document Notes

Design pivots during implementation will be documented here:

- **Pivot Date:** _TBD_
- **Reason:** _TBD_
- **Impact:** _TBD_
- **Related Commits:** _TBD_

---

## Appendix A: File Mapping

| Source (artifacts)              | Destination (commons-db)    | Action              |
|---------------------------------|-----------------------------|---------------------|
| fs/fs.go                        | fs/fs.go                    | Copy & adapt        |
| fs/s3.go                        | fs/s3.go                    | Copy & adapt        |
| clients/aws/fileinfo.go         | fs/s3.go (inline)           | Inline              |
| fs/gcs.go                       | fs/gcs.go                   | Copy & adapt        |
| clients/gcp/fileinfo.go         | fs/gcs.go (inline)          | Inline              |
| fs/ssh.go + sftp_dirfs.go       | fs/sftp.go                  | Merge & adapt       |
| clients/sftp/sftp.go            | fs/sftp.go (inline)         | Inline              |
| fs/smb.go                       | fs/smb.go                   | Copy & adapt        |
| clients/smb/smb.go              | fs/smb.go (inline)          | Inline              |
| fs/local.go                     | fs/local.go                 | Copy & adapt        |
| connection_filesystem.go        | connection/helpers.go       | Adapt & simplify    |
| clients/aws/session.go          | _N/A_                       | **SKIP** (duplicate)|
| artifacts.go (SaveArtifact)     | _N/A_                       | **REMOVE**          |

---

## Appendix B: Dependency Changes

### Before
```
commons-db → artifacts → duty
```

### After
```
commons-db (self-contained)
  └── fs/ (new package)
```

### Removed Dependencies
- `github.com/flanksource/artifacts`
- `github.com/flanksource/duty` (indirect)

### Reused Commons-DB Components
- `connection.AWSConnection.Client()` → Returns `aws.Config`
- `connection.GCSConnection.Client()` → Returns `*gcs.Client`
- `types.EnvVar`
- `types.Authentication`
- `context.Context`

---

**End of Specification**
