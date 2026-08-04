package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

type Calculator struct{}

func (Calculator) Definition() Definition {
	return Definition{Type: "function", Function: FunctionSpec{
		Name:        "calculator",
		Description: "计算只包含数字、括号和 + - * / 的数学表达式。需要精确算术时使用。",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"expression": map[string]any{"type": "string", "description": "要计算的表达式，例如 (23+17)*2"}}, "required": []string{"expression"}, "additionalProperties": false},
	}}
}

func (Calculator) Execute(_ context.Context, raw json.RawMessage) (Result, error) {
	var in struct {
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{}, fmt.Errorf("%w: 参数不是合法 JSON", ErrInvalidInput)
	}
	expr := strings.TrimSpace(in.Expression)
	if expr == "" || len(expr) > 256 {
		return Result{}, fmt.Errorf("%w: 表达式为空或过长", ErrInvalidInput)
	}
	p := parser{s: expr}
	v, err := p.expression()
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	p.skip()
	if p.pos != len(p.s) {
		return Result{}, fmt.Errorf("%w: 位置 %d 有非法字符", ErrInvalidInput, p.pos+1)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e100 {
		return Result{}, fmt.Errorf("%w: 结果超出范围", ErrInvalidInput)
	}
	value := strconv.FormatFloat(v, 'f', -1, 64)
	return Result{ModelText: value, Summary: expr + " = " + value}, nil
}

type parser struct {
	s          string
	pos, depth int
}

func (p *parser) skip() {
	for p.pos < len(p.s) && unicode.IsSpace(rune(p.s[p.pos])) {
		p.pos++
	}
}
func (p *parser) expression() (float64, error) {
	left, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		p.skip()
		if p.pos >= len(p.s) || (p.s[p.pos] != '+' && p.s[p.pos] != '-') {
			return left, nil
		}
		op := p.s[p.pos]
		p.pos++
		right, err := p.term()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
}
func (p *parser) term() (float64, error) {
	left, err := p.factor()
	if err != nil {
		return 0, err
	}
	for {
		p.skip()
		if p.pos >= len(p.s) || (p.s[p.pos] != '*' && p.s[p.pos] != '/') {
			return left, nil
		}
		op := p.s[p.pos]
		p.pos++
		right, err := p.factor()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			left *= right
		} else {
			if right == 0 {
				return 0, fmt.Errorf("不能除以零")
			}
			left /= right
		}
	}
}
func (p *parser) factor() (float64, error) {
	p.skip()
	if p.pos >= len(p.s) {
		return 0, fmt.Errorf("表达式不完整")
	}
	sign := 1.0
	for p.pos < len(p.s) && (p.s[p.pos] == '+' || p.s[p.pos] == '-') {
		if p.s[p.pos] == '-' {
			sign = -sign
		}
		p.pos++
		p.skip()
	}
	if p.pos < len(p.s) && p.s[p.pos] == '(' {
		p.depth++
		if p.depth > 16 {
			return 0, fmt.Errorf("括号嵌套过深")
		}
		p.pos++
		v, err := p.expression()
		if err != nil {
			return 0, err
		}
		p.skip()
		if p.pos >= len(p.s) || p.s[p.pos] != ')' {
			return 0, fmt.Errorf("缺少右括号")
		}
		p.pos++
		p.depth--
		return sign * v, nil
	}
	start := p.pos
	dots := 0
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == '.' {
			dots++
			if dots > 1 {
				break
			}
			p.pos++
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		p.pos++
	}
	if start == p.pos {
		return 0, fmt.Errorf("位置 %d 需要数字", p.pos+1)
	}
	v, err := strconv.ParseFloat(p.s[start:p.pos], 64)
	if err != nil {
		return 0, fmt.Errorf("数字格式错误")
	}
	return sign * v, nil
}
