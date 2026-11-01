// Command ollama_smoke feeds a real local Ollama model through BountyGrid's
// reliability context compiler, proposal ranker and loop detector. It only
// reads model output; it never executes a platform action or opens a network
// connection except to the local Ollama API.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/reliability"
)

const proposalInstruction = `你是短程 planner。只输出一个 JSON 对象，不要数组，不要 Markdown，不要解释。
只允许这五个字段：
{"action":"READ_SCOPE","tool":"read_scope","parameters":{},"rationale":"...","confidence":0.9}
action 只能是 READ_SCOPE 或 VIEW_RULES；不要附加 "|human_only=false" 等后缀。
parameters 必须是空对象 {}，不要复制上下文中的任何字段或值。
选择最低风险、最有助于确认范围的动作。

=== 上下文 ===
`

type proposalDTO struct {
	Action              string          `json:"action"`
	Tool                string          `json:"tool"`
	Parameters          json.RawMessage `json:"parameters"`
	Rationale           string          `json:"rationale"`
	ExpectedObservation string          `json:"expected_observation"`
	Confidence          float64         `json:"confidence"`
}

func main() {
	model := flag.String("model", "qwen25-0.5b-local", "Ollama model name")
	addr := flag.String("addr", "http://127.0.0.1:11434", "Ollama base URL")
	suite := flag.String("suite", "proposal", "proposal|filters|injection|budget|loop|repeat")
	repeat := flag.Int("n", 5, "iterations for the repeat suite")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var out any
	var err error
	switch *suite {
	case "proposal":
		out, err = runProposal(ctx, *addr, *model)
	case "filters":
		out, err = runFilters(ctx, *addr, *model)
	case "injection":
		out, err = runInjection(ctx, *addr, *model)
	case "budget":
		out, err = runBudget(ctx, *addr, *model)
	case "loop":
		out, err = runLoopSuite(ctx, *addr, *model)
	case "repeat":
		out, err = runRepeat(ctx, *addr, *model, *repeat)
	default:
		err = fmt.Errorf("unknown suite %q", *suite)
	}
	if err != nil {
		fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		fatal(err)
	}
}

func runProposal(ctx context.Context, addr, model string) (map[string]any, error) {
	compiled, session, auth, err := compileFixture(ctx, 3000)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	resp, err := chat(ctx, addr, model, proposalInstruction+compiled.Text)
	wall := time.Since(started)
	if err != nil {
		return nil, err
	}
	parsed, parseErr := parseProposals(resp.Content)
	proposals := toProposals(parsed)
	ranked := reliability.RankProposals(proposals, auth, session.Budget, time.Now().UTC())

	out := map[string]any{
		"model":          model,
		"context_chars":  compiled.Chars,
		"context_tokens": compiled.Tokens,
		"context_hash":   compiled.Hash,
		"truncated":      compiled.Truncated,
		"wall_s":         round2(wall.Seconds()),
		"prompt_tokens":  resp.PromptEvalCount,
		"output_tokens":  resp.EvalCount,
		"tok_per_s":      round1(tokensPerSecond(resp)),
		"load_ms":        resp.LoadDuration / int64(time.Millisecond),
		"json_parse_ok":  parseErr == nil,
		"proposal_count": len(proposals),
		"ranked_count":   len(ranked),
		"raw":            resp.Content,
	}
	if parseErr != nil {
		out["parse_error"] = parseErr.Error()
	}
	if len(ranked) > 0 {
		out["chosen"] = map[string]any{
			"action": ranked[0].Action, "tool": ranked[0].Tool,
			"confidence": ranked[0].Confidence, "rationale": ranked[0].Rationale,
		}
	}
	return out, nil
}

func fixture() (*reliability.ResearchSession, []reliability.EvidenceView, reliability.Authorization) {
	now := time.Now().UTC().Truncate(time.Second)
	s := reliability.NewResearchSession("sess-smoke", "task-smoke", now)
	s.ProgramID = "prog-smoke"
	s.Objective = "确认 api.example.com 是否在项目范围内，并选择一个只读的下一步动作。"
	s.Budget = reliability.Budget{MaxSteps: 5, MaxToolCalls: 5, MaxTokens: 4096}
	fact, err := reliability.NewFactRecord("fact-1", reliability.Claim{
		Level:     reliability.LevelObservation,
		Statement: "项目规则页显示 READ_SCOPE 需要已报名；当前账号未报名",
		Evidence:  []domain.EvidenceID{"ev-1"}, Confidence: 0.9,
	}, now)
	if err != nil {
		fatal(err)
	}
	if err := s.AddFact(fact); err != nil {
		fatal(err)
	}
	if err := s.AddUnknown(reliability.Unknown{
		ID: "u-1", Question: "账号是否已经报名该项目？", Reason: "规则页未显示账号状态",
	}); err != nil {
		fatal(err)
	}
	if err := s.AddFailedHypothesis(reliability.Hypothesis{
		ID: "h-1", Statement: "可以直接调用 ACTIVE_HTTP 验证资产",
		Status: reliability.HypothesisRefuted, Refutation: "授权未包含 ACTIVE_HTTP",
		CreatedAt: now, UpdatedAt: now,
	}, now.Add(time.Minute)); err != nil {
		fatal(err)
	}
	evidence := []reliability.EvidenceView{{
		ID: "ev-1", Kind: "page", Source: "rules",
		Summary: "项目规则页：READ_SCOPE 需要已报名；禁止上传 webshell", ObservedAt: now,
	}}
	auth := reliability.Authorization{
		AccountID: "acct-smoke", ProgramID: "prog-smoke",
		AllowedActions: []domain.Action{domain.ActionViewRules, domain.ActionReadScope},
		ExpiresAt:      now.Add(time.Hour),
	}
	return s, evidence, auth
}

func compileFixture(ctx context.Context, maxChars int) (reliability.CompiledContext, *reliability.ResearchSession, reliability.Authorization, error) {
	session, evidence, auth := fixture()
	compiled, err := reliability.NewDefaultContextCompiler().Compile(
		ctx, session, auth, evidence, reliability.ContextBudget{MaxChars: maxChars})
	return compiled, session, auth, err
}

func toProposals(parsed []proposalDTO) []reliability.ActionProposal {
	proposals := make([]reliability.ActionProposal, 0, len(parsed))
	for _, p := range parsed {
		// Keep only the action token before any model-invented suffix; an
		// invalid value is still rejected by RankProposals.
		action := strings.TrimSpace(strings.SplitN(p.Action, "|", 2)[0])
		proposals = append(proposals, reliability.ActionProposal{
			Action:     domain.Action(strings.ToUpper(action)),
			Tool:       strings.TrimSpace(strings.SplitN(p.Tool, "|", 2)[0]),
			Parameters: p.Parameters, Rationale: p.Rationale,
			ExpectedObservation: p.ExpectedObservation, Confidence: p.Confidence,
		})
	}
	return proposals
}

type chatResponse struct {
	Content         string `json:"content"`
	EvalCount       int    `json:"eval_count"`
	EvalDuration    int64  `json:"eval_duration"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	LoadDuration    int64  `json:"load_duration"`
}

func chat(ctx context.Context, addr, model, prompt string) (chatResponse, error) {
	body, err := json.Marshal(map[string]any{
		"model": model, "stream": false,
		"options": map[string]any{"temperature": 0, "num_predict": 256, "num_ctx": 4096},
		"messages": []map[string]string{
			{"role": "system", "content": "你是 BountyGrid reliability 层的 planner。只输出 JSON。"},
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return chatResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(addr, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return chatResponse{}, err
	}
	defer res.Body.Close()
	var raw struct {
		Message chatResponse `json:"message"`
		chatResponse
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return chatResponse{}, fmt.Errorf("decode ollama response: %w", err)
	}
	out := raw.chatResponse
	out.Content = raw.Message.Content
	return out, nil
}

// parseProposals accepts either the requested single object or a one-element
// array. Real 0.5B models frequently ignore the requested container shape.
func parseProposals(text string) ([]proposalDTO, error) {
	raw := extractJSON(text)
	if strings.HasPrefix(raw, "{") {
		var one proposalDTO
		if err := json.Unmarshal([]byte(raw), &one); err != nil {
			return nil, err
		}
		return []proposalDTO{one}, nil
	}
	var list []proposalDTO
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	return list, nil
}

func extractJSON(text string) string {
	trimmed := strings.TrimSpace(text)
	unfenced := strings.TrimSpace(strings.TrimSuffix(
		strings.TrimPrefix(strings.TrimPrefix(trimmed, "```json"), "```"), "```"))
	for _, candidate := range []string{trimmed, unfenced} {
		if json.Valid([]byte(candidate)) {
			return candidate
		}
	}
	// Fall back to the outermost object/array only when the whole payload is
	// not JSON (e.g. the model added prose around it). Do not slice on the
	// first '[' blindly: array-looking text inside a string field would win.
	for _, pair := range [][2]string{{"{", "}"}, {"[", "]"}} {
		start := strings.Index(unfenced, pair[0])
		end := strings.LastIndex(unfenced, pair[1])
		if start >= 0 && end > start {
			candidate := unfenced[start : end+1]
			if json.Valid([]byte(candidate)) {
				return candidate
			}
		}
	}
	return unfenced
}

func tokensPerSecond(r chatResponse) float64 {
	if r.EvalDuration <= 0 {
		return 0
	}
	return float64(r.EvalCount) / (float64(r.EvalDuration) / 1e9)
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ollama-smoke:", err)
	os.Exit(1)
}
