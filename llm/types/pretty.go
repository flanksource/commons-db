package types

import (
	"fmt"

	"github.com/flanksource/clicky/api"
)

// Pretty returns a formatted representation of the ProviderRequest for display.
func (r ProviderRequest) Pretty() api.Text {
	t := api.Text{}
	t = t.Append("Model: ", "text-muted").Append(r.Model, "font-mono")

	if r.SystemPrompt != "" {
		t = t.NewLine().Append("System: ", "text-muted").Append(r.SystemPrompt)
	}

	t = t.NewLine().Append("Prompt: ", "text-muted").Append(r.Prompt)

	if r.MaxTokens != nil {
		t = t.NewLine().Append("MaxTokens: ", "text-muted").Append(fmt.Sprintf("%d", *r.MaxTokens))
	}

	if r.StructuredOutput != nil {
		t = t.NewLine().Append("StructuredOutput: ", "text-muted").Append("enabled", "text-success")
	}

	if r.APIKey != "" {
		t = t.NewLine().Append("APIKey: ", "text-muted").Append("***", "text-muted")
	}

	if r.APIURL != "" {
		t = t.NewLine().Append("URL: ", "text-muted").Append(r.APIURL, "font-mono")
	}

	return t
}

// Pretty returns a formatted representation of the ProviderResponse for display.
func (r ProviderResponse) Pretty() api.Text {
	t := api.Text{}
	t = t.Append("Model: ", "text-muted").Append(r.Model, "font-mono")

	t = t.NewLine().Append("Tokens: ", "text-muted")
	t = t.Append(fmt.Sprintf("in=%d ", r.InputTokens), "text-info")
	t = t.Append(fmt.Sprintf("out=%d ", r.OutputTokens), "text-info")

	if r.ReasoningTokens != nil && *r.ReasoningTokens > 0 {
		t = t.Append(fmt.Sprintf("reasoning=%d ", *r.ReasoningTokens), "text-warning")
	}

	if r.CacheReadTokens != nil && *r.CacheReadTokens > 0 {
		t = t.Append(fmt.Sprintf("cache_read=%d ", *r.CacheReadTokens), "text-success")
	}

	if r.CacheWriteTokens != nil && *r.CacheWriteTokens > 0 {
		t = t.Append(fmt.Sprintf("cache_write=%d ", *r.CacheWriteTokens), "text-success")
	}

	if r.Text != "" {
		t = t.NewLine().Append("Response: ", "text-muted").Append(r.Text)
	}

	if r.StructuredData != nil {
		t = t.NewLine().Append("StructuredData: ", "text-muted").Append(fmt.Sprintf("%+v", r.StructuredData))
	}

	return t
}
