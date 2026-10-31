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

var patterns = []pattern{
	{"phone", regexp.MustCompile(`\b1[3-9]\d{9}\b`)},
	{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)},
	{"id_card", regexp.MustCompile(`\b[1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`)},
	{"bank_card", regexp.MustCompile(`\b(?:\d[ -]?){16,19}\b`)},
	{"authorization", regexp.MustCompile(`(?i)authorization\s*:\s*bearer\s+[A-Za-z0-9._~+/=\-]+`)},
	{"cookie", regexp.MustCompile(`(?i)cookie\s*:\s*[^\r\n]+`)},
	{"password", regexp.MustCompile(`(?i)(?:password|passwd|pwd)\s*[=:]\s*[^\s&"']+`)},
	{"api_key", regexp.MustCompile(`(?i)(?:access[_-]?key|secret[_-]?key|api[_-]?key|token)\s*[=:]\s*[A-Za-z0-9/+_\-]{8,}`)},
}

// Scan returns findings for sensitive-looking values. It never returns the
// full matched value.
func Scan(text string) []Finding {
	var out []Finding
	for _, p := range patterns {
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
