package types

import (
	"fmt"

	"github.com/flanksource/clicky/api"
)

// Pretty returns a formatted representation of the ProviderRequest for display.
func (r ProviderRequest) PrettShort() api.Text {
	t := api.Text{}
	return t.Append("Model: ", "text-muted").Append(r.Model, "font-mono").Space().Append(r.Prompt, "max-w-[20ch]")
}

// Pretty returns a formatted representation of the ProviderRequest for display.
func (r ProviderRequest) Pretty() api.Text {
	t := api.Text{}
	t = t.Append("Model: ", "text-muted").Append(r.Model, "font-mono")
	if r.MaxTokens != nil {
		t = t.Space().Append("MaxTokens: ", "text-muted").Append(fmt.Sprintf("%d", *r.MaxTokens))
	}

	if r.StructuredOutput != nil {
		t = t.Space().Append("StructuredOutput: ", "text-muted").Append("enabled", "text-success")
	}

	if r.SystemPrompt != "" {
		t = t.NewLine().Append("System: ", "text-muted").Append(r.SystemPrompt)
	}

	t = t.NewLine().Append("Prompt: ", "text-muted").Append(r.Prompt)

	return t
}

func (r ProviderResponse) PrettyShort() api.Text {
	t := api.Text{}
	return t.Append("Model: ", "text-muted").Append(r.Model, "font-mono").Space().Append(r.Text, "max-w-[20ch]")

}

// Pretty returns a formatted representation of the ProviderResponse for display.
func (r ProviderResponse) Pretty() api.Text {
	t := api.Text{}
	t = t.Append("Model: ", "text-muted").Append(r.Model, "font-mono")

	t = t.Space().Append("Tokens: ", "text-muted")
	t = t.Append("in: ").Append(r.InputTokens)
	t = t.Append(" out: ").Append(r.OutputTokens)

	if r.StructuredData != nil {
		t = t.NewLine().Append("StructuredData: ", "text-muted").Append(fmt.Sprintf("%+v", r.StructuredData))
	} else if r.Text != "" {
		t = t.NewLine().Append("Response: ", "text-muted").Append(r.Text)
	}

	return t
}
