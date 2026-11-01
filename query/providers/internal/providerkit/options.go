package providerkit

import "github.com/flanksource/commons-db/query"

// Options are a provider's exported options, which check themselves.
type Options interface {
	Validate() error
}

// DecodeOptions decodes a request's options into T and validates them, so a
// profile's free-form options and a typed entry embedding T are read, and
// refused, the same way.
func DecodeOptions[T Options](options map[string]any) (T, error) {
	decoded, err := query.DecodeOptions[T](options)
	if err != nil {
		return decoded, err
	}
	return decoded, decoded.Validate()
}
