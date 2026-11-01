// Package report builds platform report drafts from a verified Finding. It
// only assembles and validates text/fields; it never submits, and it never
// accepts the platform agreement on the user's behalf.
package report

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var (
	ErrNotVerified       = errors.New("report: finding is not verified")
	ErrInvalidInput      = errors.New("report: invalid input")
	ErrInvalidAttachment = errors.New("report: invalid attachment")
)

// Fields are the platform-neutral draft fields. Empty values are reported in
// Result.Missing based on the platform's required set.
type Fields struct {
	Title       string `json:"title"`
	AssetURL    string `json:"asset_url"`
	VulnType    string `json:"vuln_type"`
	Severity    string `json:"severity"`
	Summary     string `json:"summary"`
	Steps       string `json:"steps"`
	PoC         string `json:"poc"`
	Impact      string `json:"impact"`
	Remediation string `json:"remediation"`

	TaskID      string `json:"task_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`

	Vendor   string `json:"vendor,omitempty"`
	Category string `json:"category,omitempty"`
	Activity string `json:"activity,omitempty"`
	Industry string `json:"industry,omitempty"`
	Region   string `json:"region,omitempty"`
	Contact  string `json:"contact,omitempty"`
}

type AIUse struct {
	Assisted            bool     `json:"assisted"`
	ModelName           string   `json:"model_name,omitempty"`
	ModelVersion        string   `json:"model_version,omitempty"`
	ToolName            string   `json:"tool_name,omitempty"`
	HumanVerification   string   `json:"human_verification,omitempty"`
	GeneratedSections   []string `json:"ai_generated_sections,omitempty"`
	HumanEditedSections []string `json:"human_edited_sections,omitempty"`
}

type Input struct {
	Finding      domain.Finding
	Program      domain.Program
	Verification domain.Verification
	Fields       Fields
	AI           AIUse
	Attachments  []string
	Now          time.Time
}

type Result struct {
	Draft   domain.ReportDraft
	Missing []string
	Ready   bool
}

var commonRequired = []string{
	"title", "asset_url", "vuln_type", "severity", "summary",
	"steps", "poc", "impact", "remediation",
}

func requiredFields(platform domain.Platform) []string {
	out := append([]string{}, commonRequired...)
	switch platform {
	case domain.PlatformXianzhi:
		out = append(out, "task_id", "account_name")
	case domain.PlatformButian:
		out = append(out, "vendor", "category")
	}
	return out
}

// Build assembles a DRAFT. It refuses a finding whose Verification is not
// VERIFIED, but a missing platform field is not an error: the draft is still
// returned with Missing and Ready=false.
func Build(in Input) (Result, error) {
	if in.Program.ID == "" || in.Program.Platform == "" {
		return Result{}, fmt.Errorf("%w: program id/platform required", ErrInvalidInput)
	}
	if in.Finding.ID == "" {
		return Result{}, fmt.Errorf("%w: finding id required", ErrInvalidInput)
	}
	if in.Finding.ProgramID != in.Program.ID {
		return Result{}, fmt.Errorf("%w: finding/program mismatch", ErrInvalidInput)
	}
	if in.Verification.Verdict != domain.VerdictVerified {
		return Result{}, fmt.Errorf("%w: verification is %s", ErrNotVerified, in.Verification.Verdict)
	}
	if in.Verification.FindingID != in.Finding.ID {
		return Result{}, fmt.Errorf("%w: verification/finding mismatch", ErrInvalidInput)
	}
	if err := in.Verification.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	// An AI-assisted report must point at a verification that actually recorded
	// the human trace; the AIUse block alone is not proof.
	if in.AI.Assisted && (!in.Verification.AIAssisted || !in.Verification.HumanVerified ||
		in.Verification.HumanVerificationRef == "") {
		return Result{}, fmt.Errorf("%w: AI-assisted report requires a persisted human-verification trace", ErrInvalidInput)
	}
	if len(in.Verification.Evidence) == 0 {
		return Result{}, fmt.Errorf("%w: verification has no evidence", ErrInvalidInput)
	}
	if err := validateAttachments(in.Program.Platform, in.Attachments); err != nil {
		return Result{}, err
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	fields := buildFields(in)
	disclosure := buildDisclosure(in)
	missing := missingFields(in.Program.Platform, fields, disclosure, in.AI, in.Program, len(in.Verification.Evidence))

	draft := domain.ReportDraft{
		ID:           fmt.Sprintf("draft/%s/%s", findingKey(in.Finding.ID), now.UTC().Format("20060102T150405.000000000")),
		FindingID:    in.Finding.ID,
		ProgramID:    in.Program.ID,
		Fields:       fields,
		AIDisclosure: disclosure,
		Attachments:  append([]string{}, in.Attachments...),
		Status:       "DRAFT",
		CreatedAt:    now,
	}
	if err := draft.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return Result{Draft: draft, Missing: missing, Ready: len(missing) == 0}, nil
}

func buildFields(in Input) map[string]string {
	f := in.Fields
	if strings.TrimSpace(f.Title) == "" {
		f.Title = in.Finding.Title
	}
	if strings.TrimSpace(f.VulnType) == "" {
		f.VulnType = in.Finding.VulnType
	}
	if strings.TrimSpace(f.Severity) == "" {
		f.Severity = in.Finding.Severity
	}
	if strings.TrimSpace(f.TaskID) == "" {
		f.TaskID = in.Program.NativeID
	}
	if strings.TrimSpace(f.Vendor) == "" {
		f.Vendor = in.Program.Name
	}
	out := map[string]string{}
	for k, v := range map[string]string{
		"title": f.Title, "asset_url": f.AssetURL, "vuln_type": f.VulnType,
		"severity": f.Severity, "summary": f.Summary, "steps": f.Steps,
		"poc": f.PoC, "impact": f.Impact, "remediation": f.Remediation,
		"task_id": f.TaskID, "account_name": f.AccountName,
		"vendor": f.Vendor, "category": f.Category, "activity": f.Activity,
		"industry": f.Industry, "region": f.Region, "contact": f.Contact,
	} {
		if v = strings.TrimSpace(v); v != "" {
			out[k] = v
		}
	}
	return out
}

func buildDisclosure(in Input) map[string]string {
	out := map[string]string{}
	if !in.AI.Assisted {
		return out
	}
	out["ai_assisted"] = "true"
	put := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			out[k] = v
		}
	}
	put("model_name", in.AI.ModelName)
	put("model_version", in.AI.ModelVersion)
	put("tool_name", in.AI.ToolName)
	put("human_verification", in.AI.HumanVerification)
	if len(in.AI.GeneratedSections) > 0 {
		put("ai_generated_sections", strings.Join(in.AI.GeneratedSections, ", "))
	}
	if len(in.AI.HumanEditedSections) > 0 {
		put("human_edited_sections", strings.Join(in.AI.HumanEditedSections, ", "))
	}
	return out
}

func missingFields(platform domain.Platform, fields, disclosure map[string]string, ai AIUse, program domain.Program, evidence int) []string {
	var missing []string
	for _, key := range requiredFields(platform) {
		if strings.TrimSpace(fields[key]) == "" {
			missing = append(missing, key)
		}
	}
	if evidence == 0 {
		missing = append(missing, "evidence")
	}
	if ai.Assisted {
		if strings.TrimSpace(disclosure["human_verification"]) == "" {
			missing = append(missing, "ai_human_verification")
		}
		if platform == domain.PlatformButian && program.Type == domain.TypeCampusProgram {
			for _, key := range []string{"model_name", "model_version", "tool_name"} {
				if strings.TrimSpace(disclosure[key]) == "" {
					missing = append(missing, "ai_"+key)
				}
			}
		}
	}
	return missing
}

func validateAttachments(platform domain.Platform, attachments []string) error {
	for _, a := range attachments {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("%w: empty attachment path", ErrInvalidAttachment)
		}
		switch platform {
		case domain.PlatformVulbox:
			if !allowedExt(a, ".rar", ".zip", ".doc", ".docx", ".pdf", ".xls", ".xlsx") {
				return fmt.Errorf("%w: %s is not a Vulbox attachment type", ErrInvalidAttachment, a)
			}
		case domain.PlatformButian:
			if !allowedExt(a, ".zip", ".rar") {
				return fmt.Errorf("%w: %s is not a Butian archive", ErrInvalidAttachment, a)
			}
		}
	}
	return nil
}

func allowedExt(path string, exts ...string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range exts {
		if ext == e {
			return true
		}
	}
	return false
}

func findingKey(id domain.FindingID) string {
	if id == "" {
		return "unknown"
	}
	return string(id)
}
