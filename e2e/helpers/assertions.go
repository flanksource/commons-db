package helpers

import (
	"fmt"
	"strings"

	"github.com/onsi/gomega/types"
)

type HaveKeyWithValueMatcher struct {
	key   string
	value string
}

func HaveKeyWithValue(key, value string) types.GomegaMatcher {
	return &HaveKeyWithValueMatcher{
		key:   key,
		value: value,
	}
}

func (m *HaveKeyWithValueMatcher) Match(actual interface{}) (success bool, err error) {
	labels, ok := actual.(map[string]string)
	if !ok {
		return false, fmt.Errorf("expected map[string]string, got %T", actual)
	}

	actualValue, exists := labels[m.key]
	if !exists {
		return false, nil
	}

	return actualValue == m.value, nil
}

func (m *HaveKeyWithValueMatcher) FailureMessage(actual interface{}) (message string) {
	return fmt.Sprintf("expected labels to have key %q with value %q", m.key, m.value)
}

func (m *HaveKeyWithValueMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	return fmt.Sprintf("expected labels not to have key %q with value %q", m.key, m.value)
}

type ContainsElementsMatcher struct {
	elements []string
}

func ContainsElements(elements ...string) types.GomegaMatcher {
	return &ContainsElementsMatcher{
		elements: elements,
	}
}

func (m *ContainsElementsMatcher) Match(actual interface{}) (success bool, err error) {
	files, ok := actual.([]string)
	if !ok {
		return false, fmt.Errorf("expected []string, got %T", actual)
	}

	for _, elem := range m.elements {
		found := false
		for _, f := range files {
			if f == elem {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}

	return true, nil
}

func (m *ContainsElementsMatcher) FailureMessage(actual interface{}) (message string) {
	return fmt.Sprintf("expected list to contain all elements: %v", m.elements)
}

func (m *ContainsElementsMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	return fmt.Sprintf("expected list not to contain all elements: %v", m.elements)
}

type PrefixMatcher struct {
	prefix string
}

func HavePrefix(prefix string) types.GomegaMatcher {
	return &PrefixMatcher{
		prefix: prefix,
	}
}

func (m *PrefixMatcher) Match(actual interface{}) (success bool, err error) {
	str, ok := actual.(string)
	if !ok {
		return false, fmt.Errorf("expected string, got %T", actual)
	}

	return strings.HasPrefix(str, m.prefix), nil
}

func (m *PrefixMatcher) FailureMessage(actual interface{}) (message string) {
	return fmt.Sprintf("expected string to have prefix %q", m.prefix)
}

func (m *PrefixMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	return fmt.Sprintf("expected string not to have prefix %q", m.prefix)
}

type HasLenMatcher struct {
	expectedLen int
}

func HaveLen(expectedLen int) types.GomegaMatcher {
	return &HasLenMatcher{
		expectedLen: expectedLen,
	}
}

func (m *HasLenMatcher) Match(actual interface{}) (success bool, err error) {
	switch v := actual.(type) {
	case []interface{}:
		return len(v) == m.expectedLen, nil
	case []string:
		return len(v) == m.expectedLen, nil
	case map[string]interface{}:
		return len(v) == m.expectedLen, nil
	case map[string]string:
		return len(v) == m.expectedLen, nil
	default:
		return false, fmt.Errorf("expected slice or map, got %T", actual)
	}
}

func (m *HasLenMatcher) FailureMessage(actual interface{}) (message string) {
	return fmt.Sprintf("expected length %d", m.expectedLen)
}

func (m *HasLenMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	return fmt.Sprintf("expected length not %d", m.expectedLen)
}
