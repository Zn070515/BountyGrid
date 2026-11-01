package reliability

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func contextSession(t *testing.T) *ResearchSession {
	t.Helper()
	s := NewResearchSession("sess-ctx", "task-ctx", testNow)
	s.ProgramID = "prog-ctx"
	s.Objective = "determine whether the export endpoint is authorized"
	s.Budget = Budget{
		MaxSteps: 20, StepsUsed: 2,
		MaxToolCalls: 10, ToolCallsUsed: 3,
		MaxTokens: 20000, TokensUsed: 1000,
	}

	fact, err := NewFactRecord("fact-ctx", Claim{
		Level: LevelObservation, Statement: "the export endpoint returns 200",
		Evidence: []domain.EvidenceID{"ev-1"}, Confidence: 0.9,
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddFact(fact); err != nil {
		t.Fatal(err)
	}

	if err := s.AddHypothesis(Hypothesis{
		ID: "hyp-ctx", Statement: "IDOR lets user B export user A's data",
		Evidence: []domain.EvidenceID{"ev-2"}, Confidence: 0.6,
		Status: HypothesisOpen, CreatedAt: testNow, UpdatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFailedHypothesis(Hypothesis{
		ID: "hyp-failed", Statement: "export is anonymous", Status: HypothesisRefuted,
		Refutation: "the endpoint returns 401 without a session",
		CreatedAt:  testNow, UpdatedAt: testNow,
	}, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 6; i++ {
		obs := NewObservation("tool", []byte(fmt.Sprintf(`{"i":%d}`, i)),
			fmt.Sprintf("response-%d", i), fmt.Sprintf("observation %d", i),
			[]domain.EvidenceID{domain.EvidenceID(fmt.Sprintf("ev-obs-%d", i))},
			testNow.Add(time.Duration(i)*time.Minute))
		if err := s.AddObservation(obs); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("fixture session invalid: %v", err)
	}
	return s
}

func contextAuthorization() Authorization {
	return Authorization{
		AccountID:      "acct-1",
		ProgramID:      "prog-ctx",
		AllowedActions: []domain.Action{domain.ActionViewRules, domain.ActionPassiveAnalysis},
		ScopeRevision:  "scope-rev-1",
		ExpiresAt:      testNow.Add(time.Hour),
		Summary:        "read-only analysis is authorized",
	}
}

func contextEvidence() []EvidenceView {
	return []EvidenceView{
		{ID: "ev-1", Kind: "page", Summary: "export response body", ObservedAt: testNow},
		{ID: "ev-2", Kind: "page", Summary: "second account response", ObservedAt: testNow},
	}
}

func TestCompileHasAllSectionsInOrder(t *testing.T) {
	ctx, err := NewDefaultContextCompiler().Compile(
		t.Context(), contextSession(t), contextAuthorization(), contextEvidence(),
		ContextBudget{MaxChars: 8000},
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	order := []string{
		SectionTask, SectionAuthorization, SectionKnownFacts, SectionFailed,
		SectionHypotheses, SectionEvidence, SectionObservations, SectionActions, SectionBudget,
	}
	last := -1
	for _, name := range order {
		idx := strings.Index(ctx.Text, "## "+name)
		if idx < 0 {
			t.Fatalf("section %q missing from context:\n%s", name, ctx.Text)
		}
		if idx <= last {
			t.Fatalf("section %q out of order:\n%s", name, ctx.Text)
		}
		last = idx
		if _, ok := ctx.Section(name); !ok {
			t.Fatalf("structured section %q missing", name)
		}
	}
	for _, want := range []string{
		"fact-ctx", "hyp-ctx", "hyp-failed", "export response body",
		"VIEW_RULES", "steps=2/20",
	} {
		if !strings.Contains(ctx.Text, want) {
			t.Fatalf("context missing %q:\n%s", want, ctx.Text)
		}
	}
	if ctx.Chars != utf8.RuneCountInString(ctx.Text) {
		t.Fatalf("chars = %d, runes = %d", ctx.Chars, utf8.RuneCountInString(ctx.Text))
	}
	if ctx.Tokens != (ctx.Chars+3)/4 {
		t.Fatalf("tokens = %d, chars = %d", ctx.Tokens, ctx.Chars)
	}
	if ctx.Hash == "" {
		t.Fatal("context hash is empty")
	}
}

func TestCompileEnforcesCharacterBudget(t *testing.T) {
	s := contextSession(t)
	// Make the observation section large enough that the budget must trim.
	for i := 0; i < 8; i++ {
		obs := NewObservation("tool2", []byte(fmt.Sprintf(`{"j":%d}`, i)),
			fmt.Sprintf("long-response-%d", i), strings.Repeat("x", 500),
			nil, testNow.Add(time.Duration(10+i)*time.Minute))
		if err := s.AddObservation(obs); err != nil {
			t.Fatal(err)
		}
	}
	compiler := NewContextCompiler(ContextCompilerOptions{
		MaxObservations: 3, MaxObservationChars: 80,
	})
	const budget = 900
	ctx, err := compiler.Compile(t.Context(), s, contextAuthorization(), contextEvidence(),
		ContextBudget{MaxChars: budget})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if ctx.Chars > budget {
		t.Fatalf("chars %d exceeds budget %d", ctx.Chars, budget)
	}
	if !strings.Contains(ctx.Text, "older observation(s) omitted") {
		t.Fatalf("expected omitted-observation marker:\n%s", ctx.Text)
	}
	if ctx.OmittedObservations == 0 {
		t.Fatal("expected omitted observation count")
	}
}

func TestCompileEnforcesTokenBudget(t *testing.T) {
	compiler := NewContextCompiler(ContextCompilerOptions{MaxObservations: 2, MaxObservationChars: 60})
	const maxTokens = 200
	ctx, err := compiler.Compile(t.Context(), contextSession(t), contextAuthorization(), contextEvidence(),
		ContextBudget{MaxTokens: maxTokens})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if ctx.Tokens > maxTokens {
		t.Fatalf("tokens %d exceed budget %d", ctx.Tokens, maxTokens)
	}
	if ctx.Chars > maxTokens*4 {
		t.Fatalf("chars %d exceed derived char cap %d", ctx.Chars, maxTokens*4)
	}
}

func TestCompileRejectsImpossibleBudget(t *testing.T) {
	_, err := NewDefaultContextCompiler().Compile(
		t.Context(), contextSession(t), contextAuthorization(), contextEvidence(),
		ContextBudget{MaxChars: 40},
	)
	if !errors.Is(err, ErrContextBudgetInvalid) {
		t.Fatalf("want ErrContextBudgetInvalid, got %v", err)
	}
}

func TestEvidenceIDsArePreservedAndStable(t *testing.T) {
	compiler := NewDefaultContextCompiler()
	s := contextSession(t)
	first, err := compiler.Compile(t.Context(), s, contextAuthorization(), contextEvidence(),
		ContextBudget{MaxChars: 8000})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.EvidenceID{"ev-1", "ev-2", "ev-obs-0", "ev-obs-5"} {
		if !strings.Contains(first.Text, string(id)) {
			t.Fatalf("evidence id %s missing from context:\n%s", id, first.Text)
		}
		found := false
		for _, got := range first.EvidenceIDs {
			if got == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("evidence id %s missing from EvidenceIDs: %v", id, first.EvidenceIDs)
		}
	}
	second, err := compiler.Compile(t.Context(), s, contextAuthorization(), contextEvidence(),
		ContextBudget{MaxChars: 8000})
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != second.Hash {
		t.Fatal("context hash should be stable for identical inputs")
	}
	if fmt.Sprint(first.EvidenceIDs) != fmt.Sprint(second.EvidenceIDs) {
		t.Fatal("evidence IDs should be stable for identical inputs")
	}
}

func TestContextHashChangesWithEvidence(t *testing.T) {
	compiler := NewDefaultContextCompiler()
	base, err := compiler.Compile(t.Context(), contextSession(t), contextAuthorization(), contextEvidence(),
		ContextBudget{MaxChars: 8000})
	if err != nil {
		t.Fatal(err)
	}
	extra := append(contextEvidence(), EvidenceView{
		ID: "ev-extra", Kind: "page", Summary: "an additional evidence view", ObservedAt: testNow,
	})
	changed, err := compiler.Compile(t.Context(), contextSession(t), contextAuthorization(), extra,
		ContextBudget{MaxChars: 8000})
	if err != nil {
		t.Fatal(err)
	}
	if base.Hash == changed.Hash {
		t.Fatal("context hash should change when evidence changes")
	}
}

func TestDuplicateObservationsAreCompacted(t *testing.T) {
	s := NewResearchSession("sess-dup-obs", "task", testNow)
	params := []byte(`{"asset":"a"}`)
	first := NewObservation("tool", params, "same response", "duplicate body", nil, testNow)
	second := NewObservation("tool", params, "same response", "duplicate body", nil, testNow.Add(time.Minute))
	if err := s.AddObservation(first); err != nil {
		t.Fatal(err)
	}
	if err := s.AddObservation(second); err != nil {
		t.Fatal(err)
	}
	ctx, err := NewDefaultContextCompiler().Compile(t.Context(), s, contextAuthorization(), nil,
		ContextBudget{MaxChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx.Text, "duplicate observation(s) omitted") {
		t.Fatalf("expected duplicate marker:\n%s", ctx.Text)
	}
	if got := strings.Count(ctx.Text, "duplicate body"); got != 1 {
		t.Fatalf("duplicate observation rendered %d times:\n%s", got, ctx.Text)
	}
}

func TestOversizedObservationIsTruncated(t *testing.T) {
	s := NewResearchSession("sess-big-obs", "task", testNow)
	content := strings.Repeat("A", 500) + "ENDMARKER"
	obs := NewObservation("tool", []byte(`{"a":1}`), "response", content, nil, testNow)
	if err := s.AddObservation(obs); err != nil {
		t.Fatal(err)
	}
	compiler := NewContextCompiler(ContextCompilerOptions{MaxObservationChars: 80})
	ctx, err := compiler.Compile(t.Context(), s, contextAuthorization(), nil,
		ContextBudget{MaxChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ctx.Text, "ENDMARKER") {
		t.Fatalf("oversized observation was not truncated:\n%s", ctx.Text)
	}
	if !strings.Contains(ctx.Text, "[truncated]") {
		t.Fatalf("expected truncation marker:\n%s", ctx.Text)
	}
}

func TestEvidenceSummaryIsCuratedNotRaw(t *testing.T) {
	compiler := NewContextCompiler(ContextCompilerOptions{MaxEvidenceChars: 40})
	evidence := []EvidenceView{{
		ID: "ev-long", Kind: "page",
		Summary: strings.Repeat("raw-page-", 100) + "ENDMARKER",
	}}
	ctx, err := compiler.Compile(t.Context(), NewResearchSession("s", "t", testNow),
		contextAuthorization(), evidence, ContextBudget{MaxChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ctx.Text, "ENDMARKER") {
		t.Fatalf("evidence summary was not truncated:\n%s", ctx.Text)
	}
	if !strings.Contains(ctx.Text, "ev-long") {
		t.Fatalf("stable evidence id missing:\n%s", ctx.Text)
	}
}

func TestExpiredAuthorizationIsRenderedAsExpired(t *testing.T) {
	auth := contextAuthorization()
	auth.ExpiresAt = testNow.Add(-time.Minute)
	compiler := NewContextCompiler(ContextCompilerOptions{Now: func() time.Time { return testNow }})
	ctx, err := compiler.Compile(t.Context(), contextSession(t), auth, contextEvidence(),
		ContextBudget{MaxChars: 8000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx.Text, "expired=true") {
		t.Fatalf("expired authorization should be explicit:\n%s", ctx.Text)
	}
}
