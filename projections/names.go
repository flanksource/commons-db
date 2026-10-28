package projections

import "fmt"

// Names must stay unique across every loaded manifest, not just within one file:
// --name selects by name alone, and two files claiming it would make that ambiguous.
func validateProjectionNames(projections []Projection) error {
	seen := map[string]Projection{}
	for _, projection := range projections {
		if previous, ok := seen[projection.Metadata.Name]; ok {
			return fmt.Errorf("duplicate projection metadata.name %q in %s and %s",
				projection.Metadata.Name, previous.manifest, projection.manifest)
		}
		seen[projection.Metadata.Name] = projection
	}
	return nil
}
