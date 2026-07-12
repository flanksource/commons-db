package migrate

import (
	"testing"
	"testing/fstest"

	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestLoadHCLSeparatesAtlasAndSecurityBlocks(t *testing.T) {
	filesystem := fstest.MapFS{"migrations/schema.hcl": &fstest.MapFile{Data: []byte(`
schema "public" {}

table "connections" {
  schema = schema.public
  column "id" { type = text }
}
table "profiles" {
  schema = schema.public
  column "id" { type = text }
}

role "base" { external = true }
role "reader" {
  comment   = "Query reader"
  member_of = [role.base]
}
permission {
  for_each   = [table.connections, table.profiles]
  for        = each.value
  to         = role.reader
  privileges = [SELECT]
}
permission {
  for        = table.connections.column.id
  to         = PUBLIC
  privileges = [SELECT]
  grantable  = true
}
`)}}
	parser, securitySpec, err := loadHCL(filesystem, "migrations", nil)
	require.NoError(t, err)
	require.Len(t, securitySpec.Roles, 2)
	assert.Equal(t, []string{"base"}, securitySpec.Roles[1].MemberOf)
	require.Len(t, securitySpec.Permissions, 3)
	assert.Equal(t, "table:public.connections", securitySpec.Permissions[0].Target)
	assert.Equal(t, "table:public.profiles", securitySpec.Permissions[1].Target)
	assert.Equal(t, "column:public.connections.id", securitySpec.Permissions[2].Target)
	assert.Equal(t, "PUBLIC", securitySpec.Permissions[2].Grantee)

	realm := &schema.Realm{}
	require.NoError(t, postgres.EvalHCL.Eval(parser, realm, nil), "filtered document must remain valid Atlas HCL")
	require.Len(t, realm.Schemas, 1)
	assert.Len(t, realm.Schemas[0].Tables, 2)
}

func TestLoadHCLUsesSharedVariables(t *testing.T) {
	filesystem := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(`
variable "role_comment" { type = string }
variable "can_grant" { type = bool }
schema "public" {}
table "items" {
  schema = schema.public
  column "id" { type = text }
}
role "reader" { comment = var.role_comment }
permission {
  for = table.items
  to = role.reader
  privileges = [SELECT]
  grantable = var.can_grant
}
`)}}
	input := map[string]cty.Value{
		"role_comment": cty.StringVal("from input"),
		"can_grant":    cty.True,
	}
	parser, securitySpec, err := loadHCL(filesystem, ".", input)
	require.NoError(t, err)
	assert.Equal(t, "from input", securitySpec.Roles[0].Comment)
	assert.True(t, securitySpec.Permissions[0].Grantable)
	realm := &schema.Realm{}
	require.NoError(t, postgres.EvalHCL.Eval(parser, realm, input))
}

func TestLoadHCLRejectsUnsupportedPermissionTarget(t *testing.T) {
	filesystem := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(`
schema "public" {}
role "reader" {}
permission {
  for = "view:public.summary"
  to = role.reader
  privileges = [SELECT]
}
`)}}
	_, _, err := loadHCL(filesystem, ".", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported permission target kind")
}
