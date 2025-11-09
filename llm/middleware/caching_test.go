package middleware

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/commons-db/llm"
	"github.com/flanksource/commons-db/llm/cache"
)

// mockProvider is a mock implementation of llm.Provider for testing
type mockProvider struct {
	executeFunc func(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResponse, error)
	callCount   int
}

func (m *mockProvider) Execute(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResponse, error) {
	m.callCount++
	if m.executeFunc != nil {
		return m.executeFunc(ctx, req)
	}
	return llm.ProviderResponse{
		Text:         "mock response",
		Model:        req.Model,
		InputTokens:  10,
		OutputTokens: 20,
	}, nil
}

func TestCacheMiddleware_CacheHit(t *testing.T) {
	// Create temporary cache database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-cache.db")

	// Create cache
	c, err := cache.New(cache.Config{
		DBPath:  dbPath,
		TTL:     24 * time.Hour,
		NoCache: false,
		Debug:   false,
	})
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	// Create mock provider
	mock := &mockProvider{}

	// Wrap with caching middleware
	provider := Wrap(mock, WithCacheInstance(c))

	ctx := context.Background()
	req := llm.ProviderRequest{
		Prompt: "test prompt",
		Model:  "gpt-4o",
	}

	// First call - cache miss
	resp1, err := provider.Execute(ctx, req)
	if err != nil {
		t.Fatalf("First Execute failed: %v", err)
	}
	if resp1.Text != "mock response" {
		t.Errorf("Expected 'mock response', got '%s'", resp1.Text)
	}
	if mock.callCount != 1 {
		t.Errorf("Expected 1 call to provider, got %d", mock.callCount)
	}

	// Second call - cache hit (should not call provider again)
	resp2, err := provider.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Second Execute failed: %v", err)
	}
	if resp2.Text != "mock response" {
		t.Errorf("Expected 'mock response', got '%s'", resp2.Text)
	}
	if mock.callCount != 1 {
		t.Errorf("Expected 1 call to provider (cached), got %d", mock.callCount)
	}
}

func TestCacheMiddleware_BypassCache(t *testing.T) {
	// Create temporary cache database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-cache.db")

	// Create cache
	c, err := cache.New(cache.Config{
		DBPath:  dbPath,
		TTL:     24 * time.Hour,
		NoCache: false,
		Debug:   false,
	})
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	// Create mock provider
	mock := &mockProvider{}

	// Wrap with caching middleware
	provider := Wrap(mock, WithCacheInstance(c))

	req := llm.ProviderRequest{
		Prompt: "test prompt",
		Model:  "gpt-4o",
	}

	// First call without bypass
	ctx1 := context.Background()
	_, err = provider.Execute(ctx1, req)
	if err != nil {
		t.Fatalf("First Execute failed: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("Expected 1 call to provider, got %d", mock.callCount)
	}

	// Second call with bypass - should call provider again
	ctx2 := WithNoCache(context.Background())
	_, err = provider.Execute(ctx2, req)
	if err != nil {
		t.Fatalf("Second Execute with bypass failed: %v", err)
	}
	if mock.callCount != 2 {
		t.Errorf("Expected 2 calls to provider (bypassed cache), got %d", mock.callCount)
	}
}

func TestCacheMiddleware_TTL(t *testing.T) {
	// Create temporary cache database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-cache.db")

	// Create cache with very short TTL
	c, err := cache.New(cache.Config{
		DBPath:  dbPath,
		TTL:     100 * time.Millisecond,
		NoCache: false,
		Debug:   false,
	})
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	// Create mock provider that returns different responses
	responseNum := 0
	mock := &mockProvider{
		executeFunc: func(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResponse, error) {
			responseNum++
			return llm.ProviderResponse{
				Text:         fmt.Sprintf("response %d", responseNum),
				Model:        req.Model,
				InputTokens:  10,
				OutputTokens: 20,
			}, nil
		},
	}

	// Wrap with caching middleware
	provider := Wrap(mock, WithCacheInstance(c))

	ctx := context.Background()
	req := llm.ProviderRequest{
		Prompt: "test prompt",
		Model:  "gpt-4o",
	}

	// First call
	resp1, err := provider.Execute(ctx, req)
	if err != nil {
		t.Fatalf("First Execute failed: %v", err)
	}
	if resp1.Text != "response 1" {
		t.Errorf("Expected 'response 1', got '%s'", resp1.Text)
	}

	// Second call immediately - should be cached
	resp2, err := provider.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Second Execute failed: %v", err)
	}
	if resp2.Text != "response 1" {
		t.Errorf("Expected 'response 1' (cached), got '%s'", resp2.Text)
	}

	// Wait for TTL to expire
	time.Sleep(200 * time.Millisecond)

	// Third call after TTL - should execute again
	resp3, err := provider.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Third Execute failed: %v", err)
	}
	if resp3.Text != "response 2" {
		t.Errorf("Expected 'response 2' (expired), got '%s'", resp3.Text)
	}
}

func TestCacheMiddleware_DifferentPrompts(t *testing.T) {
	// Create temporary cache database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-cache.db")

	// Create cache
	c, err := cache.New(cache.Config{
		DBPath:  dbPath,
		TTL:     24 * time.Hour,
		NoCache: false,
		Debug:   false,
	})
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	// Create mock provider
	mock := &mockProvider{
		executeFunc: func(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResponse, error) {
			return llm.ProviderResponse{
				Text:         "response to: " + req.Prompt,
				Model:        req.Model,
				InputTokens:  10,
				OutputTokens: 20,
			}, nil
		},
	}

	// Wrap with caching middleware
	provider := Wrap(mock, WithCacheInstance(c))

	ctx := context.Background()

	// First prompt
	resp1, err := provider.Execute(ctx, llm.ProviderRequest{
		Prompt: "prompt1",
		Model:  "gpt-4o",
	})
	if err != nil {
		t.Fatalf("First Execute failed: %v", err)
	}
	if resp1.Text != "response to: prompt1" {
		t.Errorf("Expected 'response to: prompt1', got '%s'", resp1.Text)
	}

	// Second prompt (different) - should not be cached
	resp2, err := provider.Execute(ctx, llm.ProviderRequest{
		Prompt: "prompt2",
		Model:  "gpt-4o",
	})
	if err != nil {
		t.Fatalf("Second Execute failed: %v", err)
	}
	if resp2.Text != "response to: prompt2" {
		t.Errorf("Expected 'response to: prompt2', got '%s'", resp2.Text)
	}

	// Should have called provider twice (different prompts)
	if mock.callCount != 2 {
		t.Errorf("Expected 2 calls to provider, got %d", mock.callCount)
	}
}
