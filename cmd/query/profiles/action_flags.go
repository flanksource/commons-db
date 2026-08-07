package profiles

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/flanksource/clicky/flags"
)

// decodeActionFlags turns the flag map clicky hands an action back into the
// typed flags struct the action declared. Going through clicky's own parser
// keeps CSV-encoded repeatable flags and defaults behaving the same on the CLI
// and over HTTP, instead of each action re-splitting strings by hand.
func decodeActionFlags[T any](flagMap map[string]string) (T, error) {
	var decoded T
	fields, err := flags.ParseStructFields(reflect.TypeOf(decoded))
	if err != nil {
		return decoded, err
	}
	if err := flags.PopulateFromRequest(reflect.ValueOf(&decoded).Elem(), fields, flagMap, nil); err != nil {
		return decoded, err
	}
	return decoded, nil
}

// parseKeyValues parses repeatable key=value flags. Values may contain "="; only
// the first one separates.
func parseKeyValues(flag string, pairs []string) (map[string]string, error) {
	out := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if key = strings.TrimSpace(key); !ok || key == "" {
			return nil, fmt.Errorf("invalid --%s %q: expected key=value", flag, pair)
		}
		out[key] = value
	}
	return out, nil
}

// parseParamValues reads the repeatable --param flag of an action.
//
// Actions are served over HTTP as well as from the CLI — clicky populates the
// same flag map from a request body — so an @file reference is refused here
// rather than expanded: honouring it would let any caller read a file off the
// server. The command line reaches the loader through `query trace`/`query top`,
// and a large selection travels over HTTP as a POST body instead.
func parseParamValues(pairs []string) (map[string]any, error) {
	parsed, err := parseKeyValues("param", pairs)
	if err != nil {
		return nil, err
	}
	params := make(map[string]any, len(parsed))
	for key, value := range parsed {
		if strings.HasPrefix(value, "@") {
			return nil, fmt.Errorf(
				"param %q: @file values are read from the command line only; POST the values to /api/v1/profile/<name> instead", key)
		}
		params[key] = value
	}
	return params, nil
}
