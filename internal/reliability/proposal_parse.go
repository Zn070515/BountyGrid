package reliability

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// proposalDTO is the model-facing JSON shape. It is deliberately smaller than
// ActionProposal: the model never supplies IDs, costs or authorization fields.
type proposalDTO struct {
	Action              string          `json:"action"`
	Tool                string          `json:"tool"`
	Parameters          json.RawMessage `json:"parameters"`
	Rationale           string          `json:"rationale"`
	ExpectedObservation string          `json:"expected_observation"`
	Confidence          float64         `json:"confidence"`
}

// ParseProposals turns model output into candidate actions. It accepts one JSON
// object or an array, optionally fenced or wrapped in prose, and trims a model
// invention such as "READ_SCOPE|human_only=false" down to the action token.
//
// Parsing is not validation: the deterministic RankProposals layer still
// rejects invalid actions, unauthorized actions, human-only actions, asset
// mismatches and secret-bearing parameters. A malformed payload is an error so
// the caller can charge the paid call and then propose nothing.
func ParseProposals(content string) ([]ActionProposal, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("%w: empty model output", ErrInvalidProposal)
	}
	raw, err := extractJSONPayload(content)
	if err != nil {
		return nil, err
	}
	var parsed []proposalDTO
	if strings.HasPrefix(raw, "{") {
		var one proposalDTO
		if err := json.Unmarshal([]byte(raw), &one); err != nil {
			return nil, fmt.Errorf("%w: decode proposal: %v", ErrInvalidProposal, err)
		}
		parsed = []proposalDTO{one}
	} else {
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return nil, fmt.Errorf("%w: decode proposals: %v", ErrInvalidProposal, err)
		}
	}
	out := make([]ActionProposal, 0, len(parsed))
	for _, p := range parsed {
		action := strings.TrimSpace(strings.SplitN(p.Action, "|", 2)[0])
		tool := strings.TrimSpace(strings.SplitN(p.Tool, "|", 2)[0])
		out = append(out, ActionProposal{
			Action:              domain.Action(strings.ToUpper(action)),
			Tool:                tool,
			Parameters:          p.Parameters,
			Rationale:           p.Rationale,
			ExpectedObservation: p.ExpectedObservation,
			Confidence:          p.Confidence,
		})
	}
	return out, nil
}

// extractJSONPayload finds the outermost valid JSON value without guessing at
// the first "[" or "{" inside a string field. Whole-payload JSON wins; then a
// fenced block; then the outermost object or array in prose.
func extractJSONPayload(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	unfenced := strings.TrimSpace(strings.TrimSuffix(
		strings.TrimPrefix(strings.TrimPrefix(trimmed, "```json"), "```"), "```"))
	for _, candidate := range []string{trimmed, unfenced} {
		if json.Valid([]byte(candidate)) {
			return candidate, nil
		}
	}
	for _, pair := range [][2]string{{"{", "}"}, {"[", "]"}} {
		start := strings.Index(unfenced, pair[0])
		end := strings.LastIndex(unfenced, pair[1])
		if start >= 0 && end > start {
			candidate := unfenced[start : end+1]
			if json.Valid([]byte(candidate)) {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("%w: model output is not valid JSON", ErrInvalidProposal)
}
