package shoporders

import (
	"errors"

	orm "example.com/acme/orm/shop-db"
	"github.com/parable-work/superschematic/runtime/http/go/paging"
)

// The paged lists of shop-orders (D60) read each page through the ORM's
// FindPage, in one query: the rows the list's filter finds, in the order
// of the page request's sort (D60, amended), then their ids, up to
// page.Limit of them after the cursor's row, and the key of the last one
// when more follow. Parse<Type>Sort reads the sort, whose fields are the
// table's columns of the same names. A page's cursor is that key, the
// sort field's value and the row's id; the router binds it to the sort,
// and unbinds the one a request sends back before the implementation
// reads it. What stays the implementation's is the filter and the rows'
// views.

// pageKey is the key a page request's cursor holds, nil for the first
// page. A cursor that does not decode into K is the 400 of a cursor the
// implementation cannot read.
func pageKey[K any](page paging.Request) (*K, error) {
	if page.Cursor == "" {
		return nil, nil
	}
	key := new(K)
	if err := paging.DecodeCursor(page.Cursor, key); err != nil {
		return nil, err
	}
	return key, nil
}

// pageOf is the page of items whose last row's key is next, the last page
// when next is nil.
func pageOf[T, K any](items []T, next *K) (paging.Page[T], error) {
	if next == nil {
		return paging.Last(items), nil
	}
	cursor, err := paging.EncodeCursor(next)
	if err != nil {
		return paging.Page[T]{}, err
	}
	return paging.More(items, cursor), nil
}

// pageError is FindPage's error as the list answers it: a key that names
// no row of the page's order, one without an id or of another sort
// (orm.ErrPageKey), is the 400 of a cursor the implementation cannot read.
func pageError(err error) error {
	if errors.Is(err, orm.ErrPageKey) {
		return paging.RefusedCursor()
	}
	return err
}
