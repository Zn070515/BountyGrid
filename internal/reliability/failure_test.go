package reliability

import (
	"errors"
	"strings"
	"testing"
)

func TestFailureReportDoesNotEchoRawFailedArguments(t *testing.T) {
	rawArgs := []byte(`{"password":"hunter2","token":"abcdefgh12345678"}`)
	sig := NewFailureSignature("read_scope", "INVALID_ARGUMENT", rawArgs)
	report, err := NewFailureReport(sig, FailureReportOptions{
		AllowedFields: []string{"asset", "method"},
		AllowedValues: map[string][]string{
			"method": {"GET", "HEAD"},
			"asset":  {"asset-1"},
		},
		MissingFields: []string{"asset"},
		NextActions:   []string{"retry_with_valid_arguments", "read_evidence"},
		Message:       "the method is not allowed for token=abcdefgh12345678",
		Retryable:     true,
		Attempt:       1,
		MaxAttempts:   2,
	})
	if err != nil {
		t.Fatalf("NewFailureReport: %v", err)
	}
	out := report.Render()
	for _, secret := range []string{"hunter2", "abcdefgh12345678", `"password"`} {
		if strings.Contains(out, secret) {
			t.Fatalf("failure report echoed raw failed argument %q:\n%s", secret, out)
		}
	}
	for _, want := range []string{
		"MODEL_FACING_FAILURE",
		"tool=read_scope",
		"error_class=INVALID_ARGUMENT",
		"args_fingerprint=",
		"missing_fields=asset",
		"allowed_fields=asset, method",
		"allowed_values.asset=asset-1",
		"allowed_values.method=GET|HEAD",
		"next_actions=retry_with_valid_arguments, read_evidence",
		"retryable=true",
		"attempt=1/2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("failure report missing %q:\n%s", want, out)
		}
	}
	if again := report.Render(); again != out {
		t.Fatal("failure report rendering is not deterministic")
	}
}

func TestFailureReportRejectsInvalidSignature(t *testing.T) {
	_, err := NewFailureReport(FailureSignature{}, FailureReportOptions{})
	if !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("want ErrInvalidClaim, got %v", err)
	}
}

func TestFailureReportSanitizesAllowedValues(t *testing.T) {
	sig := NewFailureSignature("tool", "INVALID_ARGUMENT", []byte(`{"a":1}`))
	report, err := NewFailureReport(sig, FailureReportOptions{
		AllowedFields: []string{"asset", "asset", " "},
		AllowedValues: map[string][]string{
			"b": {"2"},
			"a": {"1"},
			"":  {"ignored"},
		},
		NextActions: []string{"a", "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.AllowedFields) != 1 || report.AllowedFields[0] != "asset" {
		t.Fatalf("duplicate allowed fields not sanitized: %v", report.AllowedFields)
	}
	if len(report.NextActions) != 1 {
		t.Fatalf("duplicate next actions not sanitized: %v", report.NextActions)
	}
	out := report.Render()
	if strings.Index(out, "allowed_values.a=") > strings.Index(out, "allowed_values.b=") {
		t.Fatalf("allowed values are not rendered in stable order:\n%s", out)
	}
	if strings.Contains(out, "allowed_values.=") {
		t.Fatalf("empty allowed-values key was rendered:\n%s", out)
	}
}
