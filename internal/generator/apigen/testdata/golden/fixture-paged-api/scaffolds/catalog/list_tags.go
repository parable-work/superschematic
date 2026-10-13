// Implementation scaffold for fixture-paged-api API
// Endpoint: GET /api/tags
//
// This file contains your implementation of the ListTags method.
// It implements the CatalogImplementation interface.
//
// NOTE: This file was generated as a scaffold and will NOT be overwritten.
// Feel free to modify it as needed for your business logic.

package catalog

import (
	"context"
	"errors"

	"github.com/parable-work/superschematic/runtime/http/go/paging"
)

// ListTags implements the business logic for GET /api/tags
//
// The tags items carry: a page of a primitive, with the default limits.
//
// Page (@paginated):
//   - page (paging.Request): the cursor, "" for the first page, and the limit, 1 to 100 (20 when absent)
//
// Returns:
//   - paging.Page[string]: the page's items and the cursor of the next page, nil after the last
//   - error: Error if operation fails
func (impl *Implementation) ListTags(ctx context.Context, page paging.Request) (paging.Page[string], error) {
	// TODO: Implement ListTags
	//
	// Example implementation patterns:
	//
	// 6. Read up to page.Limit+1 items after page.Cursor in the operation's
	//    own order (paging.DecodeCursor reads the cursor back into your key,
	//    and refuses one it cannot read), then return the page:
	//    return paging.FromRows(rows, page.Limit, func(row string) any { return key(row) })
	//    return paging.Page[string]{}, errors.New("something went wrong")

	return paging.Page[string]{}, errors.New("ListTags: not implemented")
}
