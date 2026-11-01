// Package model is the Model Router boundary. It selects a provider for a
// request, enforces token/budget/data-class limits, and records each call. It
// never executes a platform action and never holds browser credentials.
package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var (
	ErrInvalidRequest = errors.New("model: invalid request")
	ErrNoProvider     = errors.New("model: no eligible provider")
	ErrBudgetExceeded = errors.New("model: budget exceeded")
)

type Purpose string

const (
	PurposePlan    Purpose = "PLAN"
	PurposeAnalyze Purpose = "ANALYZE"
	PurposeCode    Purpose = "CODE"
	PurposeReport  Purpose = "REPORT"
	PurposeEmbed   Purpose = "EMBED"
)

func (p Purpose) Valid() bool {
	switch p {
	case PurposePlan, PurposeAnalyze, PurposeCode, PurposeReport, PurposeEmbed:
		return true
	default:
		return false
	}
}

type DataClass string

const (
	DataPublic    DataClass = "PUBLIC"
	DataInternal  DataClass = "INTERNAL"
	DataSensitive DataClass = "SENSITIVE"
)

func (d DataClass) Valid() bool {
	switch d {
	case DataPublic, DataInternal, DataSensitive:
		return true
	default:
		return false
	}
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ModelRequest is one routed model call. TaskID is optional; when present it
// links the call to a ResearchTask. MaxTokens is the output-token ceiling.
type ModelRequest struct {
	TaskID    domain.TaskID `json:"task_id,omitempty"`
	Purpose   Purpose       `json:"purpose"`
	DataClass DataClass     `json:"data_class"`
	Input     []Message     `json:"input"`
	MaxTokens int           `json:"max_tokens"`
	Budget    domain.Money  `json:"budget"`
}

func (r ModelRequest) Validate() error {
	if !r.Purpose.Valid() {
		return fmt.Errorf("%w: invalid purpose %q", ErrInvalidRequest, r.Purpose)
	}
	if !r.DataClass.Valid() {
		return fmt.Errorf("%w: invalid data class %q", ErrInvalidRequest, r.DataClass)
	}
	if len(r.Input) == 0 {
		return fmt.Errorf("%w: input is empty", ErrInvalidRequest)
	}
	if r.MaxTokens <= 0 {
		return fmt.Errorf("%w: max_tokens must be positive", ErrInvalidRequest)
	}
	if r.Budget.Amount < 0 {
		return fmt.Errorf("%w: budget is negative", ErrInvalidRequest)
	}
	if r.Budget.Amount > 0 && strings.TrimSpace(r.Budget.Currency) == "" {
		return fmt.Errorf("%w: budget currency is empty", ErrInvalidRequest)
	}
	for _, m := range r.Input {
		if strings.TrimSpace(m.Role) == "" {
			return fmt.Errorf("%w: message role is empty", ErrInvalidRequest)
		}
	}
	return nil
}

type ModelResponse struct {
	Content    string        `json:"content"`
	Usage      Usage         `json:"usage"`
	Model      string        `json:"model"`
	Provider   string        `json:"provider"`
	Latency    time.Duration `json:"latency"`
	Confidence float64       `json:"confidence"`
	Cost       domain.Money  `json:"cost"`
}

// ProviderInfo declares what a provider may be used for. Prices are in the
// smallest currency unit (fen) per million tokens.
type ProviderInfo struct {
	Name               string
	Model              string
	Local              bool
	DataClasses        []DataClass
	Purposes           []Purpose
	MaxOutputTokens    int
	InputPricePerMTok  int64
	OutputPricePerMTok int64
	// Strength is the planning/analysis preference; higher is stronger.
	Strength int
}

func (i ProviderInfo) SupportsDataClass(d DataClass) bool {
	if len(i.DataClasses) == 0 {
		return true
	}
	for _, c := range i.DataClasses {
		if c == d {
			return true
		}
	}
	return false
}

func (i ProviderInfo) SupportsPurpose(p Purpose) bool {
	if len(i.Purposes) == 0 {
		return true
	}
	for _, x := range i.Purposes {
		if x == p {
			return true
		}
	}
	return false
}

type Provider interface {
	Info() ProviderInfo
	Complete(ctx context.Context, req ModelRequest) (ModelResponse, error)
}

// EstimateCost returns a conservative pre-call estimate: input tokens are
// approximated from characters and MaxTokens is treated as output.
func EstimateCost(info ProviderInfo, req ModelRequest) domain.Money {
	inputTokens := 0
	for _, m := range req.Input {
		inputTokens += len([]rune(m.Content))/4 + 1
	}
	cents := ceilDiv(int64(inputTokens)*info.InputPricePerMTok, 1_000_000) +
		ceilDiv(int64(req.MaxTokens)*info.OutputPricePerMTok, 1_000_000)
	return domain.Money{Amount: cents, Currency: "CNY"}
}

func ActualCost(info ProviderInfo, usage Usage) domain.Money {
	cents := ceilDiv(int64(usage.InputTokens)*info.InputPricePerMTok, 1_000_000) +
		ceilDiv(int64(usage.OutputTokens)*info.OutputPricePerMTok, 1_000_000)
	return domain.Money{Amount: cents, Currency: "CNY"}
}

func ceilDiv(a, b int64) int64 {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func PromptHash(req ModelRequest) string {
	h := sha256.New()
	for _, m := range req.Input {
		_, _ = h.Write([]byte(m.Role))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(m.Content))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func ResponseHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// StaticProvider is a deterministic provider for tests and local dry runs. It
// performs no network I/O.
type StaticProvider struct {
	Meta     ProviderInfo
	Response string
	Err      error
	Calls    int
	// Usage lets tests model an expensive response.
	Usage Usage
}

func (p *StaticProvider) Info() ProviderInfo { return p.Meta }

func (p *StaticProvider) Complete(ctx context.Context, req ModelRequest) (ModelResponse, error) {
	p.Calls++
	if p.Err != nil {
		return ModelResponse{}, p.Err
	}
	usage := p.Usage
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		usage = Usage{InputTokens: PromptTokens(req), OutputTokens: len([]rune(p.Response))/4 + 1}
	}
	return ModelResponse{
		Content: p.Response, Usage: usage, Model: p.Meta.Model,
		Provider: p.Meta.Name, Confidence: 1,
	}, nil
}

func PromptTokens(req ModelRequest) int {
	n := 0
	for _, m := range req.Input {
		n += len([]rune(m.Content))/4 + 1
	}
	return n
}
