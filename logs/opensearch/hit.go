package opensearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func (hit *SearchHit) UnmarshalJSON(data []byte) error {
	type wire SearchHit
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode((*wire)(hit)); err != nil {
		return err
	}
	for _, value := range []any{hit.Source, hit.Fields, hit.Sort} {
		if _, err := typedHitValue(value); err != nil {
			return err
		}
	}
	return nil
}

func typedHitValue(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return integer, nil
		}
		if integer, err := strconv.ParseUint(value.String(), 10, 64); err == nil {
			return integer, nil
		}
		if !strings.ContainsAny(value.String(), ".eE") {
			return nil, fmt.Errorf("OpenSearch integer %s exceeds 64 bits", value)
		}
		return value.Float64()
	case map[string]any:
		for key, child := range value {
			converted, err := typedHitValue(child)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			value[key] = converted
		}
	case []any:
		for index, child := range value {
			converted, err := typedHitValue(child)
			if err != nil {
				return nil, err
			}
			value[index] = converted
		}
	}
	return value, nil
}
