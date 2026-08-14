package workload

import (
	"fmt"
	"path"
	"strings"

	"github.com/flanksource/commons-db/query/grammar"
	corev1 "k8s.io/api/core/v1"
)

type Resource struct {
	Kind        string            `json:"kind"`
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	UID         string            `json:"uid"`
	Labels      map[string]string `json:"labels,omitempty"`
	pod         *corev1.Pod
	podSelector string
}

type Selector struct {
	root *grammar.QueryField
}

func ParseSelector(raw string) (Selector, error) {
	if strings.TrimSpace(raw) == "" {
		return Selector{root: &grammar.QueryField{Op: "and"}}, nil
	}
	root, err := grammar.ParsePEG(raw)
	if err != nil {
		return Selector{}, fmt.Errorf("parse Kubernetes target selector: %w", err)
	}
	if err := validate(root); err != nil {
		return Selector{}, err
	}
	return Selector{root: root}, nil
}

func (s Selector) And(other Selector) Selector {
	return Selector{root: &grammar.QueryField{
		Op:     "and",
		Fields: []*grammar.QueryField{s.root, other.root},
	}}
}

func (s Selector) Matches(resource Resource) bool {
	return match(s.root, resource)
}

func (s Selector) Filter(resources []Resource) []Resource {
	var matches []Resource
	for _, resource := range resources {
		if s.Matches(resource) {
			matches = append(matches, resource)
		}
	}
	return matches
}

func (s Selector) Exact() bool {
	if s.root == nil {
		return false
	}
	values := map[string]string{}
	if !collectExact(s.root, values) {
		return false
	}
	if values["uid"] != "" {
		return true
	}
	return values["kind"] != "" && values["namespace"] != "" && values["name"] != ""
}

func validate(field *grammar.QueryField) error {
	if field == nil {
		return fmt.Errorf("Kubernetes target selector is empty")
	}
	if field.Field == "" {
		if field.Op != "and" && field.Op != "or" {
			return fmt.Errorf("Kubernetes target group operator %q is unsupported", field.Op)
		}
		for _, child := range field.Fields {
			if err := validate(child); err != nil {
				return err
			}
		}
		return nil
	}
	if !supportedField(field.Field) {
		return fmt.Errorf("Kubernetes target field %q is unsupported", field.Field)
	}
	if field.Op != grammar.Eq && field.Op != grammar.Neq {
		return fmt.Errorf("Kubernetes target operator %q is unsupported for field %q", field.Op, field.Field)
	}
	if strings.TrimSpace(fmt.Sprint(field.Value)) == "" {
		return fmt.Errorf("Kubernetes target field %q has an empty value", field.Field)
	}
	return nil
}

func supportedField(field string) bool {
	switch field {
	case "kind", "namespace", "name", "uid":
		return true
	default:
		return strings.HasPrefix(field, "labels.") &&
			strings.TrimPrefix(field, "labels.") != ""
	}
}

func match(field *grammar.QueryField, resource Resource) bool {
	if field == nil {
		return true
	}
	if field.Field == "" {
		if field.Op == "or" {
			for _, child := range field.Fields {
				if match(child, resource) {
					return true
				}
			}
			return false
		}
		for _, child := range field.Fields {
			if !match(child, resource) {
				return false
			}
		}
		return true
	}

	value, exists := resourceValue(resource, field.Field)
	patterns := strings.Split(fmt.Sprint(field.Value), ",")
	matched := false
	for _, pattern := range patterns {
		if valueMatches(value, strings.TrimSpace(pattern), field.Field == "kind") {
			matched = true
			break
		}
	}
	if strings.HasPrefix(field.Field, "labels.") && len(patterns) == 1 && patterns[0] == "*" {
		matched = exists
	}
	if field.Op == grammar.Neq {
		return !matched
	}
	return exists && matched
}

func resourceValue(resource Resource, field string) (string, bool) {
	switch field {
	case "kind":
		return resource.Kind, resource.Kind != ""
	case "namespace":
		return resource.Namespace, resource.Namespace != ""
	case "name":
		return resource.Name, resource.Name != ""
	case "uid":
		return resource.UID, resource.UID != ""
	default:
		value, ok := resource.Labels[strings.TrimPrefix(field, "labels.")]
		return value, ok
	}
}

func valueMatches(value, pattern string, fold bool) bool {
	if fold {
		value, pattern = strings.ToLower(value), strings.ToLower(pattern)
	}
	matched, err := path.Match(pattern, value)
	return err == nil && matched
}

func collectExact(field *grammar.QueryField, values map[string]string) bool {
	if field == nil {
		return true
	}
	if field.Field == "" {
		if field.Op != "and" {
			return false
		}
		for _, child := range field.Fields {
			if !collectExact(child, values) {
				return false
			}
		}
		return true
	}
	if field.Op != grammar.Eq || strings.HasPrefix(field.Field, "labels.") {
		return true
	}
	value := fmt.Sprint(field.Value)
	if strings.ContainsAny(value, ",*?[") {
		return false
	}
	if previous := values[field.Field]; previous != "" && previous != value {
		return false
	}
	values[field.Field] = value
	return true
}
