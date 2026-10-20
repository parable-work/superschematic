// Implementation scaffold for fixture-paged-api API
// Endpoint: GET /api/items
//
// This file contains your implementation of the ListItems method.
// It implements the CatalogImplementation interface.
//
// NOTE: This file was generated as a scaffold and will NOT be overwritten.
// Feel free to modify it as needed for your business logic.

package catalog

import (
	"context"
	"errors"

	"github.com/parable-work/superschematic/runtime/http/go/paging"

	types "example.com/schemas/types/go/fixture-paged-api"
)

// ListItems implements the business logic for GET /api/items
//
// The items, two to a page unless the caller asks for up to five, by
// their ids or their stock, in order of their names unless the caller
// sorts them by name or sku, either way.
//
// Query Parameters:
//   - ids (*types.IdentityUUID): Optional
//   - inStock (*bool): Optional
//
// Page (@paginated):
//   - page (paging.Request): the cursor, "" for the first page, and the limit, 1 to 5 (2 when absent)
//     and the sort, a field of name, sku, descending when Desc (name when absent; ties break by id)
//
// Returns:
//   - paging.Page[types.ItemView]: the page's items and the cursor of the next page, nil after the last
//   - error: Error if operation fails
func (impl *Implementation) ListItems(ctx context.Context, ids []types.IdentityUUID, inStock *bool, page paging.Request) (paging.Page[types.ItemView], error) {
	// TODO: Implement ListItems
	//
	// Example implementation patterns:
	//
	// 6. Read up to page.Limit+1 items after page.Cursor in the operation's
	//    own order (paging.DecodeCursor reads the cursor back into your key,
	//    and refuses one it cannot read), then return the page:
	//    return paging.FromRows(rows, page.Limit, func(row types.ItemView) any { return key(row) })
	//    return paging.Page[types.ItemView]{}, errors.New("something went wrong")

	return paging.Page[types.ItemView]{}, errors.New("ListItems: not implemented")
}
