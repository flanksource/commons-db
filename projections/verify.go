package projections

import (
	"fmt"
	"os"
	"strings"

	"github.com/goccy/go-yaml"
)

func verifyProjection(projection Projection) error {
	if err := projection.validate(); err != nil {
		return err
	}
	if _, err := compileProjection(projection); err != nil {
		return fmt.Errorf("projection %s: %w", projection.Metadata.Name, err)
	}
	if projection.Spec.Target == nil {
		return nil
	}
	targetPath := projection.resolvePath(projection.Spec.Target.Path)
	body, err := os.ReadFile(targetPath)
	if err != nil {
		return err
	}
	_, _, entries, err := projectionTarget(body, projection.Spec.Target.Select)
	if err != nil {
		return fmt.Errorf("projection %s target: %w", projection.Metadata.Name, err)
	}
	// An http source addresses each entry by templating its fields, so a URL naming
	// a field the register does not carry is checkable without a single request —
	// which is the whole point of verify. Left to apply time it would surface as
	// every entry failing at once, against a live API.
	if query := projection.Spec.Source.Query.HTTP; query != nil {
		compiled, err := parseProjectionURLTemplate(query.URL)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if _, err := renderProjectionURL(compiled, entry); err != nil {
				return fmt.Errorf("http URL template: %w", err)
			}
		}
	}
	entryPath := strings.TrimSuffix(projection.Spec.Target.Select, "[*]") + "[0]"
	for path := range projection.Spec.Set {
		if _, err := yaml.PathString(entryPath + strings.TrimPrefix(path, "$")); err != nil {
			return fmt.Errorf("projection %s spec.set key %q is not writable YAMLPath: %w", projection.Metadata.Name, path, projectionYAMLError(err))
		}
	}
	return validateProjectionTarget(projection, body)
}
