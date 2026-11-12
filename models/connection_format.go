package models

import (
	"fmt"
	"log/slog"

	"github.com/flanksource/clicky/text"
)

func (c Connection) logFields() map[string]any {
	properties := make(map[string]text.SensitiveString, len(c.Properties))
	for key, value := range c.Properties {
		properties[key] = text.NewSensitiveString(value)
	}
	return map[string]any{
		"id": c.ID, "name": c.Name, "namespace": c.Namespace, "type": c.Type,
		"url": text.NewSensitiveString(c.URL), "username": text.NewSensitiveString(c.Username),
		"password": text.NewSensitiveString(c.Password), "certificate": text.NewSensitiveString(c.Certificate),
		"source": text.NewSensitiveString(c.Source), "properties": properties,
	}
}

// Format protects diagnostic formatting without changing connection wire serialization.
func (c Connection) Format(state fmt.State, _ rune) { fmt.Fprint(state, c.logFields()) }

func (c Connection) LogValue() slog.Value { return slog.AnyValue(c.logFields()) }
