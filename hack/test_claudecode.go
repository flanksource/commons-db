package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/flanksource/commons-db/llm"
)

// TestStructure is a sample struct for testing structured output
type TestStructure struct {
	Summary     string   `json:"summary"`
	KeyConcepts []string `json:"keyConcepts"`
	Difficulty  string   `json:"difficulty"`
}

func main() {
	fmt.Println("=== Testing Claude Code CLI Provider ===\n")

	// Test 1: Simple text generation
	fmt.Println("Test 1: Text Generation")
	fmt.Println("-----------------------")
	testTextGeneration()

	fmt.Println("\n")

	// Test 2: Structured output
	fmt.Println("Test 2: Structured Output")
	fmt.Println("-------------------------")
	testStructuredOutput()

	fmt.Println("\n")

	// Test 3: Error handling - timeout
	fmt.Println("Test 3: Timeout Handling")
	fmt.Println("------------------------")
	testTimeout()
}

func testTextGeneration() {
	ctx := context.Background()

	// Create client with Claude Code model
	client, err := llm.NewClientWithModel("claude-code-sonnet")
	if err != nil {
		log.Printf("ERROR: Failed to create client: %v", err)
		return
	}

	// Execute simple request
	resp, err := client.NewRequest().
		WithSystemPrompt("You are a helpful Go programming assistant.").
		WithPrompt("Explain what Go interfaces are in one sentence.").
		WithTimeout(30 * time.Second).
		Execute(ctx)

	if err != nil {
		log.Printf("ERROR: Request failed: %v", err)
		return
	}

	fmt.Printf("Response: %s\n", resp.Text)
	fmt.Printf("Cost: $%.6f\n", resp.CostInfo.Cost)
	fmt.Printf("Tokens: %d input, %d output\n",
		resp.CostInfo.InputTokens, resp.CostInfo.OutputTokens)
}

func testStructuredOutput() {
	ctx := context.Background()

	client, err := llm.NewClientWithModel("claude-code-sonnet")
	if err != nil {
		log.Printf("ERROR: Failed to create client: %v", err)
		return
	}

	var result TestStructure
	resp, err := client.NewRequest().
		WithSystemPrompt("You are a Go programming expert.").
		WithPrompt("Explain Go interfaces").
		WithStructuredOutput(&result).
		WithTimeout(60 * time.Second).
		Execute(ctx)

	if err != nil {
		log.Printf("ERROR: Request failed: %v", err)
		return
	}

	// Pretty print the structured result
	jsonBytes, _ := json.MarshalIndent(resp.StructuredData, "", "  ")
	fmt.Printf("Structured Response:\n%s\n", string(jsonBytes))
	fmt.Printf("Cost: $%.6f\n", resp.CostInfo.Cost)
	fmt.Printf("Tokens: %d input, %d output\n",
		resp.CostInfo.InputTokens, resp.CostInfo.OutputTokens)
}

func testTimeout() {
	ctx := context.Background()

	client, err := llm.NewClientWithModel("claude-code-sonnet")
	if err != nil {
		log.Printf("ERROR: Failed to create client: %v", err)
		return
	}

	// Use extremely short timeout to trigger timeout error
	_, err = client.NewRequest().
		WithPrompt("Explain quantum computing in detail.").
		WithTimeout(1 * time.Millisecond).
		Execute(ctx)

	if err != nil {
		fmt.Printf("Expected timeout error: %v\n", err)
	} else {
		fmt.Println("WARNING: Expected timeout but request succeeded")
	}
}
