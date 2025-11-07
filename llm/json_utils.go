package llm

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	// Match markdown code blocks with optional language specifier
	codeBlockRegex = regexp.MustCompile("(?s)```(?:json)?\\s*(.+?)```")

	// Match JSON objects or arrays
	jsonObjectRegex = regexp.MustCompile(`(?s)\{.*\}`)
	jsonArrayRegex  = regexp.MustCompile(`(?s)\[.*\]`)
)

// CleanupJSONResponse attempts to extract and clean JSON from LLM responses
// that may contain markdown formatting, explanatory text, or other noise.
//
// It tries the following strategies in order:
// 1. Validate if already valid JSON
// 2. Extract JSON from markdown code blocks (```json or ```)
// 3. Extract the first JSON object {...}
// 4. Extract the first JSON array [...]
// 5. Return the trimmed original string
//
// After extraction, it validates that the result is valid JSON.
func CleanupJSONResponse(response string) string {
	// Trim whitespace
	response = strings.TrimSpace(response)

	// If empty, return as-is
	if response == "" {
		return response
	}

	// If it's already valid JSON, return as-is
	if isValidJSON(response) {
		return response
	}

	// Strategy 1: Extract from markdown code blocks
	if matches := codeBlockRegex.FindStringSubmatch(response); len(matches) > 1 {
		extracted := strings.TrimSpace(matches[1])
		if isValidJSON(extracted) {
			return extracted
		}
	}

	// Strategy 2: Extract first JSON object
	if match := jsonObjectRegex.FindString(response); match != "" {
		if isValidJSON(match) {
			return match
		}
	}

	// Strategy 3: Extract first JSON array
	if match := jsonArrayRegex.FindString(response); match != "" {
		if isValidJSON(match) {
			return match
		}
	}

	// Strategy 4: Return original trimmed
	return response
}

// isValidJSON checks if a string is valid JSON
func isValidJSON(s string) bool {
	var js interface{}
	return json.Unmarshal([]byte(s), &js) == nil
}

// UnmarshalWithCleanup attempts to unmarshal JSON with automatic cleanup
func UnmarshalWithCleanup(data string, v interface{}) error {
	// First try direct unmarshal
	if err := json.Unmarshal([]byte(data), v); err == nil {
		return nil
	}

	// If that fails, try with cleanup
	cleaned := CleanupJSONResponse(data)
	return json.Unmarshal([]byte(cleaned), v)
}
