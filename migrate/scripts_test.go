package migrate

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsRetryableMigrationErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"deadlock", &pq.Error{Code: "40P01"}, true},
		{"lock_timeout", &pq.Error{Code: "55P03"}, true},
		{"serialization_failure", &pq.Error{Code: "40001"}, true},
		{"wrapped deadlock", fmt.Errorf("execute SQL migration x: %w", &pq.Error{Code: "40P01"}), true},
		{"undefined_table", &pq.Error{Code: "42P01"}, false},
		{"syntax_error", &pq.Error{Code: "42601"}, false},
		{"non-pq error", errors.New("connection refused"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isRetryableMigrationErr(tc.err))
		})
	}
}

func TestLoadScriptsDefaultsToPostAndOrdersTransitively(t *testing.T) {
	filesystem := fstest.MapFS{
		"migrations/a.sql": &fstest.MapFile{Data: []byte("-- phase: pre\nSELECT 1")},
		"migrations/b.sql": &fstest.MapFile{Data: []byte("-- phase: pre\n-- dependsOn: a.sql\nSELECT 2")},
		"migrations/c.sql": &fstest.MapFile{Data: []byte("-- dependsOn: b.sql\nSELECT 3")},
	}
	scripts, err := loadScripts(filesystem, "migrations")
	require.NoError(t, err)
	assert.Equal(t, phasePre, scripts["a.sql"].phase)
	assert.Equal(t, phasePost, scripts["c.sql"].phase)

	ordered, err := topologicalScripts(scripts, nil)
	require.NoError(t, err)
	require.Len(t, ordered, 3)
	assert.Equal(t, []string{"a.sql", "b.sql", "c.sql"}, []string{ordered[0].path, ordered[1].path, ordered[2].path})
}

func TestScriptGraphValidation(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
		err   string
	}{
		{
			name:  "missing",
			files: fstest.MapFS{"migrations/a.sql": &fstest.MapFile{Data: []byte("-- dependsOn: missing.sql\nSELECT 1")}},
			err:   "depends on missing script",
		},
		{
			name: "cycle",
			files: fstest.MapFS{
				"migrations/a.sql": &fstest.MapFile{Data: []byte("-- dependsOn: b.sql\nSELECT 1")},
				"migrations/b.sql": &fstest.MapFile{Data: []byte("-- dependsOn: a.sql\nSELECT 2")},
			},
			err: "dependency cycle",
		},
		{
			name: "phase inversion",
			files: fstest.MapFS{
				"migrations/a.sql": &fstest.MapFile{Data: []byte("SELECT 1")},
				"migrations/b.sql": &fstest.MapFile{Data: []byte("-- phase: pre\n-- dependsOn: a.sql\nSELECT 2")},
			},
			err: "cannot depend on post script",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadScripts(tt.files, "migrations")
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tt.err), err.Error())
		})
	}
}

func TestParseScriptDirectives(t *testing.T) {
	s, err := parseScript("x.sql", `
-- comment
-- phase: pre
-- runs: always
-- transaction: false
-- dependsOn: a.sql, nested/b.sql

SELECT 1`)
	require.NoError(t, err)
	assert.Equal(t, phasePre, s.phase)
	assert.True(t, s.always)
	assert.False(t, s.transactional)
	assert.Equal(t, []string{"a.sql", "nested/b.sql"}, s.dependsOn)
}
