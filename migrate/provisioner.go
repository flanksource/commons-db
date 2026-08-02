package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

const provisionerFingerprintVersion = "commons-db/migrate-provisioner/v1"

// SchemaProvisioner prepares reusable dbtest templates and reconciles each
// cloned database using the same migration behavior as Apply.
type SchemaProvisioner struct {
	schemaFS fs.FS
	config   options
}

// NewProvisioner adapts an HCL migration bundle to dbtest.Provisioner without
// coupling the migrate package to dbtest. Options are frozen at construction.
func NewProvisioner(schemaFS fs.FS, opts ...Option) *SchemaProvisioner {
	return &SchemaProvisioner{schemaFS: schemaFS, config: resolveOptions(opts)}
}

// Fingerprint returns a deterministic identity for all inputs that affect the
// database produced by this provisioner.
func (p *SchemaProvisioner) Fingerprint(ctx context.Context) (string, error) {
	if p == nil {
		return "", errors.New("migration provisioner is nil")
	}
	if p.schemaFS == nil {
		return "", errors.New("schema filesystem is nil")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := loadScripts(p.schemaFS, p.config.dir); err != nil {
		return "", err
	}
	if _, _, err := loadHCL(p.schemaFS, p.config.dir, p.config.input); err != nil {
		return "", err
	}

	digest := sha256.New()
	writeFingerprintField(digest, provisionerFingerprintVersion)
	writeFingerprintField(digest, p.config.dir)
	writeFingerprintField(digest, p.config.name)
	writeFingerprintField(digest, fmt.Sprintf("allow-table-drops=%t", p.config.allowTableDrops))
	for _, pattern := range sortedCopy(p.config.exclude) {
		writeFingerprintField(digest, "exclude="+pattern)
	}
	if err := writeFingerprintVariables(digest, p.config.input); err != nil {
		return "", err
	}
	if err := p.writeMigrationFiles(ctx, digest); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// PrepareTemplate applies the full migration bundle to a new template.
func (p *SchemaProvisioner) PrepareTemplate(ctx context.Context, connection string) error {
	return p.prepare(ctx, connection)
}

// PrepareInstance reconciles instance-specific and always-run migrations after
// cloning a prepared template.
func (p *SchemaProvisioner) PrepareInstance(ctx context.Context, connection string) error {
	if p == nil {
		return errors.New("migration provisioner is nil")
	}
	if strings.TrimSpace(connection) == "" {
		return errors.New("connection string is empty")
	}
	if p.schemaFS == nil {
		return errors.New("schema filesystem is nil")
	}
	scripts, err := loadScripts(p.schemaFS, p.config.dir)
	if err != nil {
		return err
	}
	_, security, err := loadHCL(p.schemaFS, p.config.dir, p.config.input)
	if err != nil {
		return err
	}
	database, err := sql.Open("postgres", connection)
	if err != nil {
		return fmt.Errorf("open SQL migration database: %w", err)
	}
	defer database.Close() //nolint:errcheck
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("connect SQL migration database: %w", err)
	}
	if err := ensureMetadataTables(ctx, database); err != nil {
		return err
	}
	selected, err := selectScripts(ctx, database, p.config.name, scripts)
	if err != nil {
		return err
	}
	ordered, err := topologicalScripts(scripts, selected)
	if err != nil {
		return err
	}
	if err := runScriptPhase(ctx, database, p.config.name, ordered, phasePre); err != nil {
		return err
	}
	if err := runScriptPhase(ctx, database, p.config.name, ordered, phasePost); err != nil {
		return err
	}
	if err := retryOnLockContention(ctx, "reconcile database security", func() error {
		return reconcileSecurity(ctx, database, p.config.name, security)
	}); err != nil {
		return fmt.Errorf("reconcile database security: %w", err)
	}
	return nil
}

func (p *SchemaProvisioner) prepare(ctx context.Context, connection string) error {
	if p == nil {
		return errors.New("migration provisioner is nil")
	}
	return apply(ctx, connection, p.schemaFS, p.config)
}

func (p *SchemaProvisioner) writeMigrationFiles(ctx context.Context, digest hash.Hash) error {
	type migrationFile struct {
		name string
		data []byte
	}
	var files []migrationFile
	err := fs.WalkDir(p.schemaFS, p.config.dir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		extension := strings.ToLower(path.Ext(name))
		if entry.IsDir() || (extension != ".hcl" && extension != ".sql") {
			return nil
		}
		data, err := fs.ReadFile(p.schemaFS, name)
		if err != nil {
			return fmt.Errorf("read migration fingerprint input %s: %w", name, err)
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(name, p.config.dir), "/")
		files = append(files, migrationFile{name: relative, data: data})
		return nil
	})
	if err != nil {
		return fmt.Errorf("load migration fingerprint inputs: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	for _, file := range files {
		writeFingerprintField(digest, "file="+file.name)
		writeFingerprintBytes(digest, file.data)
	}
	return nil
}

func writeFingerprintVariables(digest hash.Hash, input map[string]cty.Value) error {
	names := make([]string, 0, len(input))
	for name := range input {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := input[name]
		if value == cty.NilVal {
			return fmt.Errorf("fingerprint migration variable %q: value is nil", name)
		}
		if !value.IsWhollyKnown() {
			return fmt.Errorf("fingerprint migration variable %q: value is not wholly known", name)
		}
		typeJSON, err := ctyjson.MarshalType(value.Type())
		if err != nil {
			return fmt.Errorf("fingerprint migration variable %q type: %w", name, err)
		}
		valueJSON, err := ctyjson.Marshal(value, value.Type())
		if err != nil {
			return fmt.Errorf("fingerprint migration variable %q value: %w", name, err)
		}
		writeFingerprintField(digest, "variable="+name)
		writeFingerprintBytes(digest, typeJSON)
		writeFingerprintBytes(digest, valueJSON)
	}
	return nil
}

func writeFingerprintField(digest hash.Hash, value string) {
	writeFingerprintBytes(digest, []byte(value))
}

func writeFingerprintBytes(digest hash.Hash, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(value)
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
