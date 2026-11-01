package authz

import (
	"strings"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// ResolveInScopeAsset returns the canonical asset recorded in the program's
// stored Scope and its current match classification.
//
// The caller may only supply an ID. The asset value, kind and program/platform
// binding come from the persisted scope, never from the caller. The returned
// match is Scope.CheckAsset, so a gated or unknown disclosure yields
// ScopeMatchUnknown rather than INCLUDED, and an empty include list means no
// executable assets. Any error is fail-closed: there is no asset to authorize.
func ResolveInScopeAsset(program domain.Program, assetID domain.AssetID) (*domain.Asset, domain.ScopeMatch, error) {
	if strings.TrimSpace(string(assetID)) == "" {
		return nil, domain.ScopeMatchUnknown, ErrAssetRequired
	}
	asset, _, ok := program.Scope.ResolveAsset(assetID)
	if !ok {
		return nil, domain.ScopeMatchUnknown, ErrAssetUnknown
	}
	if asset.ProgramID == "" {
		asset.ProgramID = program.ID
	}
	if asset.Platform == "" {
		asset.Platform = program.Platform
	}
	// ResolveAsset answers "which entry names this ID"; CheckAsset answers
	// "is that asset usable under the current access/contents state". The
	// latter is the authorization answer and must win.
	match := program.Scope.CheckAsset(asset)
	return &asset, match, nil
}
