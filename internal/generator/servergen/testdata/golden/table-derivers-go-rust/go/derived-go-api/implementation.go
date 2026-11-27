// Package derivedgoapi implements the derived-go-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it. Each deriver
// fails every field until you implement it.
package derivedgoapi

import (
	"context"
	"github.com/parable-work/superschematic/runtime/http/go/deriver"

	api "example.com/schemas/api/derived-go-api"
	types "example.com/schemas/types/go/derived-go-api"
	fixturederivedprovenancedb "example.com/schemas/types/go/fixture-derived-provenance-db"
)

// New builds the implementation of derived-go-api from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Triage: &Triage{deps: deps},
	}, nil
}

var _ api.Constructor = New

// NewDerivers builds the derivers of derived-go-api from the API's
// dependencies. Its signature is the generated one, api.DeriversConstructor.
// A deriver's entrypoint calls the method of each stale derived field,
// which asks a model; a value settles the field, and a failure or an error
// counts an attempt.
func NewDerivers(deps api.Deps) (api.Derivers, error) {
	return api.Derivers{
		FillAll:       &fillAllDeriver{deps: deps},
		TriageTickets: &triageTicketsDeriver{deps: deps},
	}, nil
}

var _ api.DeriversConstructor = NewDerivers

// fillAllDeriver implements api.FillAllDeriver.
type fillAllDeriver struct {
	deps api.Deps
}

// ArticleSummary derives Article.summary from body.
func (d *fillAllDeriver) ArticleSummary(ctx context.Context, item deriver.DerivedItem[api.ArticleSummaryInputs]) (deriver.DerivedAnswer[string], error) {
	return deriver.DerivedAnswer[string]{}, api.NotImplementedError("deriver FillAll, Article.summary")
}

// TicketCategory derives Ticket.category from subject and body.
func (d *fillAllDeriver) TicketCategory(ctx context.Context, item deriver.DerivedItem[api.TicketCategoryInputs]) (deriver.DerivedAnswer[fixturederivedprovenancedb.Category], error) {
	return deriver.DerivedAnswer[fixturederivedprovenancedb.Category]{}, api.NotImplementedError("deriver FillAll, Ticket.category")
}

// TicketPriority derives Ticket.priority from subject and body.
func (d *fillAllDeriver) TicketPriority(ctx context.Context, item deriver.DerivedItem[api.TicketPriorityInputs]) (deriver.DerivedAnswer[string], error) {
	return deriver.DerivedAnswer[string]{}, api.NotImplementedError("deriver FillAll, Ticket.priority")
}

// triageTicketsDeriver implements api.TriageTicketsDeriver.
type triageTicketsDeriver struct {
	deps api.Deps
}

// TicketCategory derives Ticket.category from subject and body.
func (d *triageTicketsDeriver) TicketCategory(ctx context.Context, item deriver.DerivedItem[api.TicketCategoryInputs]) (deriver.DerivedAnswer[fixturederivedprovenancedb.Category], error) {
	return deriver.DerivedAnswer[fixturederivedprovenancedb.Category]{}, api.NotImplementedError("deriver TriageTickets, Ticket.category")
}

// TicketPriority derives Ticket.priority from subject and body.
func (d *triageTicketsDeriver) TicketPriority(ctx context.Context, item deriver.DerivedItem[api.TicketPriorityInputs]) (deriver.DerivedAnswer[string], error) {
	return deriver.DerivedAnswer[string]{}, api.NotImplementedError("deriver TriageTickets, Ticket.priority")
}

// Triage implements api.TriageImplementation.
type Triage struct {
	deps api.Deps
}

// Summary handles GET /api/summary.
func (impl *Triage) Summary(ctx context.Context) (*types.Summary, error) {
	return nil, api.NotImplementedError("Triage.Summary")
}
