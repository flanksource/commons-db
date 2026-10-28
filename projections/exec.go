package projections

import (
	gocontext "context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/shell"
)

type projectionSourceResult struct {
	Items    []map[string]any
	Context  map[string]any
	Warnings []ProjectionWarning
}

func parseExecTemplate(script string) (*template.Template, error) {
	interpreter, args := shell.DetectInterpreterFromShebang(script)
	if filepath.Base(interpreter) == "env" && len(args) > 0 {
		interpreter = args[0]
	}
	quote := func(value string) (string, error) {
		switch strings.TrimSuffix(filepath.Base(interpreter), ".exe") {
		case "bash", "sh", "zsh", "dash", "ksh":
			return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'", nil
		case "pwsh", "powershell":
			return "'" + strings.ReplaceAll(value, "'", "''") + "'", nil
		default:
			return "", fmt.Errorf("shellQuote does not support interpreter %q", interpreter)
		}
	}
	return template.New("script").Option("missingkey=error").Funcs(template.FuncMap{"shellQuote": quote}).Parse(script)
}

func queryExecProjection(ctx context.Context, projection Projection, values map[string]any) ([]map[string]any, error) {
	query := projection.Spec.Source.Query.Exec
	scripts, entries, err := projectionScripts(projection, values)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0)
	for i, script := range scripts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		payload, err := runProjectionScript(ctx, projection, script)
		if err != nil {
			return nil, fmt.Errorf("exec invocation %d: %w", i+1, err)
		}
		payload, err = selectPayload(payload, query.Select)
		if err != nil {
			return nil, err
		}
		if query.Mode == "perTarget" {
			rows = append(rows, projectionTargetRow(entries[i], payload))
		} else {
			normalized, err := projectionRows(payload)
			if err != nil {
				return nil, err
			}
			rows = append(rows, normalized...)
		}
	}
	return rows, nil
}

func projectionScripts(p Projection, values map[string]any) ([]string, []map[string]any, error) {
	query := p.Spec.Source.Query.Exec
	compiled, err := parseExecTemplate(query.Script)
	if err != nil {
		return nil, nil, err
	}
	entries := []map[string]any{nil}
	if query.Mode == "perTarget" {
		entries, err = projectionEntries(p, values)
		if err != nil {
			return nil, nil, err
		}
	}
	scripts := make([]string, 0, len(entries))
	for _, entry := range entries {
		activation := map[string]any{"context": values}
		if query.Mode == "perTarget" {
			activation["target"] = entry
		}
		var script strings.Builder
		if err := compiled.Execute(&script, activation); err != nil {
			return nil, nil, fmt.Errorf("exec script template: %w", err)
		}
		scripts = append(scripts, script.String())
	}
	return scripts, entries, nil
}

func runProjectionScript(ctx context.Context, p Projection, script string) (payload any, resultErr error) {
	query := p.Spec.Source.Query.Exec
	timeout := query.Timeout
	if timeout == 0 {
		timeout = time.Minute
	}
	deadline, cancel := gocontext.WithTimeout(ctx, timeout)
	defer cancel()
	cwd, err := filepath.Abs(filepath.Dir(p.manifest))
	if err != nil {
		return nil, err
	}
	base := filepath.Join(cwd, ".tmp")
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(base, "projection-")
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(dir)) }()
	var options shell.Exec
	// Hydration mutates connection structs; each invocation owns its copy.
	encoded, err := json.Marshal(query.Connections)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(encoded, &options.Connections); err != nil {
		return nil, err
	}
	options.Script, options.Chroot, options.BaseDir, options.CredentialsDir = script, cwd, dir, dir
	options.EnvVars, options.PassthroughEnv = query.Env, query.PassthroughEnv
	options.DisplayPath, options.DisplayArgs = "projection", []string{p.Metadata.Name}
	result, err := shell.Run(ctx.Wrap(deadline), options)
	if err != nil {
		if result != nil && result.Stderr != "" {
			return nil, fmt.Errorf("%w: %s", err, result.Stderr)
		}
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("exec stdout must be JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("exec stdout must contain exactly one JSON value")
	}
	return payload, nil
}

func projectionRows(payload any) ([]map[string]any, error) {
	switch value := payload.(type) {
	case map[string]any:
		return []map[string]any{value}, nil
	case []any:
		rows := make([]map[string]any, 0, len(value))
		for i, item := range value {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("exec row %d is %T, expected JSON object", i, item)
			}
			rows = append(rows, row)
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("exec output is %T, expected JSON object or array of objects", payload)
	}
}
