package types

import (
	"context"
	"fmt"
	"sync"

	"github.com/flanksource/clicky/api"
)

// Session tracks costs across multiple agent calls
type Session struct {
	context.Context
	Model       string
	ID          string
	ProjectName string
	Costs       Costs
	mutex       sync.RWMutex
}

type Costs []Cost

type ModelType string

const (
	ModelTypeFast      ModelType = "fast"
	ModelTypeReasoning ModelType = "reasoning"
	ModelTypeCache     ModelType = "cache"
	ModelTypeLLM       ModelType = "llm"
)

type Cost struct {
	Model string `json:"model,omitempty"`
	// Multiple models can be used during to full a single request (e.g., cache + reasoning + LLM)
	ModelType    ModelType `json:"model_type,omitempty"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
	TotalTokens  int       `json:"total_tokens,omitempty"`
	InputCost    float64   `json:"input_cost,omitempty"`
	OutputCost   float64   `json:"output_cost,omitempty"`
}

func (c Cost) Total() float64 {
	return c.InputCost + c.OutputCost
}

func (c Cost) Pretty() api.Text {
	t := api.Text{}
	if c.Model != "" {
		t = t.Append(c.Model, "font-mono").Space()
	}
	if c.Total() > 0 {
		t = t.Append(fmt.Sprintf("$%.4f", c.Total()))
	}

	if c.InputTokens+c.OutputTokens > 0 {
		t = t.Space().Append(fmt.Sprintf("(%v in, %v out)", api.Human(c.InputTokens), api.Human(c.OutputTokens)), "text-muted")
	}

	return t
}

func (c Cost) Add(other Cost) Cost {
	model := c.Model
	if model == "" {
		model = other.Model
	}
	return Cost{
		Model:        model,
		ModelType:    c.ModelType,
		InputTokens:  c.InputTokens + other.InputTokens,
		OutputTokens: c.OutputTokens + other.OutputTokens,
		TotalTokens:  c.TotalTokens + other.TotalTokens,
		InputCost:    c.InputCost + other.InputCost,
		OutputCost:   c.OutputCost + other.OutputCost,
	}
}

func (c Costs) Add(other Cost) Costs {
	for _, existing := range c {
		if existing.Model == other.Model && existing.ModelType == other.ModelType {
			updated := existing.Add(other)
			// Replace existing cost
			newCosts := Costs{}
			for _, cost := range c {
				if cost.Model == existing.Model && cost.ModelType == existing.ModelType {
					newCosts = append(newCosts, updated)
				} else {
					newCosts = append(newCosts, cost)
				}
			}
			return newCosts
		}
	}
	return append(c, other)
}

func (c Costs) Sum() Cost {
	total := Cost{}
	for _, cost := range c {
		total = total.Add(cost)
	}
	return total
}

func (c Costs) GetCostsByModel() map[string]Cost {
	modelCosts := make(map[string]Cost)
	for _, cost := range c {
		model := cost.Model
		if model == "" {
			model = "unknown"
		}

		existing := modelCosts[model]
		existing.Model = model
		existing.InputTokens += cost.InputTokens
		existing.OutputTokens += cost.OutputTokens
		existing.TotalTokens += cost.TotalTokens
		existing.InputCost += cost.InputCost
		existing.OutputCost += cost.OutputCost
		modelCosts[model] = existing
	}
	return modelCosts
}

func (c Costs) Pretty() api.Text {

	modelCosts := c.GetCostsByModel()

	t := api.Text{}
	t = t.Append("Session Costs", "font-bold").NewLine()

	// Display each model's costs
	for _, cost := range modelCosts {
		t = t.Append("  ").Add(cost.Pretty()).NewLine()
	}

	if len(modelCosts) > 1 {
		// Display total
		t = t.Append("  Total: ", "font-bold").Add(c.Sum().Pretty())
	}
	return t

}

func (c Cost) TotalCost() float64 {
	return c.InputCost + c.OutputCost
}

// NewSession creates a new session for tracking costs
func NewSession(id, projectName string) *Session {
	return &Session{
		Context:     context.Background(),
		ID:          id,
		ProjectName: projectName,
		Costs:       Costs{},
	}
}

// AddCost adds a cost entry to the session in a thread-safe manner
func (s *Session) AddCost(cost Cost) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.Costs = s.Costs.Add(cost)
}

// GetTotalCost returns the aggregated cost across all entries
func (s *Session) GetTotalCost() Cost {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.Costs.Sum()
}

// GetCostsByModel returns costs grouped by model
func (s *Session) GetCostsByModel() map[string]Cost {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.Costs.GetCostsByModel()
}
