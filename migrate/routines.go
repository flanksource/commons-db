package migrate

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

// WithRoutines declares stored procedures and functions from the .sql files
// under dir (relative to the schema filesystem root). Each file holds exactly
// one CREATE [OR ALTER] PROCEDURE|FUNCTION batch, optionally followed by GO,
// and is named after the routine it creates (mc_ClonePlan.sql creates
// mc_ClonePlan). The files are the routines' single source: they run as
// written, so the same file works in SSMS or sqlcmd, and they are never
// treated as hash-gated SQL scripts. Only SQL Server supports routines.
func WithRoutines(dir string) Option {
	return func(o *options) { o.routines = strings.Trim(path.Clean(dir), "/") }
}

// refuseRoutines rejects WithRoutines on a dialect that does not support it.
func refuseRoutines(cfg options, dialect string) error {
	if cfg.routines != "" {
		return fmt.Errorf("%s migrations do not support routines (WithRoutines); only SQL Server does", dialect)
	}
	return nil
}

// loadRoutines parses every .sql file under dir into the routine it creates.
func loadRoutines(schemaFS fs.FS, dir string) ([]*planner.Routine, error) {
	var routines []*planner.Routine
	seen := map[string]string{}
	err := fs.WalkDir(schemaFS, dir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(path.Ext(name), ".sql") {
			return nil
		}
		routine, err := loadRoutine(schemaFS, name)
		if err != nil {
			return err
		}
		key := strings.ToLower(routine.Name)
		if other, ok := seen[key]; ok {
			return fmt.Errorf("routine %s is declared by both %s and %s", routine.Name, other, name)
		}
		seen[key] = name
		routines = append(routines, routine)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load routines from %q: %w", dir, err)
	}
	if len(routines) == 0 {
		return nil, fmt.Errorf("load routines from %q: no .sql files found", dir)
	}
	return routines, nil
}

func loadRoutine(schemaFS fs.FS, name string) (*planner.Routine, error) {
	data, err := fs.ReadFile(schemaFS, name)
	if err != nil {
		return nil, fmt.Errorf("read routine %s: %w", name, err)
	}
	batches, err := sqlglot.SplitBatches(string(data), planner.Dialect)
	if err != nil {
		return nil, fmt.Errorf("routine %s: %w", name, err)
	}
	if len(batches) != 1 {
		return nil, fmt.Errorf("routine %s: expected exactly one batch, found %d", name, len(batches))
	}
	routine, err := planner.ParseRoutine(batches[0])
	if err != nil {
		return nil, fmt.Errorf("routine %s: %w", name, err)
	}
	if base := strings.TrimSuffix(path.Base(name), path.Ext(name)); routine.Name != base {
		return nil, fmt.Errorf("routine %s creates %q: the file must be named after its routine", name, routine.Name)
	}
	return routine, nil
}
