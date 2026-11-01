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

func TestBuildModelPlanRemoteAllowedWithoutEscalationStaysLocal(t *testing.T) {
	plan, err := buildModelPlan(modelOptions{
		Purpose: "PLAN", DataClass: "INTERNAL", MaxTokens: 1000, Budget: 100,
		ModelEgress: "REMOTE_ALLOWED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) == 0 || !plan.Candidates[0].Local {
		t.Fatalf("PLAN candidates = %+v, want a local provider first", plan.Candidates)
	}
	// REMOTE_ALLOWED is a ceiling, not an escalation decision: without a
	// reason the router must not treat a third-party provider as a fallback.
	for _, c := range plan.Candidates {
		if c.Provider == "remote-strong" {
			t.Fatalf("REMOTE_ALLOWED without escalation included the remote provider: %+v", plan.Candidates)
		}
	}
}

func TestBuildModelPlanEscalationPrefersRemote(t *testing.T) {
	plan, err := buildModelPlan(modelOptions{
		Purpose: "PLAN", DataClass: "INTERNAL", MaxTokens: 1000, Budget: 100,
		ModelEgress: "REMOTE_ALLOWED", Escalation: "STALLED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) == 0 || plan.Candidates[0].Provider != "remote-strong" {
		t.Fatalf("escalated PLAN candidates = %+v, want remote-strong first", plan.Candidates)
	}
}

func TestBuildModelPlanUnknownEgressStaysLocal(t *testing.T) {
	plan, err := buildModelPlan(modelOptions{
		Purpose: "ANALYZE", DataClass: "INTERNAL", MaxTokens: 1000, Budget: 100,
		Escalation: "STALLED",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Candidates {
		if !c.Local {
			t.Fatalf("UNKNOWN egress included remote provider %s", c.Provider)
		}
	}
	if plan.ModelEgress != "LOCAL_ONLY" {
		t.Fatalf("effective egress = %q, want LOCAL_ONLY", plan.ModelEgress)
	}
}

func TestBuildModelPlanRejectsInvalidPurpose(t *testing.T) {
	if _, err := buildModelPlan(modelOptions{Purpose: "NOPE", DataClass: "PUBLIC", MaxTokens: 1}); err == nil {
		t.Fatal("invalid purpose should fail")
	}
}

func TestParseModelOptions(t *testing.T) {
	opts, err := parseModelOptions([]string{
		"-purpose", "code", "-data-class", "public", "-max-tokens", "100", "-budget", "5",
		"-model-egress", "remote_allowed", "-redacted", "-escalation", "stalled",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Purpose != "code" || opts.DataClass != "public" || opts.MaxTokens != 100 || opts.Budget != 5 {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.ModelEgress != "remote_allowed" || !opts.Redacted || opts.Escalation != "stalled" {
		t.Fatalf("egress opts = %+v", opts)
	}
	if _, err := parseModelOptions([]string{"-max-tokens", "0"}); err == nil {
		t.Fatal("zero max tokens should fail")
	}
}
