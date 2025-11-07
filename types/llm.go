package types

// HTTP represents an HTTP connection with bearer token authentication.
// Used for HTTP-based connections like LLM providers (OpenAI, Anthropic, Gemini).
type HTTP struct {
	// URL is the base URL for the HTTP endpoint
	URL EnvVar `json:"url,omitempty" yaml:"url,omitempty"`
	// Bearer is the bearer token for authentication (API key)
	Bearer EnvVar `json:"bearer,omitempty" yaml:"bearer,omitempty"`
}

// IsEmpty returns true if both URL and Bearer are empty
func (h HTTP) IsEmpty() bool {
	return h.URL.IsEmpty() && h.Bearer.IsEmpty()
}

// GetURL returns the static URL value
func (h HTTP) GetURL() EnvVar {
	return h.URL
}

// GetBearer returns the bearer token EnvVar
func (h HTTP) GetBearer() EnvVar {
	return h.Bearer
}
