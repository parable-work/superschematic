// Package fixturetrackerapi implements the fixture-tracker-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
package fixturetrackerapi

import (
	"context"

	api "example.com/schemas/api/fixture-tracker-api"
	types "example.com/schemas/types/go/fixture-tracker-api"
)

// New builds the implementation of fixture-tracker-api from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Teams: &Teams{deps: deps},
	}, nil
}

var _ api.Constructor = New

// Teams implements api.TeamsImplementation.
type Teams struct {
	deps api.Deps
}

// SummarizeTeam handles GET /api/teams/{id}/summary.
func (impl *Teams) SummarizeTeam(ctx context.Context, id types.IdentityUUID) (*types.TeamSummary, error) {
	return nil, api.NotImplementedError("Teams.SummarizeTeam")
}
