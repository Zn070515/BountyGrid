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
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var (
	ErrInvalidRequest = errors.New("model: invalid request")
	ErrNoProvider     = errors.New("model: no eligible provider")
	ErrBudgetExceeded = errors.New("model: budget exceeded")
	// ErrDuplicateCall means this logical call already has a reservation. It is
	// returned instead of invoking a provider again and paying twice.
	ErrDuplicateCall = errors.New("model: call already recorded")
	// ErrCallAmbiguous means the recorded call is RESERVED, STARTED or
	// AMBIGUOUS. The provider may already have been invoked and charged, so a
	// retry is refused until a human reconciles the call.
	ErrCallAmbiguous = errors.New("model: call is ambiguous; the provider may have been charged and manual reconciliation is required")
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

func (d DataClass) rank() int {
	switch d {
	case DataPublic:
		return 0
	case DataInternal:
		return 1
	case DataSensitive:
		return 2
	default:
		return 3 // invalid is stricter than SENSITIVE
	}
}

// MaxDataClass returns the more sensitive of two classes. Classification is
// monotonic: a caller (or a model) may raise the class, never lower it. An
// invalid class is not a valid "more sensitive" answer, so it collapses to
// SENSITIVE instead of leaking an unknown string into routing.
func MaxDataClass(a, b DataClass) DataClass {
	if !a.Valid() || !b.Valid() {
		return DataSensitive
	}
	if a.rank() >= b.rank() {
		return a
	}
	return b
}

// EffectiveDataClass is the maximum of the requested class and every provenance
// class. Provenance must be supplied by the caller from evidence/task
// classification; the router refuses to let a request label sensitive content as
// PUBLIC. Any malformed class fails closed to SENSITIVE.
func (r ModelRequest) EffectiveDataClass() DataClass {
	out := r.DataClass
	if !out.Valid() {
		return DataSensitive
	}
	for _, d := range r.Provenance {
		if !d.Valid() {
			return DataSensitive
		}
		out = MaxDataClass(out, d)
	}
	return out
}

// Capability is an optional provider feature a request depends on.
type Capability string

const (
	CapabilityToolCalling      Capability = "TOOL_CALLING"
	CapabilityVision           Capability = "VISION"
	CapabilityStructuredOutput Capability = "STRUCTURED_OUTPUT"
)

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
	// Provenance records the data classes of the inputs (evidence, task, source
	// files). The router uses max(DataClass, Provenance...) so a request cannot
	// under-classify sensitive content.
	Provenance []DataClass  `json:"provenance,omitempty"`
	Input      []Message    `json:"input"`
	MaxTokens  int          `json:"max_tokens"`
	Budget     domain.Money `json:"budget"`
	// CallID is an optional idempotency key. When set, a retry with the same ID
	// is rejected instead of paying for a second model call.
	CallID string `json:"call_id,omitempty"`
	// RequiredCapabilities filters out providers lacking a needed feature.
	RequiredCapabilities []Capability `json:"required_capabilities,omitempty"`
}

func (r ModelRequest) Validate() error {
	if !r.Purpose.Valid() {
		return fmt.Errorf("%w: invalid purpose %q", ErrInvalidRequest, r.Purpose)
	}
	if !r.DataClass.Valid() {
		return fmt.Errorf("%w: invalid data class %q", ErrInvalidRequest, r.DataClass)
	}
	for _, d := range r.Provenance {
		if !d.Valid() {
			return fmt.Errorf("%w: invalid provenance data class %q", ErrInvalidRequest, d)
		}
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
	Name        string
	Model       string
	Local       bool
	DataClasses []DataClass
	Purposes    []Purpose
	// MaxContextTokens bounds input+output. Zero means unknown (not checked).
	MaxContextTokens   int
	MaxOutputTokens    int
	InputPricePerMTok  int64
	OutputPricePerMTok int64
	// Endpoint is the provider's transport identity. A provider declared Local
	// must use a loopback/unix/in-process endpoint; Router.New rejects a "local"
	// provider pointed at a remote URL. An empty endpoint is an in-process
	// provider (for example StaticProvider).
	Endpoint string
	// ToolCalling/Vision/StructuredOutput describe optional capabilities.
	ToolCalling      bool
	Vision           bool
	StructuredOutput bool
	ReasoningMode    string
	LocalKVPrivacy   bool
	// Strength is the planning/analysis preference; higher is stronger.
	Strength int
}

// IsLocal reports whether the provider's transport is local. Locality is a
// property of the transport, not a self-declared boolean: a non-empty remote
// endpoint is never local even if Local=true.
func (i ProviderInfo) IsLocal() bool {
	if endpointIsLocal(i.Endpoint) {
		return true
	}
	if i.Endpoint != "" {
		return false
	}
	return i.Local
}

// endpointIsLocal accepts loopback hosts, unix/named-pipe/static schemes and
// localhost. Anything else is treated as remote.
func endpointIsLocal(endpoint string) bool {
	e := strings.ToLower(strings.TrimSpace(endpoint))
	if e == "" {
		return false
	}
	for _, prefix := range []string{"unix://", "npipe://", "static://", "stdio://"} {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	host := e
	if u, err := url.Parse(e); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	} else if h, _, err := net.SplitHostPort(e); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().IsLoopback()
	}
	return false
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

func (i ProviderInfo) SupportsCapability(c Capability) bool {
	switch c {
	case CapabilityToolCalling:
		return i.ToolCalling
	case CapabilityVision:
		return i.Vision
	case CapabilityStructuredOutput:
		return i.StructuredOutput
	default:
		return false
	}
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
