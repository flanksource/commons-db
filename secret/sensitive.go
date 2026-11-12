package secret

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/clicky/text"
)

const sensitivePlaceholder = "[REDACTED]"

type Sensitive []byte

func (t Sensitive) GoString() string { return sensitivePlaceholder }

func (t Sensitive) Format(state fmt.State, verb rune) {
	text.NewSensitiveString(t.String()).Format(state, verb)
}

func (t Sensitive) String() string {
	return sensitivePlaceholder
}

func (t Sensitive) PlainText() string {
	return string(t)
}

func (t Sensitive) MarshalJSON() ([]byte, error) {
	return json.Marshal(sensitivePlaceholder)
}

func (t Sensitive) MarshalText() ([]byte, error) {
	return []byte(sensitivePlaceholder), nil
}

func (t *Sensitive) Clear() {
	*t = make([]byte, len(*t))
	for i := range *t {
		(*t)[i] = 0
	}
	*t = nil
}
