package tools

import (
	"context"
	"encoding/json"
	"errors"
)

// HostedWebSearch advertises the web_search capability to the classroom UI.
// The model provider executes it; Engine must never call Execute locally.
type HostedWebSearch struct{}

func (HostedWebSearch) Definition() Definition {
	return Definition{Type: "function", Function: FunctionSpec{
		Name:        "web_search",
		Description: "由模型服务商托管的联网搜索。",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	}}
}

func (HostedWebSearch) Execute(context.Context, json.RawMessage) (Result, error) {
	return Result{}, errors.New("托管网页搜索不能在本地执行")
}
