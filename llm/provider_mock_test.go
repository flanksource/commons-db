package llm

import (
	"testing"

	. "github.com/flanksource/commons-db/llm/types"
)

func TestMockProvider_DefaultResponse(t *testing.T) {
	p := NewMockProvider("test-model")
	resp, err := p.Execute(&Session{}, ProviderRequest{Prompt: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "mock response" {
		t.Errorf("got %q, want %q", resp.Text, "mock response")
	}
	if resp.Model != "test-model" {
		t.Errorf("model = %q, want %q", resp.Model, "test-model")
	}
}

func TestMockProvider_Expectations(t *testing.T) {
	MockReset()
	defer MockReset()

	MockWhen("analyze commit", "type: feat\nscope: test\nsubject: mock commit")
	MockWhen("summarize", "name: Summary\ndescription: Mock summary")

	p := NewMockProvider("test-model")

	resp, _ := p.Execute(&Session{}, ProviderRequest{Prompt: "please analyze commit abc123"})
	if resp.Text != "type: feat\nscope: test\nsubject: mock commit" {
		t.Errorf("got %q", resp.Text)
	}

	resp, _ = p.Execute(&Session{}, ProviderRequest{Prompt: "summarize the changes"})
	if resp.Text != "name: Summary\ndescription: Mock summary" {
		t.Errorf("got %q", resp.Text)
	}

	resp, _ = p.Execute(&Session{}, ProviderRequest{Prompt: "no match here"})
	if resp.Text != "mock response" {
		t.Errorf("unmatched should return default, got %q", resp.Text)
	}
}

func TestMockReset(t *testing.T) {
	MockWhen("test", "response")
	MockReset()

	p := NewMockProvider("m")
	resp, _ := p.Execute(&Session{}, ProviderRequest{Prompt: "test"})
	if resp.Text != "mock response" {
		t.Errorf("after reset, expected default response, got %q", resp.Text)
	}
}

func TestMockProvider_Backend(t *testing.T) {
	p := NewMockProvider("m")
	if p.GetBackend() != LLMBackendMock {
		t.Errorf("backend = %q, want %q", p.GetBackend(), LLMBackendMock)
	}
	if p.GetOpenRouterModelID() != "" {
		t.Errorf("OpenRouter ID should be empty")
	}
}
