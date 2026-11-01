package policy

import (
	"fmt"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// effectiveEligibility recomputes eligibility only when the program carries
// gates. Programs with no gates keep the stored decision; programs with gates
// are re-evaluated against the account's current observed state so a gate like
// PRACTICAL_CERTIFICATION is not permanently frozen as UNSATISFIED.
func effectiveEligibility(dc DecisionContext, version string, now time.Time) (domain.EligibilityState, error) {
	p := dc.Program
	if len(p.Gates) == 0 && len(p.Eligibility.Gates) == 0 {
		return p.Eligibility, nil
	}
	st, err := EvaluateEligibility(dc.Account, p, now, version)
	if err != nil {
		return domain.EligibilityState{}, fmt.Errorf("policy: eligibility: %w", err)
	}
	return st, nil
}

// checkExecutionEnvironment evaluates the constraints that depend on runtime
// state: time windows, RPS, concurrency, VPN, egress IP, researcher headers,
// test-account attestation and allowed HTTP methods.
//
// It returns a non-empty decision only when the action cannot proceed. Missing
// runtime data is REQUIRE_MORE_EVIDENCE (fail closed); an observed violation is
// DENY.
func checkExecutionEnvironment(dc DecisionContext, now time.Time) (domain.Decision, string) {
	if !dc.Request.Action.NeedsScope() {
		return "", ""
	}
	c := dc.Program.Constraints
	env := dc.Environment

	// Time windows are absolute and are checked against the decision time.
	if len(c.AllowedTimeWindows) > 0 {
		inWindow := false
		for _, w := range c.AllowedTimeWindows {
			at := now
			if w.TZ != "" {
				if loc, err := time.LoadLocation(w.TZ); err == nil {
					at = now.In(loc)
				}
			}
			if !at.Before(w.Start) && at.Before(w.End) {
				inWindow = true
				break
			}
		}
		if !inWindow {
			return domain.DecisionDeny, "OUTSIDE_ALLOWED_TIME_WINDOW"
		}
	}

	if len(c.AllowedMethods) > 0 || len(c.ForbiddenMethods) > 0 {
		m := strings.ToUpper(strings.TrimSpace(dc.Request.Method))
		if m == "" {
			return domain.DecisionRequireMoreEvidence, "METHOD_UNKNOWN"
		}
		for _, f := range c.ForbiddenMethods {
			if strings.EqualFold(strings.TrimSpace(f), m) {
				return domain.DecisionDeny, "METHOD_FORBIDDEN"
			}
		}
		if len(c.AllowedMethods) > 0 {
			allowed := false
			for _, a := range c.AllowedMethods {
				if strings.EqualFold(strings.TrimSpace(a), m) {
					allowed = true
					break
				}
			}
			if !allowed {
				return domain.DecisionDeny, "METHOD_NOT_ALLOWED"
			}
		}
	}

	if c.MaxRPS != nil {
		if env == nil || env.CurrentRPS == nil {
			return domain.DecisionRequireMoreEvidence, "RPS_UNKNOWN"
		}
		if *env.CurrentRPS >= *c.MaxRPS {
			return domain.DecisionDeny, "RATE_LIMIT_EXCEEDED"
		}
	}
	if c.MaxConcurrency != nil {
		if env == nil || env.CurrentConcurrency == nil {
			return domain.DecisionRequireMoreEvidence, "CONCURRENCY_UNKNOWN"
		}
		if *env.CurrentConcurrency >= *c.MaxConcurrency {
			return domain.DecisionDeny, "CONCURRENCY_LIMIT_EXCEEDED"
		}
	}

	if d, reason := requireTriState(c.VPNRequired, "VPN", env != nil && env.VPNConnected == domain.TriYes,
		env != nil && env.VPNConnected == domain.TriNo, "VPN_STATE_UNKNOWN"); d != "" {
		return d, reason
	}
	if d, reason := requireTriState(c.DedicatedIPRequired, "DEDICATED_IP",
		env != nil && env.EgressIP != "", false, "EGRESS_IP_UNKNOWN"); d != "" {
		return d, reason
	}
	if len(c.AllowedEgressIPs) > 0 {
		if env == nil || env.EgressIP == "" {
			return domain.DecisionRequireMoreEvidence, "EGRESS_IP_UNKNOWN"
		}
		allowed := false
		for _, ip := range c.AllowedEgressIPs {
			if strings.EqualFold(strings.TrimSpace(ip), strings.TrimSpace(env.EgressIP)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return domain.DecisionDeny, "EGRESS_IP_NOT_ALLOWED"
		}
	}
	if d, reason := requireTriState(c.ResearcherHeaderRequired, "RESEARCHER_HEADER",
		env != nil && env.ResearcherHeaders != nil && *env.ResearcherHeaders,
		env != nil && env.ResearcherHeaders != nil && !*env.ResearcherHeaders,
		"RESEARCHER_HEADER_UNKNOWN"); d != "" {
		return d, reason
	}
	if d, reason := requireTriState(c.TestAccountRequired, "TEST_ACCOUNT",
		env != nil && env.TestAccountID != "",
		env != nil && env.TestAccountID == "",
		"TEST_ACCOUNT_UNKNOWN"); d != "" {
		return d, reason
	}
	if c.TestAccountReportingRequired != nil && *c.TestAccountReportingRequired {
		if env == nil || env.TestAccountReportingReady == nil {
			return domain.DecisionRequireMoreEvidence, "TEST_ACCOUNT_REPORTING_UNKNOWN"
		}
		if !*env.TestAccountReportingReady {
			return domain.DecisionDeny, "TEST_ACCOUNT_REPORTING_REQUIRED"
		}
	}
	return "", ""
}

// requireTriState maps a constraint's tri-state to a policy answer:
//   - "" or NO: no runtime proof needed
//   - YES: the proof must be present; when it is absent an empty decision means
//     "requirement satisfied", so callers pass `satisfied` for YES-proof.
//   - UNKNOWN/PROGRAM_SPECIFIC: the constraint itself is unresolved.
func requireTriState(req domain.TriState, name string, satisfied, explicitlyAbsent bool, unknownReason string) (domain.Decision, string) {
	switch req {
	case "", domain.TriNo:
		return "", ""
	case domain.TriYes:
		if satisfied {
			return "", ""
		}
		if explicitlyAbsent {
			return domain.DecisionDeny, name + "_REQUIRED"
		}
		return domain.DecisionRequireMoreEvidence, unknownReason
	default:
		return domain.DecisionRequireMoreEvidence, name + "_CONSTRAINT_UNKNOWN"
	}
}
