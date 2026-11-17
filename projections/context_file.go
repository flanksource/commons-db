package projections

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// reservedContextKeys are stamped by every run; a caller-supplied value would be silently replaced.
var reservedContextKeys = []string{"observed_at", "date"}

// LoadContextFile reads a YAML mapping whose keys become context.<key> in source
// templates and CEL, so one params document (an assessment window, an instance
// identity) feeds every projection of a run.
func LoadContextFile(path string) (map[string]any, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read context file: %w", err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(body, &values); err != nil {
		return nil, fmt.Errorf("context file %s must be a YAML mapping: %w", path, err)
	}
	if values == nil {
		return nil, fmt.Errorf("context file %s is empty", path)
	}
	return values, nil
}
