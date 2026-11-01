package reliability

import (
	"fmt"
	"sort"
	"strings"
)

// FailureReportOptions is the non-secret repair information a tool contract may
// expose to the model. It never includes raw failed arguments.
type FailureReportOptions struct {
	// AllowedFields lists the field names the tool accepts.
	AllowedFields []string
	// AllowedValues lists accepted values per field.
	AllowedValues map[string][]string
	// MissingFields lists required fields that were absent.
	MissingFields []string
	// NextActions are stable action labels such as read_evidence or handoff.
	NextActions []string
	// Message is a short, already-safe explanation. It is scrubbed defensively.
	Message     string
	Retryable   bool
	Attempt     int
	MaxAttempts int
}

// FailureReport is the runtime-generated, model-facing description of a failed
// call. It is built from a FailureSignature (tool + error class + argument hash)
// and allowed repair information. It deliberately holds no raw arguments, so
// rendering it cannot repeat the call that just failed.
type FailureReport struct {
	Signature     FailureSignature
	AllowedFields []string
	AllowedValues map[string][]string
	MissingFields []string
	NextActions   []string
	Message       string
	Retryable     bool
	Attempt       int
	MaxAttempts   int
}

// NewFailureReport validates and sanitizes a failure report.
func NewFailureReport(sig FailureSignature, opts FailureReportOptions) (FailureReport, error) {
	sig = NormalizeFailureSignature(sig)
	if err := sig.Validate(); err != nil {
		return FailureReport{}, err
	}
	r := FailureReport{
		Signature:     sig,
		AllowedFields: sanitizeList(opts.AllowedFields),
		MissingFields: sanitizeList(opts.MissingFields),
		NextActions:   sanitizeList(opts.NextActions),
		Message:       compactText(scrubText(opts.Message)),
		Retryable:     opts.Retryable,
		Attempt:       opts.Attempt,
		MaxAttempts:   opts.MaxAttempts,
	}
	if len(opts.AllowedValues) > 0 {
		r.AllowedValues = make(map[string][]string, len(opts.AllowedValues))
		for field, values := range opts.AllowedValues {
			key := compactText(scrubText(field))
			if key == "" {
				continue
			}
			r.AllowedValues[key] = sanitizeList(values)
		}
	}
	return r, nil
}

// Render returns the deterministic model-facing text.
func (r FailureReport) Render() string {
	var b strings.Builder
	b.WriteString("MODEL_FACING_FAILURE\n")
	b.WriteString("tool=" + orNone(r.Signature.ToolName) + "\n")
	b.WriteString("error_class=" + orNone(r.Signature.ErrorClass) + "\n")
	b.WriteString("args_fingerprint=" + shortHash(r.Signature.ArgsHash) + "\n")
	if r.Message != "" {
		b.WriteString("message=" + r.Message + "\n")
	}
	if len(r.MissingFields) > 0 {
		b.WriteString("missing_fields=" + strings.Join(r.MissingFields, ", ") + "\n")
	}
	if len(r.AllowedFields) > 0 {
		b.WriteString("allowed_fields=" + strings.Join(r.AllowedFields, ", ") + "\n")
	}
	if len(r.AllowedValues) > 0 {
		keys := make([]string, 0, len(r.AllowedValues))
		for k := range r.AllowedValues {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("allowed_values." + k + "=" + strings.Join(r.AllowedValues[k], "|") + "\n")
		}
	}
	if len(r.NextActions) > 0 {
		b.WriteString("next_actions=" + strings.Join(r.NextActions, ", ") + "\n")
	}
	b.WriteString(fmt.Sprintf("retryable=%t\n", r.Retryable))
	if r.Attempt > 0 || r.MaxAttempts > 0 {
		b.WriteString(fmt.Sprintf("attempt=%d/%d\n", r.Attempt, r.MaxAttempts))
	}
	return b.String()
}

// String implements fmt.Stringer.
func (r FailureReport) String() string { return r.Render() }

func sanitizeList(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = compactText(scrubText(item))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
