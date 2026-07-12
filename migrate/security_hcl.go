package migrate

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
)

type roleSpec struct {
	Name        string
	External    bool
	Login       bool
	Inherit     bool
	Superuser   bool
	CreateDB    bool
	CreateRole  bool
	Replication bool
	BypassRLS   bool
	ConnLimit   int64
	Comment     string
	MemberOf    []string
}

type permissionSpec struct {
	Grantee    string   `json:"grantee"`
	Target     string   `json:"target"`
	Privileges []string `json:"privileges"`
	Grantable  bool     `json:"grantable"`
}

type securitySpec struct {
	Roles       []roleSpec
	Permissions []permissionSpec
}

type parsedHCLFile struct {
	name string
	data []byte
	body *hclsyntax.Body
}

func loadHCL(schemaFS fs.FS, dir string, input map[string]cty.Value) (*hclparse.Parser, securitySpec, error) {
	var files []parsedHCLFile
	err := fs.WalkDir(schemaFS, dir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.ToLower(path.Ext(name)) != ".hcl" {
			return nil
		}
		data, err := fs.ReadFile(schemaFS, name)
		if err != nil {
			return fmt.Errorf("read HCL %s: %w", name, err)
		}
		file, diagnostics := hclsyntax.ParseConfig(data, name, hcl.Pos{Line: 1, Column: 1})
		if diagnostics.HasErrors() {
			return diagnostics
		}
		files = append(files, parsedHCLFile{name: name, data: data, body: file.Body.(*hclsyntax.Body)})
		return nil
	})
	if err != nil {
		return nil, securitySpec{}, fmt.Errorf("load HCL schemas: %w", err)
	}
	if len(files) == 0 {
		return nil, securitySpec{}, fmt.Errorf("no HCL schema files found in %q", dir)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	ctx, err := securityEvalContext(files, input)
	if err != nil {
		return nil, securitySpec{}, err
	}
	security, err := decodeSecurity(files, ctx)
	if err != nil {
		return nil, securitySpec{}, err
	}

	parser := hclparse.NewParser()
	for _, file := range files {
		filtered := append([]byte(nil), file.data...)
		for _, block := range file.body.Blocks {
			if block.Type != "role" && block.Type != "permission" {
				continue
			}
			r := block.Range()
			for i := r.Start.Byte; i < r.End.Byte && i < len(filtered); i++ {
				if filtered[i] != '\n' && filtered[i] != '\r' {
					filtered[i] = ' '
				}
			}
		}
		if _, diagnostics := parser.ParseHCL(filtered, file.name); diagnostics.HasErrors() {
			return nil, securitySpec{}, diagnostics
		}
	}
	return parser, security, nil
}

func securityEvalContext(files []parsedHCLFile, input map[string]cty.Value) (*hcl.EvalContext, error) {
	schemas := map[string]cty.Value{}
	tables := map[string]cty.Value{}
	qualifiedTables := map[string]map[string]cty.Value{}
	sequences := map[string]cty.Value{}
	qualifiedSequences := map[string]map[string]cty.Value{}
	roles := map[string]cty.Value{}
	for _, file := range files {
		for _, block := range file.body.Blocks {
			if len(block.Labels) == 0 {
				continue
			}
			name := block.Labels[len(block.Labels)-1]
			qualifier := ""
			if len(block.Labels) > 1 {
				qualifier = block.Labels[0]
			}
			switch block.Type {
			case "schema":
				schemas[name] = refValue("schema:" + name)
			case "role":
				roles[name] = refValue("role:" + name)
			case "table":
				schemaName := blockSchema(block, "public")
				columns := map[string]cty.Value{}
				for _, child := range block.Body.Blocks {
					if child.Type == "column" && len(child.Labels) > 0 {
						columns[child.Labels[0]] = refValue("column:" + schemaName + "." + name + "." + child.Labels[0])
					}
				}
				table := cty.ObjectVal(map[string]cty.Value{
					"__ref":  cty.StringVal("table:" + schemaName + "." + name),
					"column": objectValue(columns),
				})
				if qualifier == "" {
					tables[name] = table
				} else {
					if qualifiedTables[qualifier] == nil {
						qualifiedTables[qualifier] = map[string]cty.Value{}
					}
					qualifiedTables[qualifier][name] = table
				}
			case "sequence":
				schemaName := blockSchema(block, "public")
				sequence := refValue("sequence:" + schemaName + "." + name)
				if qualifier == "" {
					sequences[name] = sequence
				} else {
					if qualifiedSequences[qualifier] == nil {
						qualifiedSequences[qualifier] = map[string]cty.Value{}
					}
					qualifiedSequences[qualifier][name] = sequence
				}
			}
		}
	}
	for qualifier, values := range qualifiedTables {
		tables[qualifier] = objectValue(values)
	}
	for qualifier, values := range qualifiedSequences {
		sequences[qualifier] = objectValue(values)
	}
	vars := map[string]cty.Value{
		"schema":     objectValue(schemas),
		"table":      objectValue(tables),
		"sequence":   objectValue(sequences),
		"role":       objectValue(roles),
		"var":        objectValue(input),
		"PUBLIC":     cty.StringVal("PUBLIC"),
		"ALL":        cty.StringVal("ALL"),
		"SELECT":     cty.StringVal("SELECT"),
		"INSERT":     cty.StringVal("INSERT"),
		"UPDATE":     cty.StringVal("UPDATE"),
		"DELETE":     cty.StringVal("DELETE"),
		"TRUNCATE":   cty.StringVal("TRUNCATE"),
		"REFERENCES": cty.StringVal("REFERENCES"),
		"TRIGGER":    cty.StringVal("TRIGGER"),
		"CREATE":     cty.StringVal("CREATE"),
		"USAGE":      cty.StringVal("USAGE"),
		"MAINTAIN":   cty.StringVal("MAINTAIN"),
	}
	return &hcl.EvalContext{Variables: vars}, nil
}

func blockSchema(block *hclsyntax.Block, fallback string) string {
	attr, ok := block.Body.Attributes["schema"]
	if !ok {
		return fallback
	}
	traversal, diagnostics := hcl.AbsTraversalForExpr(attr.Expr)
	if diagnostics.HasErrors() || len(traversal) < 2 {
		return fallback
	}
	if root, ok := traversal[0].(hcl.TraverseRoot); !ok || root.Name != "schema" {
		return fallback
	}
	if step, ok := traversal[1].(hcl.TraverseAttr); ok {
		return step.Name
	}
	return fallback
}

func decodeSecurity(files []parsedHCLFile, ctx *hcl.EvalContext) (securitySpec, error) {
	var out securitySpec
	seenRoles := map[string]bool{}
	for _, file := range files {
		for _, block := range file.body.Blocks {
			switch block.Type {
			case "role":
				role, err := decodeRole(block, ctx)
				if err != nil {
					return out, fmt.Errorf("%s: %w", file.name, err)
				}
				if seenRoles[role.Name] {
					return out, fmt.Errorf("duplicate role %q", role.Name)
				}
				seenRoles[role.Name] = true
				out.Roles = append(out.Roles, role)
			case "permission":
				permissions, err := decodePermission(block, ctx)
				if err != nil {
					return out, fmt.Errorf("%s: %w", file.name, err)
				}
				out.Permissions = append(out.Permissions, permissions...)
			}
		}
	}
	return out, nil
}

func decodeRole(block *hclsyntax.Block, ctx *hcl.EvalContext) (roleSpec, error) {
	if len(block.Labels) != 1 {
		return roleSpec{}, fmt.Errorf("role block requires exactly one label")
	}
	r := roleSpec{Name: block.Labels[0], Inherit: true, ConnLimit: -1}
	allowed := map[string]bool{"external": true, "login": true, "inherit": true, "superuser": true, "create_db": true, "create_role": true, "replication": true, "bypass_rls": true, "conn_limit": true, "comment": true, "member_of": true}
	for name, attr := range block.Body.Attributes {
		if !allowed[name] {
			return r, fmt.Errorf("role %q has unsupported attribute %q", r.Name, name)
		}
		value, diagnostics := attr.Expr.Value(ctx)
		if diagnostics.HasErrors() {
			return r, diagnostics
		}
		var err error
		switch name {
		case "external":
			r.External, err = boolValue(value)
		case "login":
			r.Login, err = boolValue(value)
		case "inherit":
			r.Inherit, err = boolValue(value)
		case "superuser":
			r.Superuser, err = boolValue(value)
		case "create_db":
			r.CreateDB, err = boolValue(value)
		case "create_role":
			r.CreateRole, err = boolValue(value)
		case "replication":
			r.Replication, err = boolValue(value)
		case "bypass_rls":
			r.BypassRLS, err = boolValue(value)
		case "conn_limit":
			r.ConnLimit, err = intValue(value)
		case "comment":
			r.Comment, err = stringValue(value)
		case "member_of":
			r.MemberOf, err = refList(value, "role:")
		}
		if err != nil {
			return r, fmt.Errorf("role %q attribute %s: %w", r.Name, name, err)
		}
	}
	return r, nil
}

func decodePermission(block *hclsyntax.Block, ctx *hcl.EvalContext) ([]permissionSpec, error) {
	allowed := map[string]bool{"to": true, "for": true, "privileges": true, "grantable": true, "for_each": true}
	for name := range block.Body.Attributes {
		if !allowed[name] {
			return nil, fmt.Errorf("permission has unsupported attribute %q", name)
		}
	}
	for _, required := range []string{"to", "for", "privileges"} {
		if _, ok := block.Body.Attributes[required]; !ok {
			return nil, fmt.Errorf("permission is missing %q", required)
		}
	}
	contexts := []*hcl.EvalContext{ctx}
	if attr, ok := block.Body.Attributes["for_each"]; ok {
		value, diagnostics := attr.Expr.Value(ctx)
		if diagnostics.HasErrors() {
			return nil, diagnostics
		}
		contexts = nil
		it := value.ElementIterator()
		for it.Next() {
			_, item := it.Element()
			child := ctx.NewChild()
			child.Variables = map[string]cty.Value{"each": cty.ObjectVal(map[string]cty.Value{"value": item})}
			contexts = append(contexts, child)
		}
	}
	var out []permissionSpec
	for _, evalCtx := range contexts {
		to, diagnostics := block.Body.Attributes["to"].Expr.Value(evalCtx)
		if diagnostics.HasErrors() {
			return nil, diagnostics
		}
		grantee, err := reference(to)
		if err != nil {
			return nil, fmt.Errorf("permission to: %w", err)
		}
		grantee = strings.TrimPrefix(grantee, "role:")
		targetValue, diagnostics := block.Body.Attributes["for"].Expr.Value(evalCtx)
		if diagnostics.HasErrors() {
			return nil, diagnostics
		}
		target, err := reference(targetValue)
		if err != nil {
			return nil, fmt.Errorf("permission for: %w", err)
		}
		privValue, diagnostics := block.Body.Attributes["privileges"].Expr.Value(evalCtx)
		if diagnostics.HasErrors() {
			return nil, diagnostics
		}
		privileges, err := stringList(privValue)
		if err != nil {
			return nil, fmt.Errorf("permission privileges: %w", err)
		}
		grantable := false
		if attr, ok := block.Body.Attributes["grantable"]; ok {
			value, diagnostics := attr.Expr.Value(evalCtx)
			if diagnostics.HasErrors() {
				return nil, diagnostics
			}
			grantable, err = boolValue(value)
			if err != nil {
				return nil, fmt.Errorf("permission grantable: %w", err)
			}
		}
		p := permissionSpec{Grantee: grantee, Target: target, Privileges: privileges, Grantable: grantable}
		if err := validatePermission(p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func refValue(ref string) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{"__ref": cty.StringVal(ref)})
}

func objectValue(values map[string]cty.Value) cty.Value {
	if len(values) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(values)
}

func reference(value cty.Value) (string, error) {
	if value.Type() == cty.String {
		return value.AsString(), nil
	}
	if value.IsNull() || !value.IsKnown() || !value.Type().IsObjectType() {
		return "", fmt.Errorf("expected an object reference or string")
	}
	ref := value.GetAttr("__ref")
	if ref.Type() != cty.String {
		return "", fmt.Errorf("invalid object reference")
	}
	return ref.AsString(), nil
}

func refList(value cty.Value, prefix string) ([]string, error) {
	var out []string
	it := value.ElementIterator()
	for it.Next() {
		_, item := it.Element()
		ref, err := reference(item)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(ref, prefix) {
			return nil, fmt.Errorf("expected %s reference, got %q", prefix, ref)
		}
		out = append(out, strings.TrimPrefix(ref, prefix))
	}
	return out, nil
}

func stringList(value cty.Value) ([]string, error) {
	var out []string
	it := value.ElementIterator()
	for it.Next() {
		_, item := it.Element()
		v, err := stringValue(item)
		if err != nil {
			return nil, err
		}
		out = append(out, strings.ToUpper(v))
	}
	return out, nil
}

func stringValue(value cty.Value) (string, error) {
	if value.Type() != cty.String || value.IsNull() || !value.IsKnown() {
		return "", fmt.Errorf("expected string")
	}
	return value.AsString(), nil
}

func boolValue(value cty.Value) (bool, error) {
	if value.Type() != cty.Bool || value.IsNull() || !value.IsKnown() {
		return false, fmt.Errorf("expected boolean")
	}
	return value.True(), nil
}

func intValue(value cty.Value) (int64, error) {
	if value.Type() != cty.Number || value.IsNull() || !value.IsKnown() {
		return 0, fmt.Errorf("expected number")
	}
	var result int64
	if err := gocty.FromCtyValue(value, &result); err != nil {
		return 0, err
	}
	return result, nil
}
