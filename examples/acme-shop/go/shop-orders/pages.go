package shoporders

import (
	"errors"

	orm "example.com/acme/orm/shop-db"
	"github.com/parable-work/superschematic/runtime/http/go/paging"
)

// The paged lists of shop-orders (D60) read each page through the ORM's
// FindPage, in one query: the rows the list's filter finds, in the order
// of the page request's sort (D60, amended), then their ids, up to
// page.Limit of them after the cursor's row. Parse<Type>Sort reads the
// sort, whose fields are the table's columns of the same names. A page's
// cursor is the ORM's key of its last row, {"id": ...}: FindPage reads
// that row's value of the sort field from the table, so a cursor is as
// short as an id, whatever the row holds. The key does not say its sort;
// the router binds the cursor to the sort, and refuses one sent back with
// another before the implementation reads it. What stays the
// implementation's is the filter and the rows' views.

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

// pageOf is the page of FindPage's rows, each read as view, whose next
// cursor is next, the key of the last row, nil after the last page.
func pageOf[R, T, K any](rows []R, next *K, view func(R) T) (paging.Page[T], error) {
	items := make([]T, 0, len(rows))
	for _, row := range rows {
		items = append(items, view(row))
	}
	if next == nil {
		return paging.Last(items), nil
	}
	cursor, err := paging.EncodeCursor(next)
	if err != nil {
		return paging.Page[T]{}, err
	}
	return paging.More(items, cursor), nil
}

// pageError is FindPage's error as the list answers it: a key that holds
// no id, or whose row no longer exists (orm.ErrPageKey), is the 400 of a
// cursor the implementation cannot read.
func pageError(err error) error {
	if errors.Is(err, orm.ErrPageKey) {
		return paging.RefusedCursor()
	}
	return err
}
