package projections

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/types"
	"github.com/ohler55/ojg/jp"
)

type ProjectionQuery struct {
	Exec *ProjectionExecQuery `json:"exec,omitempty" yaml:"exec,omitempty"`
	HTTP *ProjectionHTTPQuery `json:"http,omitempty" yaml:"http,omitempty"`
}

type ProjectionExecQuery struct {
	Script         string                     `json:"script" yaml:"script"`
	Mode           string                     `json:"mode,omitempty" yaml:"mode,omitempty"`
	Connections    connection.ExecConnections `json:"connections,omitempty" yaml:"connections,omitempty"`
	Env            []types.EnvVar             `json:"env,omitempty" yaml:"env,omitempty"`
	PassthroughEnv []string                   `json:"passthroughEnv,omitempty" yaml:"passthroughEnv,omitempty"`
	Select         string                     `json:"select,omitempty" yaml:"select,omitempty"`
	Timeout        time.Duration              `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

type ProjectionHTTPQuery struct {
	connection.HTTPConnection `json:",inline" yaml:",inline"`
	Method                    string                 `json:"method,omitempty" yaml:"method,omitempty"`
	Select                    string                 `json:"select,omitempty" yaml:"select,omitempty"`
	Accepted                  ProjectionHTTPAccepted `json:"accepted,omitempty" yaml:"accepted,omitempty"`
}

type ProjectionHTTPAccepted struct {
	Attempts int           `json:"attempts,omitempty" yaml:"attempts,omitempty"`
	Wait     time.Duration `json:"wait,omitempty" yaml:"wait,omitempty"`
}

func (q ProjectionExecQuery) validate() error {
	if strings.TrimSpace(q.Script) == "" {
		return fmt.Errorf("spec.source.query.exec.script is required")
	}
	if q.Mode != "" && q.Mode != "once" && q.Mode != "perTarget" {
		return fmt.Errorf("spec.source.query.exec.mode must be once or perTarget")
	}
	if q.Timeout < 0 {
		return fmt.Errorf("spec.source.query.exec.timeout must not be negative")
	}
	if q.Connections.FromConfigItem != nil {
		return fmt.Errorf("spec.source.query.exec.connections.fromConfigItem is not supported")
	}
	if _, err := parseExecTemplate(q.Script); err != nil {
		return fmt.Errorf("spec.source.query.exec.script: %w", err)
	}
	return validateSourceSelect(q.Select)
}

func (q ProjectionHTTPQuery) validate() error {
	if strings.TrimSpace(q.URL) == "" {
		return fmt.Errorf("spec.source.query.http.url is required")
	}
	if _, err := parseProjectionURLTemplate(q.URL); err != nil {
		return fmt.Errorf("spec.source.query.http.url: %w", err)
	}
	if q.Accepted.Attempts < 0 || q.Accepted.Wait < 0 {
		return fmt.Errorf("spec.source.query.http.accepted attempts and wait must not be negative")
	}
	return validateSourceSelect(q.Select)
}

func validateSourceSelect(selector string) error {
	if selector != "" {
		if _, err := jp.ParseString(selector); err != nil {
			return fmt.Errorf("source select: %w", err)
		}
	}
	return nil
}

func (p Projection) validate() error {
	if p.APIVersion != projectionAPIVersion {
		return fmt.Errorf("apiVersion must be %q", projectionAPIVersion)
	}
	if p.Kind != projectionKind {
		return fmt.Errorf("kind must be %q", projectionKind)
	}
	if strings.TrimSpace(p.Metadata.Name) == "" {
		return fmt.Errorf("metadata.name is required")
	}
	query := p.Spec.Source.Query
	if (query.Exec == nil) == (query.HTTP == nil) {
		return fmt.Errorf("spec.source.query must contain exactly one of exec or http")
	}
	if query.Exec != nil {
		if err := query.Exec.validate(); err != nil {
			return err
		}
	}
	if query.HTTP != nil {
		if err := query.HTTP.validate(); err != nil {
			return err
		}
	}
	if p.Spec.Target == nil {
		if query.HTTP != nil || query.Exec != nil && query.Exec.Mode == "perTarget" {
			return fmt.Errorf("per-target sources require spec.target")
		}
		if len(p.Spec.Match) != 0 || len(p.Spec.Set) != 0 {
			return fmt.Errorf("spec.match and spec.set require spec.target")
		}
		return nil
	}
	if p.Spec.Target.Path == "" || p.Spec.Target.Select == "" {
		return fmt.Errorf("spec.target.path and spec.target.select are required")
	}
	if !strings.HasSuffix(p.Spec.Target.Select, "[*]") {
		return fmt.Errorf("spec.target.select must end in [*]")
	}
	if len(p.Spec.Match) == 0 {
		return fmt.Errorf("spec.match must contain at least one CEL expression")
	}
	if len(p.Spec.Set) == 0 {
		return fmt.Errorf("spec.set must contain at least one JSONPath key")
	}
	for path, mapping := range p.Spec.Set {
		if !strings.HasPrefix(path, "$.") {
			return fmt.Errorf("spec.set key %q must be a JSONPath relative to the selected target", path)
		}
		if strings.TrimSpace(mapping.Value) == "" {
			return fmt.Errorf("spec.set[%q].value is required", path)
		}
		switch mapping.Strategy {
		case "", "replace", "mergeUnique":
			if mapping.Match != "" {
				return fmt.Errorf("spec.set[%q].match is only valid with strategy replaceMatching", path)
			}
		case "replaceMatching":
			if mapping.Match == "" {
				return fmt.Errorf("spec.set[%q].match is required with strategy replaceMatching", path)
			}
		default:
			return fmt.Errorf("spec.set[%q].strategy must be replace, mergeUnique, or replaceMatching", path)
		}
		if p.Spec.Target.Aggregate && (mapping.Strategy == "" || mapping.Strategy == "replace") && strings.TrimSpace(mapping.When) == "" {
			return fmt.Errorf("spec.set[%q] must use strategy mergeUnique or replaceMatching, or guard its scalar write with when, because spec.target.aggregate folds many sources into one target", path)
		}
	}
	return nil
}
