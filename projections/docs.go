package projections

import _ "embed"

//go:embed README.md
var syntax string

// Syntax returns the manifest reference shared by package documentation and CLI help.
func Syntax() string { return syntax }
