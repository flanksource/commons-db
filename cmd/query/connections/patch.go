package connections

import (
	"encoding/json"
	"fmt"
	"github.com/flanksource/commons-db/models"
)

func mergeConnectionPatch(existing *models.Connection, patch map[string]any) (map[string]any, error) {
	data, err := json.Marshal(existing)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err = json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	for key, value := range patch {
		if key == "password" || key == "certificate" {
			if value == "" {
				continue
			}
		}
		if key == "properties" {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			var props map[string]string
			if err = json.Unmarshal(encoded, &props); err != nil {
				return nil, fmt.Errorf("invalid properties")
			}
			previous, _ := body[key].(map[string]any)
			if previous == nil {
				previous = map[string]any{}
			}
			for name, v := range props {
				previous[name] = v
			}
			body[key] = previous
		} else {
			body[key] = value
		}
	}
	return body, nil
}
