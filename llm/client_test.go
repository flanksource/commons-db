package llm

import (
	"os"
	"testing"

	. "github.com/flanksource/commons-db/llm/types"
	"github.com/flanksource/commons-db/types"
)

func TestInferBackendFromModel(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		want    LLMBackend
		wantErr bool
	}{
		{
			name:  "gpt-4o",
			model: "gpt-4o",
			want:  LLMBackendOpenAI,
		},
		{
			name:  "gpt-3.5-turbo",
			model: "gpt-3.5-turbo",
			want:  LLMBackendOpenAI,
		},
		{
			name:  "o1-preview",
			model: "o1-preview",
			want:  LLMBackendOpenAI,
		},
		{
			name:  "o3-mini",
			model: "o3-mini",
			want:  LLMBackendOpenAI,
		},
		{
			name:  "claude-3-5-sonnet",
			model: "claude-3-5-sonnet-20241022",
			want:  LLMBackendAnthropic,
		},
		{
			name:  "claude-opus",
			model: "claude-opus-4-20250514",
			want:  LLMBackendAnthropic,
		},
		{
			name:  "gemini-pro",
			model: "gemini-2.5-pro-exp-03-25",
			want:  LLMBackendGemini,
		},
		{
			name:  "gemini with models prefix",
			model: "models/gemini-pro",
			want:  LLMBackendGemini,
		},
		{
			name:    "unknown model",
			model:   "unknown-model-123",
			wantErr: true,
		},
		{
			name:    "empty model",
			model:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := inferBackendFromModel(tt.model)
			if (err != nil) != tt.wantErr {
				t.Errorf("inferBackendFromModel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("inferBackendFromModel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetAPIKeyFromEnv(t *testing.T) {
	tests := []struct {
		name       string
		backend    LLMBackend
		envVars    map[string]string
		want       string
		wantErr    bool
		clearFirst bool
	}{
		{
			name:    "OpenAI API key from OPENAI_API_KEY",
			backend: LLMBackendOpenAI,
			envVars: map[string]string{
				"OPENAI_API_KEY": "sk-test-key",
			},
			want: "sk-test-key",
		},
		{
			name:    "Anthropic API key from ANTHROPIC_API_KEY",
			backend: LLMBackendAnthropic,
			envVars: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-test",
			},
			want: "sk-ant-test",
		},
		{
			name:    "Gemini API key from GEMINI_API_KEY",
			backend: LLMBackendGemini,
			envVars: map[string]string{
				"GEMINI_API_KEY": "gemini-test-key",
			},
			want: "gemini-test-key",
		},
		{
			name:    "Gemini API key from GOOGLE_API_KEY",
			backend: LLMBackendGemini,
			envVars: map[string]string{
				"GOOGLE_API_KEY": "google-test-key",
			},
			want: "google-test-key",
		},
		{
			name:       "OpenAI missing API key",
			backend:    LLMBackendOpenAI,
			clearFirst: true,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear relevant env vars if requested
			if tt.clearFirst {
				os.Unsetenv("OPENAI_API_KEY")
				os.Unsetenv("ANTHROPIC_API_KEY")
				os.Unsetenv("GEMINI_API_KEY")
				os.Unsetenv("GOOGLE_API_KEY")
			}

			// Set environment variables for test
			for k, v := range tt.envVars {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			got, err := getAPIKeyFromEnv(tt.backend)
			if (err != nil) != tt.wantErr {
				t.Errorf("getAPIKeyFromEnv() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("getAPIKeyFromEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewClientWithModel(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		envVars map[string]string
		wantErr bool
	}{
		{
			name:  "valid OpenAI model with API key",
			model: "gpt-4o",
			envVars: map[string]string{
				"OPENAI_API_KEY": "sk-test",
			},
			wantErr: false,
		},
		{
			name:  "valid Anthropic model with API key",
			model: "claude-3-5-sonnet-20241022",
			envVars: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-test",
			},
			wantErr: false,
		},
		{
			name:    "empty model",
			model:   "",
			wantErr: true,
		},
		{
			name:    "unknown model",
			model:   "unknown-model",
			wantErr: true,
		},
		{
			name:    "valid model but missing API key",
			model:   "gpt-4o",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all API keys first
			os.Unsetenv("OPENAI_API_KEY")
			os.Unsetenv("ANTHROPIC_API_KEY")
			os.Unsetenv("GEMINI_API_KEY")
			os.Unsetenv("GOOGLE_API_KEY")

			// Set environment variables for test
			for k, v := range tt.envVars {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			client, err := NewClientWithModel(tt.model)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewClientWithModel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && client == nil {
				t.Error("NewClientWithModel() returned nil client")
			}
		})
	}
}

func TestBuildConnectionFromModel(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		envVars map[string]string
		want    *Connection
		wantErr bool
	}{
		{
			name:  "OpenAI connection",
			model: "gpt-4o",
			envVars: map[string]string{
				"OPENAI_API_KEY": "sk-test-key",
			},
			want: &Connection{
				Backend: LLMBackendOpenAI,
				Model:   "gpt-4o",
				HTTP: types.HTTP{
					Bearer: types.EnvVar{
						ValueStatic: "sk-test-key",
					},
				},
			},
		},
		{
			name:  "Anthropic connection",
			model: "claude-3-5-sonnet-20241022",
			envVars: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-test",
			},
			want: &Connection{
				Backend: LLMBackendAnthropic,
				Model:   "claude-3-5-sonnet-20241022",
				HTTP: types.HTTP{
					Bearer: types.EnvVar{
						ValueStatic: "sk-ant-test",
					},
				},
			},
		},
		{
			name:    "missing API key",
			model:   "gpt-4o",
			wantErr: true,
		},
		{
			name:    "unknown model",
			model:   "unknown-model",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all API keys first
			os.Unsetenv("OPENAI_API_KEY")
			os.Unsetenv("ANTHROPIC_API_KEY")
			os.Unsetenv("GEMINI_API_KEY")
			os.Unsetenv("GOOGLE_API_KEY")

			// Set environment variables for test
			for k, v := range tt.envVars {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			got, err := buildConnectionFromModel(tt.model)
			if (err != nil) != tt.wantErr {
				t.Errorf("buildConnectionFromModel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			if got.Backend != tt.want.Backend {
				t.Errorf("buildConnectionFromModel() Backend = %v, want %v", got.Backend, tt.want.Backend)
			}
			if got.Model != tt.want.Model {
				t.Errorf("buildConnectionFromModel() Model = %v, want %v", got.Model, tt.want.Model)
			}
			if got.Bearer.ValueStatic != tt.want.Bearer.ValueStatic {
				t.Errorf("buildConnectionFromModel() Bearer = %v, want %v", got.Bearer.ValueStatic, tt.want.Bearer.ValueStatic)
			}
		})
	}
}
