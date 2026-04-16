package middleware

import (
	"net/http"

	. "github.com/flanksource/commons-db/llm/types"
	"github.com/flanksource/commons/logger"
)

type httpLoggingProvider struct {
	provider   Provider
	httpClient *http.Client
}

func (p *httpLoggingProvider) Execute(sess *Session, req ProviderRequest) (ProviderResponse, error) {
	req.HTTPClient = p.httpClient
	return p.provider.Execute(sess, req)
}

func (p *httpLoggingProvider) GetModel() string             { return p.provider.GetModel() }
func (p *httpLoggingProvider) GetBackend() LLMBackend       { return p.provider.GetBackend() }
func (p *httpLoggingProvider) GetOpenRouterModelID() string { return p.provider.GetOpenRouterModelID() }

// WithHTTPLogging returns a middleware that injects an HTTP client with request/response
// logging using commons/logger.NewHttpLoggerWithLevels.
// Headers are logged at headerLevel, bodies at bodyLevel.
func WithHTTPLogging(headerLevel, bodyLevel logger.LogLevel) Option {
	return func(p Provider) (Provider, error) {
		if !logger.IsLevelEnabled(headerLevel) {
			return p, nil
		}
		transport := logger.NewHttpLoggerWithLevels(
			logger.StandardLogger(),
			http.DefaultTransport,
			headerLevel, bodyLevel,
		)
		return &httpLoggingProvider{
			provider:   p,
			httpClient: &http.Client{Transport: transport},
		}, nil
	}
}
