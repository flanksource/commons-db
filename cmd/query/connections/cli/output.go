package cli

// Redact removes credentials before a connection result crosses a process boundary.
func Redact(value any) { redact(value) }
