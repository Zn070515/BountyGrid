// Package redact provides secret scanning, redaction, and content hashing for
// evidence and audit payloads. It is intentionally conservative: a finding is
// reported with a kind and a masked preview, never the full secret.
package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

type Finding struct {
	Kind    string
	Preview string
}

type pattern struct {
	kind string
	re   *regexp.Regexp
}

var (
	phonePattern         = pattern{"phone", regexp.MustCompile(`\b1[3-9]\d{9}\b`)}
	emailPattern         = pattern{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)}
	idCardPattern        = pattern{"id_card", regexp.MustCompile(`\b[1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`)}
	bankCardPattern      = pattern{"bank_card", regexp.MustCompile(`\b(?:\d[ -]?){16,19}\b`)}
	authorizationPattern = pattern{"authorization", regexp.MustCompile(`(?i)authorization\s*:\s*bearer\s+[A-Za-z0-9._~+/=\-]+`)}
	cookiePattern        = pattern{"cookie", regexp.MustCompile(`(?i)cookie\s*:\s*[^\r\n]+`)}
	passwordPattern      = pattern{"password", regexp.MustCompile(`(?i)(?:password|passwd|pwd)\s*[=:]\s*[^\s&"']+`)}
	apiKeyPattern        = pattern{"api_key", regexp.MustCompile(`(?i)(?:access[_-]?key|secret[_-]?key|api[_-]?key|token)\s*[=:]\s*[A-Za-z0-9/+_\-]{8,}`)}
	// reportBankCardPattern requires card-ish context; the broad bank_card
	// pattern matches timestamps and order IDs and is too noisy for report text.
	reportBankCardPattern = pattern{"bank_card", regexp.MustCompile(`(?i)(?:bank|card|银行卡|卡号)[^\r\n]{0,24}\b(?:\d[ -]?){16,19}\b`)}

	patterns = []pattern{
		phonePattern, emailPattern, idCardPattern, bankCardPattern,
		authorizationPattern, cookiePattern, passwordPattern, apiKeyPattern,
	}
	reportPatterns = []pattern{
		phonePattern, emailPattern, idCardPattern, reportBankCardPattern,
		authorizationPattern, cookiePattern, passwordPattern, apiKeyPattern,
	}
)

// Scan returns findings for sensitive-looking values. It never returns the
// full matched value.
func Scan(text string) []Finding { return scan(text, patterns) }

// ScanReport is the report-text variant. It accepts ordinary long timestamps/
// order IDs but still rejects bare card numbers when they pass Luhn or use a
// well-known card prefix (4/5/62/34/37/35/6011/65).
func ScanReport(text string) []Finding {
	out := scan(text, reportPatterns)
	seen := make(map[string]struct{}, len(out))
	for _, f := range out {
		seen[f.Kind+"|"+f.Preview] = struct{}{}
	}
	for _, m := range bankCardPattern.re.FindAllString(text, -1) {
		digits := digitsOnly(m)
		if !luhnValid(digits) && !looksLikeCardPrefix(digits) {
			continue
		}
		key := "bank_card|" + preview(m)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Finding{Kind: "bank_card", Preview: preview(m)})
	}
	return out
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func luhnValid(digits string) bool {
	if len(digits) < 16 || len(digits) > 19 {
		return false
	}
	parity := len(digits) % 2
	sum := 0
	for i, r := range digits {
		d := int(r - '0')
		if i%2 == parity {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

func looksLikeCardPrefix(digits string) bool {
	for _, p := range []string{"4", "5", "62", "34", "37", "35", "6011", "65"} {
		if strings.HasPrefix(digits, p) {
			return true
		}
	}
	return false
}

func scan(text string, ps []pattern) []Finding {
	var out []Finding
	for _, p := range ps {
		for _, m := range p.re.FindAllString(text, -1) {
			out = append(out, Finding{Kind: p.kind, Preview: preview(m)})
		}
	}
	return out
}

// Redact replaces sensitive values with [REDACTED:kind].
func Redact(text string) (string, []Finding) {
	out := text
	var findings []Finding
	for _, p := range patterns {
		out = p.re.ReplaceAllStringFunc(out, func(m string) string {
			findings = append(findings, Finding{Kind: p.kind, Preview: preview(m)})
			return "[REDACTED:" + p.kind + "]"
		})
	}
	return out, findings
}

// Hash returns the SHA-256 hex digest used by Evidence.ContentHash.
func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func preview(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 6 {
		return "***"
	}
	return fmt.Sprintf("%s***%s", s[:2], s[len(s)-2:])
}
