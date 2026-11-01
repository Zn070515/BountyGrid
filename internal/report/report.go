// Package report builds platform report drafts from a verified Finding. It
// only assembles and validates text/fields; it never submits, and it never
// accepts the platform agreement on the user's behalf.
package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/redact"
)

var (
	ErrNotVerified       = errors.New("report: finding is not verified")
	ErrInvalidInput      = errors.New("report: invalid input")
	ErrInvalidAttachment = errors.New("report: invalid attachment")
	ErrSensitive         = errors.New("report: draft contains a sensitive value")
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

	Vendor     string `json:"vendor,omitempty"`
	Category   string `json:"category,omitempty"`
	AssetScope string `json:"asset_scope,omitempty"`
	Weight     string `json:"weight,omitempty"`
	Activity   string `json:"activity,omitempty"`
	Industry   string `json:"industry,omitempty"`
	Region     string `json:"region,omitempty"`
	// Contact is a reference (e.g. "platform account"), not a raw phone/email:
	// the database must not store full contact values.
	Contact string `json:"contact,omitempty"`
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
	// AttachmentsRoot, when set, enables strict attachment validation: the file
	// must resolve inside this os.Root, be non-empty and within
	// MaxAttachmentBytes, have magic bytes matching its extension, and pass the
	// sensitive-value scan. AttachmentEvidence gives each attachment its
	// evidence provenance; strict mode requires it.
	AttachmentsRoot    string
	MaxAttachmentBytes int64
	AttachmentEvidence map[string][]domain.EvidenceID
	Now                time.Time
}

type Result struct {
	Draft            domain.ReportDraft
	Missing          []string
	Ready            bool
	AttachmentHashes map[string]string
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
		// platform-audit/butian/notes.md §6 lists these as form fields.
		out = append(out, "vendor", "category", "asset_scope", "weight",
			"activity", "task_id", "industry", "region", "contact")
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
	if len(in.Verification.Evidence) == 0 {
		return Result{}, fmt.Errorf("%w: verification has no evidence", ErrInvalidInput)
	}
	attachmentHashes, err := validateAttachments(in.Program.Platform, in.Attachments, in.AttachmentsRoot, in.MaxAttachmentBytes, in.AttachmentEvidence)
	if err != nil {
		return Result{}, err
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	fields := buildFields(in)
	disclosure := buildDisclosure(in)
	if err := checkSensitive(fields, disclosure); err != nil {
		return Result{}, err
	}
	missing := missingFields(in.Program.Platform, fields, disclosure, in.Program, in.Verification.AIAssisted)

	draft := domain.ReportDraft{
		ID:               fmt.Sprintf("draft/%s/%s", findingKey(in.Finding.ID), now.UTC().Format("20060102T150405.000000000")),
		FindingID:        in.Finding.ID,
		ProgramID:        in.Program.ID,
		VerificationID:   in.Verification.ID,
		Fields:           fields,
		AIDisclosure:     disclosure,
		Attachments:      append([]string{}, in.Attachments...),
		AttachmentHashes: attachmentHashes,
		Status:           domain.ReportStatusDraft,
		CreatedAt:        now,
	}
	if err := draft.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return Result{Draft: draft, Missing: missing, Ready: len(missing) == 0, AttachmentHashes: attachmentHashes}, nil
}

// ContentHash binds an approval to the report content, not just its ID. The
// hash covers every field, the AI disclosure, attachments and the verification
// reference, so editing a draft after approval invalidates the approval.
func ContentHash(d domain.ReportDraft) (string, error) {
	payload := struct {
		ReportID         string            `json:"report_id"`
		FindingID        string            `json:"finding_id"`
		ProgramID        string            `json:"program_id"`
		VerificationID   string            `json:"verification_id"`
		Fields           map[string]string `json:"fields"`
		AIDisclosure     map[string]string `json:"ai_disclosure"`
		Attachments      []string          `json:"attachments"`
		AttachmentHashes map[string]string `json:"attachment_hashes,omitempty"`
	}{
		ReportID: d.ID, FindingID: string(d.FindingID), ProgramID: string(d.ProgramID),
		VerificationID: d.VerificationID, Fields: d.Fields,
		AIDisclosure: d.AIDisclosure, Attachments: d.Attachments,
		AttachmentHashes: d.AttachmentHashes,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("report: content hash: %w", err)
	}
	return domain.CanonicalParametersHash(raw), nil
}

// Recheck re-validates a stored draft against the stored Finding/Program/
// Verification before submission. The draft maps are converted back to the
// structured fields so the same completeness rules apply.
func Recheck(draft domain.ReportDraft, finding domain.Finding, program domain.Program, verification domain.Verification) (Result, error) {
	if draft.FindingID != finding.ID || draft.ProgramID != program.ID ||
		draft.VerificationID != verification.ID ||
		finding.ProgramID != program.ID || verification.FindingID != finding.ID {
		return Result{}, fmt.Errorf("%w: stored draft entity mismatch", ErrInvalidInput)
	}
	if err := draft.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if verification.Verdict != domain.VerdictVerified {
		return Result{}, fmt.Errorf("%w: verification is %s", ErrNotVerified, verification.Verdict)
	}
	if len(verification.Evidence) == 0 {
		return Result{Draft: draft, Missing: []string{"evidence"}}, nil
	}
	if err := verification.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	// Recheck intentionally does not inspect files: it validates the stored draft
	// shape. File existence/hash re-verification happens in the submit service
	// against the approved AttachmentHashes.
	if _, err := validateAttachments(program.Platform, draft.Attachments, "", 0, nil); err != nil {
		return Result{}, err
	}
	if len(draft.AttachmentHashes) > 0 {
		for _, a := range draft.Attachments {
			if strings.TrimSpace(draft.AttachmentHashes[a]) == "" {
				return Result{}, fmt.Errorf("%w: %s has no approved hash", ErrInvalidAttachment, a)
			}
		}
	}
	if err := checkSensitive(draft.Fields, draft.AIDisclosure); err != nil {
		return Result{}, err
	}
	// Do not re-derive defaults from Finding/Program: the submitted content must
	// be exactly what was stored and approved, so an incomplete stored draft is
	// not made "ready" by a later program/finding update.
	missing := missingFields(program.Platform, draft.Fields, draft.AIDisclosure, program, verification.AIAssisted)
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
		"vendor": f.Vendor, "category": f.Category, "asset_scope": f.AssetScope,
		"weight": f.Weight, "activity": f.Activity,
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

func missingFields(platform domain.Platform, fields, disclosure map[string]string, program domain.Program, verificationAIAssisted bool) []string {
	var missing []string
	for _, key := range requiredFields(platform) {
		if strings.TrimSpace(fields[key]) == "" {
			missing = append(missing, key)
		}
	}
	aiAssisted := disclosure["ai_assisted"] == "true"
	// AI used anywhere in discovery must be disclosed in the report, even if
	// the report text itself was written by a human.
	if verificationAIAssisted && !aiAssisted {
		missing = append(missing, "ai_disclosure")
	}
	if aiAssisted {
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

// checkSensitive refuses report text that contains cookies, tokens, passwords,
// full phone/email values or card/ID numbers. Raw evidence stays local; the DB
// must not store secrets, and a report should use placeholders instead.
func checkSensitive(fields, disclosure map[string]string) error {
	for key, value := range fields {
		if findings := redact.ScanReport(value); len(findings) > 0 {
			return fmt.Errorf("%w: %s (%s)", ErrSensitive, key, findings[0].Kind)
		}
	}
	if findings := redact.ScanReport(disclosure["human_verification"]); len(findings) > 0 {
		return fmt.Errorf("%w: human_verification (%s)", ErrSensitive, findings[0].Kind)
	}
	return nil
}

const DefaultMaxAttachmentBytes = 20 << 20

// magicByExt lists the allowed container signatures per extension. A .zip that
// is not a zip (or a .pdf that is an executable) is rejected instead of being
// forwarded to the platform by name.
var magicByExt = map[string][][]byte{
	".zip":  {[]byte{'P', 'K', 0x03, 0x04}, []byte{'P', 'K', 0x05, 0x06}},
	".docx": {[]byte{'P', 'K', 0x03, 0x04}, []byte{'P', 'K', 0x05, 0x06}},
	".xlsx": {[]byte{'P', 'K', 0x03, 0x04}, []byte{'P', 'K', 0x05, 0x06}},
	".rar":  {[]byte("Rar!\x1a\x07")},
	".pdf":  {[]byte("%PDF-")},
	".doc":  {{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}},
	".xls":  {{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}},
}

func validateAttachments(platform domain.Platform, attachments []string, rootDir string, maxBytes int64, provenance map[string][]domain.EvidenceID) (map[string]string, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxAttachmentBytes
	}
	var root *os.Root
	if strings.TrimSpace(rootDir) != "" {
		r, err := os.OpenRoot(rootDir)
		if err != nil {
			return nil, fmt.Errorf("%w: open attachments root: %v", ErrInvalidAttachment, err)
		}
		defer func() { _ = r.Close() }()
		root = r
	}
	hashes := map[string]string{}
	for _, a := range attachments {
		if strings.TrimSpace(a) == "" {
			return nil, fmt.Errorf("%w: empty attachment path", ErrInvalidAttachment)
		}
		switch platform {
		case domain.PlatformVulbox:
			if !allowedExt(a, ".rar", ".zip", ".doc", ".docx", ".pdf", ".xls", ".xlsx") {
				return nil, fmt.Errorf("%w: %s is not a Vulbox attachment type", ErrInvalidAttachment, a)
			}
		case domain.PlatformButian:
			if !allowedExt(a, ".zip", ".rar") {
				return nil, fmt.Errorf("%w: %s is not a Butian archive", ErrInvalidAttachment, a)
			}
		}
		if root == nil {
			continue
		}
		// Strict mode: the attachment must exist inside the workspace, match its
		// extension's magic bytes, be bounded in size, and have evidence
		// provenance.
		if len(provenance[a]) == 0 {
			return nil, fmt.Errorf("%w: %s has no evidence provenance", ErrInvalidAttachment, a)
		}
		hash, err := inspectAttachment(root, a, maxBytes)
		if err != nil {
			return nil, err
		}
		hashes[a] = hash
	}
	return hashes, nil
}

func inspectAttachment(root *os.Root, path string, maxBytes int64) (string, error) {
	f, err := root.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrInvalidAttachment, path, err)
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil || st.Size() <= 0 {
		return "", fmt.Errorf("%w: %s is empty or unreadable", ErrInvalidAttachment, path)
	} else if st.Size() > maxBytes {
		return "", fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidAttachment, path, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrInvalidAttachment, path, err)
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidAttachment, path, maxBytes)
	}
	ext := strings.ToLower(filepath.Ext(path))
	sigs, ok := magicByExt[ext]
	if !ok {
		return "", fmt.Errorf("%w: %s has no recognized signature", ErrInvalidAttachment, path)
	}
	matched := false
	for _, sig := range sigs {
		if len(data) >= len(sig) && bytes.Equal(data[:len(sig)], sig) {
			matched = true
			break
		}
	}
	if !matched {
		return "", fmt.Errorf("%w: %s content does not match %s", ErrInvalidAttachment, path, ext)
	}
	if isTextLike(data) {
		if findings := redact.ScanReport(string(data)); len(findings) > 0 {
			return "", fmt.Errorf("%w: %s contains a sensitive value (%s)", ErrInvalidAttachment, path, findings[0].Kind)
		}
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func isTextLike(data []byte) bool {
	if len(data) == 0 || !utf8.Valid(data) {
		return false
	}
	printable := 0
	for _, r := range string(data) {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsPrint(r) {
			printable++
		}
	}
	return printable*10 >= len([]rune(string(data)))*9
}

// VerifyAttachmentHashes re-hashes stored draft attachments against the hashes
// bound by the approval. A missing root when hashes exist is an error: the
// submitter must not send a file it cannot verify.
func VerifyAttachmentHashes(d domain.ReportDraft, rootDir string, maxBytes int64) error {
	if len(d.AttachmentHashes) == 0 {
		return nil
	}
	if strings.TrimSpace(rootDir) == "" {
		return fmt.Errorf("%w: attachment root is required to verify hashes", ErrInvalidAttachment)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxAttachmentBytes
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return fmt.Errorf("%w: open attachments root: %v", ErrInvalidAttachment, err)
	}
	defer func() { _ = root.Close() }()
	for _, a := range d.Attachments {
		want, ok := d.AttachmentHashes[a]
		if !ok {
			return fmt.Errorf("%w: %s has no approved hash", ErrInvalidAttachment, a)
		}
		got, err := inspectAttachment(root, a, maxBytes)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%w: %s changed after approval", ErrInvalidAttachment, a)
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
