package main

import (
	"testing"
)

func TestBuildModelPlanSensitiveUsesLocalOnly(t *testing.T) {
	plan, err := buildModelPlan(modelOptions{Purpose: "ANALYZE", DataClass: "SENSITIVE", MaxTokens: 1000, Budget: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) == 0 {
		t.Fatal("no local candidate for SENSITIVE data")
	}
	for _, c := range plan.Candidates {
		if !c.Local {
			t.Fatalf("SENSITIVE plan includes remote provider %s", c.Provider)
		}
	}
}

func TestBuildModelPlanPrefersRemoteForPlanning(t *testing.T) {
	plan, err := buildModelPlan(modelOptions{Purpose: "PLAN", DataClass: "INTERNAL", MaxTokens: 1000, Budget: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) == 0 || plan.Candidates[0].Provider != "remote-strong" {
		t.Fatalf("PLAN candidates = %+v, want remote-strong first", plan.Candidates)
	}
}

func TestBuildModelPlanRejectsInvalidPurpose(t *testing.T) {
	if _, err := buildModelPlan(modelOptions{Purpose: "NOPE", DataClass: "PUBLIC", MaxTokens: 1}); err == nil {
		t.Fatal("invalid purpose should fail")
	}
}

func TestParseModelOptions(t *testing.T) {
	opts, err := parseModelOptions([]string{"-purpose", "code", "-data-class", "public", "-max-tokens", "100", "-budget", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Purpose != "code" || opts.DataClass != "public" || opts.MaxTokens != 100 || opts.Budget != 5 {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseModelOptions([]string{"-max-tokens", "0"}); err == nil {
		t.Fatal("zero max tokens should fail")
	}
}
