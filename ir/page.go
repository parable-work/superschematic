package ir

import (
	"errors"
	"fmt"
)

// OperationPage is @paginated (D60): the operation's list result pages by
// an opaque cursor. The operation takes two query parameters no schema
// declares, `cursor` and `limit`, and answers `{items, next}` in place of
// its list. The data forms write it as the decorator's argument, either
// member unsaid; the loader fills both in every form, so a reader never
// applies the defaults itself.
type OperationPage struct {
	// DefaultLimit is how many items a page holds when the caller sends no
	// limit: 1 to MaxLimit.
	DefaultLimit int `json:"defaultLimit,omitempty" yaml:"defaultLimit,omitempty"`

	// MaxLimit is the most items a caller may ask a page to hold: 1 to
	// [PageMaxLimitBound].
	MaxLimit int `json:"maxLimit,omitempty" yaml:"maxLimit,omitempty"`
}

// The limits of a page (D60).
const (
	// DefaultPageLimit is a page's default limit when @paginated leaves it
	// unsaid, or its maxLimit when that is less.
	DefaultPageLimit = 20
	// DefaultPageMaxLimit is a page's maxLimit when @paginated leaves it
	// unsaid.
	DefaultPageMaxLimit = 100
	// PageMaxLimitBound is the largest maxLimit an operation may declare.
	PageMaxLimitBound = 1000
	// PageCursorMaxLength is the longest cursor a router reads, in
	// characters of the unpadded base64url alphabet.
	PageCursorMaxLength = 1024
)

// PageCursorPattern is the pattern a cursor matches: the unpadded
// base64url alphabet, at least one character.
const PageCursorPattern = "^[A-Za-z0-9_-]+$"

// PageParameterNames are the query parameters a page adds, cursor and
// limit, and "page", an implementation's name for the page request; a paged
// operation may declare an argument by none of them.
var PageParameterNames = []string{"cursor", "limit", "page"}

// WithDefaults fills the members the author left unsaid, zero: MaxLimit
// takes [DefaultPageMaxLimit], then DefaultLimit takes [DefaultPageLimit],
// or MaxLimit when that is less, so @paginated({ maxLimit: 10 }) pages ten
// at a time.
func (p OperationPage) WithDefaults() OperationPage {
	if p.MaxLimit == 0 {
		p.MaxLimit = DefaultPageMaxLimit
	}
	if p.DefaultLimit == 0 {
		p.DefaultLimit = min(DefaultPageLimit, p.MaxLimit)
	}
	return p
}

// ValidateOperationPage checks an operation's page as the JSON Schema of
// @paginated's argument and its Apply do, for IR built without a frontend:
// each limit from 1 to [PageMaxLimitBound], filled in, and the default no
// more than the maximum. owner names the operation (Set.operation). What
// the page asks of the operation, a GET returning a list and no argument
// named as a page parameter, is the loader's check (verify), which reads
// the operation set and the schema.
func ValidateOperationPage(owner string, op *FieldDef) error {
	if op == nil || op.Page == nil {
		return nil
	}
	page := op.Page
	var errs []error
	for _, member := range []struct {
		name  string
		value int
	}{{"maxLimit", page.MaxLimit}, {"defaultLimit", page.DefaultLimit}} {
		if member.value < 1 || member.value > PageMaxLimitBound {
			errs = append(errs, fmt.Errorf("%s: @paginated %s must be between 1 and %d, not %d", owner, member.name, PageMaxLimitBound, member.value))
		}
	}
	if len(errs) == 0 && page.DefaultLimit > page.MaxLimit {
		errs = append(errs, fmt.Errorf("%s: @paginated defaultLimit %d is more than its maxLimit %d", owner, page.DefaultLimit, page.MaxLimit))
	}
	return errors.Join(errs...)
}
