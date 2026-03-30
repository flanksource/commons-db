package llm

import (
	"strings"
	"sync"

	. "github.com/flanksource/commons-db/llm/types"
)

// MockExpectation defines a prompt match rule and its canned response.
type MockExpectation struct {
	Contains string
	Response string
}

var (
	mockExpectations []MockExpectation
	mockMu           sync.RWMutex
)

// MockWhen registers an expectation: when a prompt contains the given
// substring, return the specified response text.
func MockWhen(contains, response string) {
	mockMu.Lock()
	defer mockMu.Unlock()
	mockExpectations = append(mockExpectations, MockExpectation{Contains: contains, Response: response})
}

// MockReset clears all registered mock expectations.
func MockReset() {
	mockMu.Lock()
	defer mockMu.Unlock()
	mockExpectations = nil
}

type mockProvider struct {
	model string
}

// NewMockProvider creates a mock provider that returns canned responses.
func NewMockProvider(model string) Provider {
	return &mockProvider{model: model}
}

func (p *mockProvider) Execute(_ *Session, req ProviderRequest) (ProviderResponse, error) {
	mockMu.RLock()
	defer mockMu.RUnlock()

	for _, exp := range mockExpectations {
		if strings.Contains(req.Prompt, exp.Contains) {
			return ProviderResponse{Text: exp.Response, Model: p.model}, nil
		}
	}

	return ProviderResponse{Text: "mock response", Model: p.model}, nil
}

func (p *mockProvider) GetModel() string            { return p.model }
func (p *mockProvider) GetBackend() LLMBackend       { return LLMBackendMock }
func (p *mockProvider) GetOpenRouterModelID() string { return "" }
