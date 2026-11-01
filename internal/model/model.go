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
	// ErrStableCallIDRequired means worker mode was configured to require an
	// explicit, deterministic CallID and the request did not carry one.
	ErrStableCallIDRequired = errors.New("model: worker mode requires a stable call ID")
	// ErrInvalidCallTransition means a late/stale writer tried to overwrite a
	// terminal logical call (COMPLETED/AMBIGUOUS/FAILED_BEFORE_SEND), or to move
	// a call backwards. The recorded status is authoritative.
	ErrInvalidCallTransition = errors.New("model: invalid call status transition")
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

// EscalationReason is the only mechanism that lets local-first routing prefer a
// stronger remote provider. An empty reason means no escalation: the router
// stays within the local tiers.
type EscalationReason string

const (
	EscalationNone                 EscalationReason = ""
	EscalationStalled              EscalationReason = "STALLED"
	EscalationLowConfidence        EscalationReason = "LOW_CONFIDENCE"
	EscalationHighValueTask        EscalationReason = "HIGH_VALUE_TASK"
	EscalationVerificationConflict EscalationReason = "VERIFICATION_CONFLICT"
)

func (e EscalationReason) Valid() bool {
	switch e {
	case EscalationNone, EscalationStalled, EscalationLowConfidence, EscalationHighValueTask, EscalationVerificationConflict:
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

// ModelPolicy is the authoritative routing policy for one request. It is
// returned by a PolicyResolver backed by the Program/evidence store. The
// ModelRequest.ModelEgress, Redacted and Escalation fields are hints for CLIs
// and tests; the Router must never treat them as authority.
type ModelPolicy struct {
	// Egress is the program-level ceiling. Zero value/UNKNOWN means LOCAL_ONLY.
	Egress domain.ModelEgressPolicy
	// Redacted attests that this prompt/task was redacted for the request.
	Redacted bool
	// Escalation is the externally authorized escalation decision. Empty means
	// no escalation: third-party providers are not eligible at all.
	Escalation EscalationReason
}

// PolicyResolver returns the authoritative policy for a request. Implementations
// must derive Egress from the stored Program.ModelEgressPolicy, Redacted from a
// redaction attestation for this prompt/task, and Escalation from a trusted
// external escalation decision. If no resolver is configured the Router fails
// closed to LOCAL_ONLY with no escalation and no redaction.
type PolicyResolver interface {
	ResolveModelPolicy(ctx context.Context, req ModelRequest) (ModelPolicy, error)
}

// PolicyResolverFunc adapts a function to PolicyResolver. It is useful for
// tests, CLIs and store-backed adapters.
type PolicyResolverFunc func(ctx context.Context, req ModelRequest) (ModelPolicy, error)

func (f PolicyResolverFunc) ResolveModelPolicy(ctx context.Context, req ModelRequest) (ModelPolicy, error) {
	return f(ctx, req)
}

// ModelRequest is one routed model call. TaskID is optional; when present it
// links the call to a ResearchTask. StepID names the task step and, together
// with the prompt hash, produces a stable call ID for worker retries.
type ModelRequest struct {
	TaskID domain.TaskID `json:"task_id,omitempty"`
	// ProgramID identifies the program whose egress policy governs this call.
	// A production PolicyResolver should require it; it is optional here so
	// interactive/dry-run callers can still describe a local-only plan.
	ProgramID domain.ProgramID `json:"program_id,omitempty"`
	StepID    string           `json:"step_id,omitempty"`
	Purpose   Purpose       `json:"purpose"`
	DataClass DataClass     `json:"data_class"`
	// Provenance records the data classes of the inputs (evidence, task, source
	// files). The router uses max(DataClass, Provenance...) so a request cannot
	// under-classify sensitive content.
	Provenance []DataClass  `json:"provenance,omitempty"`
	Input      []Message    `json:"input"`
	MaxTokens  int          `json:"max_tokens"`
	Budget     domain.Money `json:"budget"`
	// ModelEgress is a caller hint for CLI/test plans. The Router never treats
	// it as authority; PolicyResolver supplies the effective policy. The zero
	// value and UNKNOWN behave as LOCAL_ONLY.
	ModelEgress domain.ModelEgressPolicy `json:"model_egress,omitempty"`
	// Redacted is a caller hint. The Router uses the PolicyResolver attestation
	// instead, so a forged true value cannot unlock remote egress.
	Redacted bool `json:"redacted,omitempty"`
	// Escalation is a caller hint. The Router only allows third-party providers
	// when the PolicyResolver authorizes escalation; a forged value is ignored.
	Escalation EscalationReason `json:"escalation,omitempty"`
	// CallID is an optional idempotency key. When set, a retry with the same ID
	// is rejected instead of paying for a second model call. Worker mode
	// requires it; use StableCallID.
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
	if !r.ModelEgress.Valid() {
		return fmt.Errorf("%w: invalid model egress policy %q", ErrInvalidRequest, r.ModelEgress)
	}
	if !r.Escalation.Valid() {
		return fmt.Errorf("%w: invalid escalation reason %q", ErrInvalidRequest, r.Escalation)
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

// ProviderTrust is the deployment/trust boundary of a model provider.
//
// The three kinds replace the old binary Local flag:
//
//   - IN_PROCESS: the model runs in this process (static/dry-run, or a
//     loopback/unix/stdio endpoint). Never leaves the machine.
//   - LOCAL_NETWORK: a trusted LAN endpoint (for example a Strata server on
//     another machine). It is not third-party cloud egress, but it is not the
//     same process either, so its IP literal must be inside an
//     operator-configured trusted CIDR.
//   - THIRD_PARTY: a cloud/remote API. Only eligible with an authoritative
//     escalation decision, and never for SENSITIVE data.
type ProviderTrust string

const (
	TrustInProcess    ProviderTrust = "IN_PROCESS"
	TrustLocalNetwork ProviderTrust = "LOCAL_NETWORK"
	TrustThirdParty   ProviderTrust = "THIRD_PARTY"
)

func (t ProviderTrust) Valid() bool {
	switch t {
	case TrustInProcess, TrustLocalNetwork, TrustThirdParty:
		return true
	default:
		return false
	}
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
	// Endpoint is the provider's transport identity. A loopback/unix/static
	// endpoint is IN_PROCESS. A LOCAL_NETWORK provider must use an IP literal
	// inside Router.Options.TrustedNetworks. An empty endpoint is an in-process
	// provider (for example StaticProvider) when Local is true.
	Endpoint string
	// Trust is the deployment/trust boundary. The zero value is inferred from
	// Local and Endpoint for legacy catalogs: a loopback/unix/static endpoint or
	// Local=true with no endpoint is IN_PROCESS, everything else is THIRD_PARTY.
	Trust ProviderTrust
	// ToolCalling/Vision/StructuredOutput describe optional capabilities.
	ToolCalling      bool
	Vision           bool
	StructuredOutput bool
	ReasoningMode    string
	LocalKVPrivacy   bool
	// Strength is the planning/analysis preference; higher is stronger.
	Strength int
}

// EffectiveTrust resolves the explicit Trust kind or, for legacy catalogs,
// infers it from Local/Endpoint. An explicit value always wins.
func (i ProviderInfo) EffectiveTrust() ProviderTrust {
	switch i.Trust {
	case TrustInProcess, TrustLocalNetwork, TrustThirdParty:
		return i.Trust
	}
	if endpointIsLocal(i.Endpoint) {
		return TrustInProcess
	}
	if i.Endpoint == "" {
		if i.Local {
			return TrustInProcess
		}
		return TrustThirdParty
	}
	return TrustThirdParty
}

// IsLocal reports whether the provider runs in this process. It is retained for
// legacy tests/callers; the Router uses IsLocalTrust/IsThirdParty so a trusted
// LAN provider is not mistaken for either a cloud provider or an in-process one.
func (i ProviderInfo) IsLocal() bool {
	return i.EffectiveTrust() == TrustInProcess
}

// IsThirdParty reports whether the provider is cloud/remote egress.
func (i ProviderInfo) IsThirdParty() bool {
	return i.EffectiveTrust() == TrustThirdParty
}

// IsLocalTrust reports whether the provider is inside the trusted boundary:
// an in-process endpoint, or a LOCAL_NETWORK endpoint whose IP literal is in
// one of the trusted CIDRs. A THIRD_PARTY provider is never trusted-local.
func (i ProviderInfo) IsLocalTrust(trusted []netip.Prefix) bool {
	switch i.EffectiveTrust() {
	case TrustInProcess:
		return i.Endpoint == "" || endpointIsLocal(i.Endpoint)
	case TrustLocalNetwork:
		ip, ok := endpointIP(i.Endpoint)
		return ok && trustedContains(trusted, ip)
	default:
		return false
	}
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

// endpointIP extracts an IP literal from a provider endpoint. It accepts a URL
// (http://10.0.0.5:8000), host:port (10.0.0.5:8000) and a bare literal
// (10.0.0.5). A hostname is not accepted: a LOCAL_NETWORK provider must pin an
// address rather than rely on DNS.
func endpointIP(endpoint string) (netip.Addr, bool) {
	e := strings.TrimSpace(endpoint)
	if e == "" {
		return netip.Addr{}, false
	}
	if u, err := url.Parse(e); err == nil && u.Hostname() != "" {
		return parseAddrHost(u.Hostname())
	}
	if h, _, err := net.SplitHostPort(e); err == nil {
		return parseAddrHost(h)
	}
	return parseAddrHost(e)
}

func parseAddrHost(host string) (netip.Addr, bool) {
	host = strings.Trim(host, "[]")
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), true
	}
	return netip.Addr{}, false
}

func trustedContains(prefixes []netip.Prefix, ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
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

// StableCallID derives the idempotency key for a worker step. The same task,
// step, purpose and prompt always produce the same ID, so a retried worker
// resumes the existing logical call instead of paying for a second one. An
// interactive/adhoc caller may keep using auto-generated IDs.
func StableCallID(taskID domain.TaskID, stepID string, purpose Purpose, promptHash string) string {
	h := sha256.New()
	for _, part := range []string{string(taskID), stepID, string(purpose), promptHash} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return "call/stable/" + hex.EncodeToString(h.Sum(nil))
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
