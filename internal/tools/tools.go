package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
)

var ErrInvalidInput = errors.New("invalid tool input")

type Definition struct {
	Type     string       `json:"type"`
	Function FunctionSpec `json:"function"`
}

type FunctionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Result struct {
	ModelText string `json:"model_text"`
	Summary   string `json:"summary"`
}

type Tool interface {
	Definition() Definition
	Execute(context.Context, json.RawMessage) (Result, error)
}

type Registry struct{ values map[string]Tool }

func NewRegistry(items ...Tool) *Registry {
	r := &Registry{values: map[string]Tool{}}
	for _, item := range items {
		r.values[item.Definition().Function.Name] = item
	}
	return r
}

func (r *Registry) Get(name string) (Tool, bool) { t, ok := r.values[name]; return t, ok }
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.values))
	for name := range r.values {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
func (r *Registry) Definitions(enabled []string) []Definition {
	out := make([]Definition, 0, len(enabled))
	for _, name := range enabled {
		if t, ok := r.values[name]; ok {
			out = append(out, t.Definition())
		}
	}
	return out
}
