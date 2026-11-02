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

func TestParseModelOptionsCallFlags(t *testing.T) {
	opts, err := parseModelOptions([]string{
		"-call",
		"-endpoint", "http://127.0.0.1:8080/v1",
		"-provider-model", "qwen3.8-flash-next-iq3_s",
		"-provider-name", "strata",
		"-prompt", "hello",
		"-timeout", "30s",
		"-call-id", "call/test/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Call || opts.Endpoint != "http://127.0.0.1:8080/v1" ||
		opts.ProviderModel != "qwen3.8-flash-next-iq3_s" || opts.ProviderName != "strata" ||
		opts.Prompt != "hello" || opts.Timeout.Seconds() != 30 || opts.CallID != "call/test/1" {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseModelOptionsCallRequiresPrompt(t *testing.T) {
	if _, err := parseModelOptions([]string{"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m"}); err == nil {
		t.Fatal("-call without a prompt should fail")
	}
}

func TestParseModelOptionsCallRejectsBothPrompts(t *testing.T) {
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-prompt-file", "p.txt",
	}); err == nil {
		t.Fatal("-prompt with -prompt-file should fail")
	}
}

func TestParseModelOptionsNewFlagsRequireCall(t *testing.T) {
	for _, args := range [][]string{
		{"-endpoint", "http://127.0.0.1:8080/v1"},
		{"-provider-model", "m"},
		{"-prompt", "hi"},
		{"-persist"},
		{"-timeout", "5s"},
	} {
		if _, err := parseModelOptions(args); err == nil {
			t.Fatalf("%v without -call should fail", args)
		}
	}
}

func TestParseModelOptionsProgramRequiresPersist(t *testing.T) {
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-program", "vulbox/444",
	}); err == nil {
		t.Fatal("-program without -persist should fail")
	}
}

func TestParseModelOptionsCallRejectsModelEgress(t *testing.T) {
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-model-egress", "REMOTE_ALLOWED",
	}); err == nil {
		t.Fatal("-model-egress with -call should fail")
	}
}

func TestParseModelOptionsCallRejectsInvalidPurpose(t *testing.T) {
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-purpose", "NOPE",
	}); err == nil {
		t.Fatal("an invalid purpose with -call should fail")
	}
}

func TestParseModelOptionsProviderFormat(t *testing.T) {
	opts, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-provider-format", "anthropic",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.ProviderFormat != "anthropic" {
		t.Fatalf("provider format = %q", opts.ProviderFormat)
	}
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-provider-format", "gemini",
	}); err == nil {
		t.Fatal("an unknown provider format should fail")
	}
	if _, err := parseModelOptions([]string{"-provider-format", "anthropic"}); err == nil {
		t.Fatal("-provider-format without -call should fail")
	}
}

func TestParseModelOptionsThinkingAndTemperature(t *testing.T) {
	opts, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-thinking", "off", "-thinking-budget", "1024", "-temperature", "0.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Thinking != "off" || opts.ThinkingBudget != 1024 || opts.Temperature != 0.2 {
		t.Fatalf("opts = %+v", opts)
	}
	for _, args := range [][]string{
		{"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m", "-prompt", "hi", "-thinking", "maybe"},
		{"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m", "-prompt", "hi", "-thinking-budget", "-1"},
		{"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m", "-prompt", "hi", "-temperature", "-2"},
	} {
		if _, err := parseModelOptions(args); err == nil {
			t.Fatalf("%v should fail", args)
		}
	}
}

func TestParseModelOptionsAPIKeyEnvUnset(t *testing.T) {
	t.Setenv("BOUNTYGRID_TEST_KEY", "")
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-api-key-env", "BOUNTYGRID_TEST_KEY",
	}); err == nil {
		t.Fatal("an empty api key env should fail")
	}
}
