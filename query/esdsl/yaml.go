package esdsl

import (
	"encoding/json"

	"gopkg.in/yaml.v3"
)

func (s Search) MarshalYAML() (any, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	err = json.Unmarshal(encoded, &value)
	return value, err
}
func (s *Search) UnmarshalYAML(node *yaml.Node) error {
	var value map[string]any
	if err := node.Decode(&value); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, s)
}
