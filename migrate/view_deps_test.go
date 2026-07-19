package migrate

import (
	"testing"

	"ariga.io/atlas/sql/schema"
	"github.com/stretchr/testify/assert"
)

func postScript(path, content string) *script {
	return &script{path: path, content: content, phase: phasePost}
}

func TestManagedViews(t *testing.T) {
	cases := []struct {
		name    string
		scripts map[string]*script
		want    map[string]string
	}{
		{
			name:    "create or replace view is attributed with its schema",
			scripts: map[string]*script{"v.sql": postScript("v.sql", "CREATE OR REPLACE VIEW public.overview AS SELECT 1")},
			want:    map[string]string{"public.overview": "v.sql"},
		},
		{
			name:    "unqualified view defaults to the public schema",
			scripts: map[string]*script{"v.sql": postScript("v.sql", "CREATE VIEW overview AS SELECT 1")},
			want:    map[string]string{"public.overview": "v.sql"},
		},
		{
			name:    "materialized view is attributed",
			scripts: map[string]*script{"m.sql": postScript("m.sql", "CREATE MATERIALIZED VIEW reports.daily AS SELECT 1")},
			want:    map[string]string{"reports.daily": "m.sql"},
		},
		{
			name: "views alongside plpgsql functions and triggers are extracted without error",
			scripts: map[string]*script{"mixed.sql": postScript("mixed.sql", `
CREATE OR REPLACE FUNCTION public.touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RETURN NEW; -- CREATE VIEW inside a body must not be picked up
END;
$$;
CREATE OR REPLACE VIEW public.transcript AS SELECT 1;
CREATE TRIGGER touch_trg BEFORE INSERT ON public.messages FOR EACH ROW EXECUTE FUNCTION public.touch();`)},
			want: map[string]string{"public.transcript": "mixed.sql"},
		},
		{
			name: "pre-phase scripts are ignored",
			scripts: map[string]*script{
				"pre.sql": {path: "pre.sql", content: "CREATE VIEW public.early AS SELECT 1", phase: phasePre},
			},
			want: map[string]string{},
		},
		{
			name:    "unparseable scripts are skipped rather than fatal",
			scripts: map[string]*script{"bad.sql": postScript("bad.sql", "CREATE VIEW public.broken AS SELECT FROM WHERE ;;")},
			want:    map[string]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, managedViews(tc.scripts))
		})
	}
}

func modifyTable(name string, changes ...schema.Change) *schema.ModifyTable {
	return &schema.ModifyTable{T: &schema.Table{Name: name, Schema: &schema.Schema{Name: "public"}}, Changes: changes}
}

func TestRiskyModifiedTables(t *testing.T) {
	col := &schema.Column{Name: "c"}
	cases := []struct {
		name    string
		changes []schema.Change
		want    []string
	}{
		{
			name:    "dropping a column is risky",
			changes: []schema.Change{modifyTable("messages", &schema.DropColumn{C: col})},
			want:    []string{"public.messages"},
		},
		{
			name:    "altering a column type is risky",
			changes: []schema.Change{modifyTable("messages", &schema.ModifyColumn{From: col, To: col})},
			want:    []string{"public.messages"},
		},
		{
			name:    "dropping a table is risky",
			changes: []schema.Change{&schema.DropTable{T: &schema.Table{Name: "messages", Schema: &schema.Schema{Name: "public"}}}},
			want:    []string{"public.messages"},
		},
		{
			name:    "adding a column alone is not risky",
			changes: []schema.Change{modifyTable("messages", &schema.AddColumn{C: col})},
			want:    nil,
		},
		{
			name: "a table is reported once despite multiple risky changes",
			changes: []schema.Change{
				modifyTable("messages", &schema.DropColumn{C: col}, &schema.ModifyColumn{From: col, To: col}),
			},
			want: []string{"public.messages"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, ref := range riskyModifiedTables(tc.changes) {
				got = append(got, ref.Qualified())
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
