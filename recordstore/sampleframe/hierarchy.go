package sampleframe

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
)

type WeightedStack struct {
	Frames []string
	IDs    []string
	Value  float64
}
type WeightedNode struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Self  float64 `json:"self"`
	Total float64 `json:"total"`

	Children []*WeightedNode `json:"children"`
}
type Hierarchy struct {
	Metric string        `json:"metric"`
	Unit   string        `json:"unit"`
	Root   *WeightedNode `json:"root"`
}

// BuildHierarchy accepts root-first stacks with exclusive leaf weights.
func BuildHierarchy(stacks []WeightedStack) (*WeightedNode, error) {
	root := &WeightedNode{ID: "root", Label: "All stacks", Children: []*WeightedNode{}}
	for _, stack := range stacks {
		if stack.Value < 0 || math.IsNaN(float64(stack.Value)) || math.IsInf(float64(root.Total)+float64(stack.Value), 0) {
			return nil, fmt.Errorf("stack weights and total must be finite and nonnegative")
		}
		if len(stack.IDs) > 0 && len(stack.IDs) != len(stack.Frames) {
			return nil, fmt.Errorf("stack identities must match its frames")
		}
		root.Total += stack.Value
		node := root
		for frame, label := range stack.Frames {
			if strings.TrimSpace(label) == "" {
				return nil, fmt.Errorf("stack contains an empty frame")
			}
			key := label
			if len(stack.IDs) > 0 {
				key = stack.IDs[frame]
				if key == "" {
					return nil, fmt.Errorf("stack identity is empty")
				}
			}
			hash := sha256.Sum256([]byte(node.ID + "\x00" + key))
			id := hex.EncodeToString(hash[:])
			index := slices.IndexFunc(node.Children, func(child *WeightedNode) bool { return child.ID == id })
			if index < 0 {
				node.Children = append(node.Children, &WeightedNode{ID: id, Label: label, Children: []*WeightedNode{}})
				index = len(node.Children) - 1
			}
			node = node.Children[index]
			node.Total += stack.Value
		}
		node.Self += stack.Value
	}
	return root, nil
}
