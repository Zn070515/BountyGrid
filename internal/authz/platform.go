package authz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy/rules"
)

// VerifyPlatformEvidence proves that a PlatformRule is actually backed by the
// platform's own evidence and is still usable for this program.
//
// It checks, fail-closed:
//   - an evidence store is configured and every evidence_refs ID exists;
//   - each Evidence belongs to the program's platform;
//   - the rule is bound to this program;
//   - the rule's policy_revision matches Program.PolicyRevision();
//   - valid_until is in the future and observed_at is not materially future;
//   - Program.PolicyFreshAt(now) holds.
//
// A missing evidence record, a platform mismatch, a stale revision and an
// expired rule are all rejections. Store/database failures are returned as
// errors so a transient outage is never mistaken for authority.
func VerifyPlatformEvidence(ctx context.Context, rule rules.PlatformRule, program domain.Program, store EvidenceStore, now time.Time) error {
	if store == nil {
		return reject(ReasonEvidenceStoreMissing, "no evidence store configured")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !rule.BindsProgram(program.ID) {
		return reject(ReasonRuleProgramMismatch, "rule %s does not bind program %s", rule.ID(), program.ID)
	}
	if err := CheckRevisionBinding(program, "", rule.PolicyRevision()); err != nil {
		return err
	}
	if !rule.ValidUntil().After(now) {
		return reject(ReasonRuleExpired, "rule %s valid_until %s is not after %s",
			rule.ID(), rule.ValidUntil().UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	if rule.ObservedAt().After(now.Add(ClockSkew)) {
		return reject(ReasonRuleFromFuture, "rule %s observed_at %s is after %s",
			rule.ID(), rule.ObservedAt().UTC().Format(time.RFC3339), now.Add(ClockSkew).UTC().Format(time.RFC3339))
	}
	if !program.PolicyFreshAt(now) {
		return &FreshnessError{Reason: ReasonStaleAutomationPolicy}
	}
	refs := rule.EvidenceRefs()
	if len(refs) == 0 {
		return reject(ReasonEvidenceMissing, "rule %s has no evidence refs", rule.ID())
	}
	for _, id := range refs {
		ev, err := store.GetEvidence(ctx, id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return reject(ReasonEvidenceMissing, "evidence %s not found", id)
			}
			return fmt.Errorf("authz: load evidence %s: %w", id, err)
		}
		if ev.Platform != program.Platform {
			return reject(ReasonEvidencePlatformMismatch, "evidence %s platform %q does not match program platform %q",
				id, ev.Platform, program.Platform)
		}
	}
	return nil
}

// BuildPlatformPolicy constructs platform authority for one program. Every
// candidate must pass NewPlatformRule and VerifyPlatformEvidence; the resulting
// policy is immutable and holds only verified rules.
func BuildPlatformPolicy(ctx context.Context, program domain.Program, specs []rules.PlatformRuleSpec, store EvidenceStore, now time.Time) (*PlatformPolicy, error) {
	verified := make([]rules.PlatformRule, 0, len(specs))
	for _, spec := range specs {
		rule, err := rules.NewPlatformRule(spec)
		if err != nil {
			return nil, err
		}
		if err := VerifyPlatformEvidence(ctx, rule, program, store, now); err != nil {
			return nil, err
		}
		verified = append(verified, rule)
	}
	return newPlatformPolicy(program, verified, store), nil
}

// PlatformPolicy is verified platform authority for exactly one program. It can
// only be produced by BuildPlatformPolicy (or another authz constructor), never
// from a rule file. Evaluate re-checks the program's platform, policy revision
// and policy freshness at decision time, so authority observed earlier cannot
// outlive the snapshot it was derived from.
type PlatformPolicy struct {
	programID   domain.ProgramID
	platform    domain.Platform
	rules       []rules.PlatformRule
	store       EvidenceStore
	fingerprint string
}

func newPlatformPolicy(program domain.Program, verified []rules.PlatformRule, store EvidenceStore) *PlatformPolicy {
	p := &PlatformPolicy{
		programID: program.ID,
		platform:  program.Platform,
		rules:     append([]rules.PlatformRule(nil), verified...),
		store:     store,
	}
	p.fingerprint = p.computeFingerprint()
	return p
}

// ProgramID is the program this policy was verified for.
func (p *PlatformPolicy) ProgramID() domain.ProgramID {
	if p == nil {
		return ""
	}
	return p.programID
}

// Len reports how many verified platform rules the policy holds.
func (p *PlatformPolicy) Len() int {
	if p == nil {
		return 0
	}
	return len(p.rules)
}

// Fingerprint covers the program binding and every rule's provenance fields
// (evidence refs, observation window, policy revision, selectors). Authority
// changes therefore invalidate decisions bound to the previous fingerprint.
func (p *PlatformPolicy) Fingerprint() string {
	if p == nil {
		return ""
	}
	return p.fingerprint
}

func (p *PlatformPolicy) computeFingerprint() string {
	parts := []string{string(p.programID), string(p.platform)}
	for _, r := range p.rules {
		parts = append(parts, r.Fingerprint())
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Evaluate matches verified platform rules against req. Only fresh,
// revision-matching rules can set CanRelax. A policy for a different
// program/platform, or a program whose policy snapshot is stale, yields no
// authority rather than an error; the caller's own domain gates still apply.
func (p *PlatformPolicy) Evaluate(ctx context.Context, program domain.Program, req rules.Request) (rules.Result, error) {
	if p == nil || p.store == nil {
		return rules.Result{}, nil
	}
	if program.ID != p.programID || program.Platform != p.platform {
		return rules.Result{}, nil
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
		req.Now = now
	}
	if !program.PolicyFreshAt(now) {
		return rules.Result{}, nil
	}
	var out rules.Result
	for _, rule := range p.rules {
		// Re-verify at decision time, not only at build time: evidence may have
		// been deleted or superseded since the policy was compiled. A failed
		// verification simply yields no authority for that rule (the base
		// decision/freshness gates still apply).
		if err := VerifyPlatformEvidence(ctx, rule, program, p.store, now); err != nil {
			continue
		}
		ok, matchedOn, err := rule.Matches(req)
		if err != nil {
			return rules.Result{}, err
		}
		if !ok {
			continue
		}
		out.Matched = append(out.Matched, rule.MatchedRule(matchedOn))
		out.Decision = rules.DecisionAllow
		out.CanRelax = true
	}
	return out, nil
}
