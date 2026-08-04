package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"classroom-agent/internal/store"
	"classroom-agent/internal/tools"
)

var (
	ErrBusy      = errors.New("chat already in progress")
	ErrBudget    = errors.New("student token budget exceeded")
	ErrTurnLimit = errors.New("agent turn limit reached")
)

type Event struct {
	Type      string          `json:"type"`
	TurnID    string          `json:"turn_id,omitempty"`
	Step      int             `json:"step,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	Delta     string          `json:"delta,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	TokensIn  int64           `json:"tokens_in,omitempty"`
	TokensOut int64           `json:"tokens_out,omitempty"`
	Code      string          `json:"code,omitempty"`
	Message   string          `json:"message,omitempty"`
	Success   *bool           `json:"success,omitempty"`
}

type Request struct {
	RunID, StudentID, TurnID, Input string
	Design                          store.Design
}

type Engine struct {
	Store                           *store.Store
	Client                          Client
	Tools                           *tools.Registry
	Model                           string
	HMACKey                         []byte
	Timeout                         time.Duration
	Semaphore                       chan struct{}
	TokenBudget                     int64
	MaxToolCalls                    int
	MaxOutputChars                  int
	InputPricePerM, OutputPricePerM float64
	mu                              sync.Mutex
	active                          map[string]bool
}

func NewEngine(st *store.Store, client Client, registry *tools.Registry, model, hmacKey string, timeout time.Duration, concurrency int) *Engine {
	return &Engine{Store: st, Client: client, Tools: registry, Model: model, HMACKey: []byte(hmacKey), Timeout: timeout, Semaphore: make(chan struct{}, concurrency), active: map[string]bool{}}
}

func (e *Engine) Run(ctx context.Context, req Request, emit func(Event) error) error {
	key := req.RunID + "\x00" + req.StudentID
	e.mu.Lock()
	if e.active[key] {
		e.mu.Unlock()
		return ErrBusy
	}
	e.active[key] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.active, key); e.mu.Unlock() }()
	u, err := e.Store.Usage(ctx, req.RunID, req.StudentID)
	if err != nil {
		return err
	}
	if u.TokensIn+u.TokensOut >= e.TokenBudget {
		return ErrBudget
	}
	if _, err = e.Store.AddMessage(ctx, store.Message{RunID: req.RunID, StudentID: req.StudentID, TurnID: req.TurnID, Role: "user", Content: req.Input}); err != nil {
		return err
	}
	if err = emit(Event{Type: "turn_start", TurnID: req.TurnID}); err != nil {
		return err
	}
	history, err := e.Store.Messages(ctx, req.RunID, req.StudentID, 0, 500)
	if err != nil {
		return err
	}
	messages := buildMessages(req.Design, history)
	defs := e.Tools.Definitions(req.Design.Tools)
	var totalIn, totalOut int64
	toolCount := 0
	defer func() {
		if totalIn+totalOut > 0 {
			cost := float64(totalIn)/1e6*e.InputPricePerM + float64(totalOut)/1e6*e.OutputPricePerM
			_ = e.Store.AddUsage(context.Background(), req.RunID, req.StudentID, totalIn, totalOut, cost)
		}
	}()
	for iteration := 1; iteration <= req.Design.MaxTurns; iteration++ {
		streamedText := false
		streamedChars := 0
		completion, callErr := e.callWithRetry(ctx, CompletionRequest{Model: e.Model, UserID: e.anonymousID(req.RunID, req.StudentID), Messages: messages, Tools: defs, OnDelta: func(delta string) error {
			remaining := e.MaxOutputChars - streamedChars
			if remaining <= 0 {
				return nil
			}
			delta = truncateRunes(delta, remaining)
			if delta == "" {
				return nil
			}
			streamedText = true
			streamedChars += utf8.RuneCountInString(delta)
			return emit(Event{Type: "text_delta", Delta: delta})
		}})
		if callErr != nil {
			return callErr
		}
		totalIn += completion.TokensIn
		totalOut += completion.TokensOut
		if totalIn+totalOut+u.TokensIn+u.TokensOut > e.TokenBudget {
			return ErrBudget
		}
		if len(completion.ToolCalls) == 0 {
			content := truncateRunes(completion.Content, e.MaxOutputChars)
			if !streamedText {
				for _, delta := range completion.Deltas {
					if content == "" {
						break
					}
					piece := delta
					if utf8.RuneCountInString(piece) > utf8.RuneCountInString(content) {
						piece = content
					}
					if err := emit(Event{Type: "text_delta", Delta: piece}); err != nil {
						return err
					}
					content = strings.TrimPrefix(content, piece)
				}
				if content != "" {
					if err := emit(Event{Type: "text_delta", Delta: content}); err != nil {
						return err
					}
				}
			}
			final := truncateRunes(completion.Content, e.MaxOutputChars)
			if _, err = e.Store.AddMessage(ctx, store.Message{RunID: req.RunID, StudentID: req.StudentID, TurnID: req.TurnID, Role: "assistant", Content: final}); err != nil {
				return err
			}
			return emit(Event{Type: "turn_end", TurnID: req.TurnID, Reason: "completed", TokensIn: totalIn, TokensOut: totalOut})
		}
		callJSON, _ := json.Marshal(completion.ToolCalls)
		if _, err = e.Store.AddMessage(ctx, store.Message{RunID: req.RunID, StudentID: req.StudentID, TurnID: req.TurnID, Role: "assistant", Content: completion.Content, ToolCalls: string(callJSON)}); err != nil {
			return err
		}
		messages = append(messages, Message{Role: "assistant", Content: completion.Content, ToolCalls: completion.ToolCalls})
		for _, call := range completion.ToolCalls {
			toolCount++
			if toolCount > e.MaxToolCalls {
				return ErrTurnLimit
			}
			detail, _ := json.Marshal(call)
			summary := summarizeArguments(call.Function.Arguments)
			if err := emit(Event{Type: "tool_start", Step: toolCount, Tool: call.Function.Name, Summary: summary, Detail: detail}); err != nil {
				return err
			}
			t, ok := e.Tools.Get(call.Function.Name)
			var result tools.Result
			var toolErr error
			if !ok || !contains(req.Design.Tools, call.Function.Name) {
				toolErr = fmt.Errorf("工具 %q 未启用", call.Function.Name)
			} else {
				result, toolErr = t.Execute(ctx, json.RawMessage(call.Function.Arguments))
			}
			success := toolErr == nil
			modelText := result.ModelText
			if toolErr != nil {
				modelText = "工具执行失败：" + toolErr.Error()
				result.Summary = modelText
			}
			if _, err = e.Store.AddMessage(ctx, store.Message{RunID: req.RunID, StudentID: req.StudentID, TurnID: req.TurnID, Role: "tool", Content: modelText, ToolCalls: call.ID}); err != nil {
				return err
			}
			messages = append(messages, Message{Role: "tool", ToolCallID: call.ID, Content: modelText})
			if err := emit(Event{Type: "tool_result", Step: toolCount, Tool: call.Function.Name, Summary: result.Summary, Success: &success}); err != nil {
				return err
			}
		}
	}
	return ErrTurnLimit
}

func (e *Engine) callWithRetry(ctx context.Context, req CompletionRequest) (Completion, error) {
	var last error
	originalDelta := req.OnDelta
	streamed := false
	if originalDelta != nil {
		req.OnDelta = func(delta string) error { streamed = true; return originalDelta(delta) }
	}
	for attempt := 0; attempt < 2; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, e.Timeout)
		select {
		case e.Semaphore <- struct{}{}:
		case <-callCtx.Done():
			cancel()
			return Completion{}, callCtx.Err()
		}
		out, err := e.Client.Complete(callCtx, req)
		<-e.Semaphore
		cancel()
		if err == nil {
			return out, nil
		}
		last = err
		if streamed {
			return Completion{}, err
		}
		if ctx.Err() != nil {
			return Completion{}, ctx.Err()
		}
	}
	return Completion{}, last
}

func (e *Engine) anonymousID(runID, studentID string) string {
	h := hmac.New(sha256.New, e.HMACKey)
	h.Write([]byte(runID))
	h.Write([]byte{0})
	h.Write([]byte(studentID))
	return hex.EncodeToString(h.Sum(nil))[:32]
}
func buildMessages(d store.Design, history []store.Message) []Message {
	system := "你是课堂 Agent。模型只负责决定是否使用已提供工具；工具由平台执行。不得声称执行未提供的工具。\n\n学生设置的人设：\n" + d.Persona + "\n\n学生设置的技能手册：\n" + d.SkillMD
	out := []Message{{Role: "system", Content: system}}
	for _, m := range history {
		am := Message{Role: m.Role, Content: m.Content}
		if m.Role == "assistant" && m.ToolCalls != "" {
			_ = json.Unmarshal([]byte(m.ToolCalls), &am.ToolCalls)
		}
		if m.Role == "tool" {
			am.ToolCallID = m.ToolCalls
		}
		out = append(out, am)
	}
	return out
}
func contains(v []string, want string) bool {
	for _, s := range v {
		if s == want {
			return true
		}
	}
	return false
}
func summarizeArguments(v string) string {
	if len(v) > 160 {
		return v[:160] + "…"
	}
	return v
}
func truncateRunes(v string, max int) string {
	if max <= 0 || utf8.RuneCountInString(v) <= max {
		return v
	}
	r := []rune(v)
	return string(r[:max])
}
