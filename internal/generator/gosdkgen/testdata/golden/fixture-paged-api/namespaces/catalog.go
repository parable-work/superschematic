package namespaces

import (
	"context"
	"fmt"
	"iter"
	"net/url"

	"example.com/schemas/sdk/go/fixture-paged-api/runtime"
	types "example.com/schemas/types/go/fixture-paged-api"
)

// CatalogNamespace contains API methods for the catalog namespace.
//
// Endpoints:
// - GetItem: One item.
// - ListItems: The items, two to a page unless the caller asks for up to five, by their ids or their stock, in order of their names unless the caller sorts them by name or sku, either way.
// - ListTags: The tags items carry: a page of a primitive, with the default limits.
// - PlaceBasket: Place a basket.
type CatalogNamespace struct {
	client jsonClient
}

// NewCatalogNamespace creates a CatalogNamespace.
func NewCatalogNamespace(
	client jsonClient,
) *CatalogNamespace {
	return &CatalogNamespace{
		client: client,
	}
}

// GetItem One item.
func (n *CatalogNamespace) GetItem(
	ctx context.Context,
	ItemId string,
) (types.ItemView, error) {
	path := fmt.Sprintf("/api/items/%v", pathSegment(ItemId))

	var requestBody any
	var out types.ItemView
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.ItemView
		return zero, err
	}
	return out, nil
}

type CatalogListItemsQueryParams struct {
	Ids     []types.IdentityUUID `json:"ids,omitzero"`
	InStock *bool                `json:"inStock,omitempty"`
	Sort    *string              `json:"sort,omitempty"`
}

// ListItems The items, two to a page unless the caller asks for up to five, by their ids or their stock, in order of their names unless the caller sorts them by name or sku, either way.
//
// It returns one page of the list (D60): page.Cursor is the Next of the
// page before, "" for the first page, and page.Limit the most items the
// page holds, 1 to 5, or 0 for 2. ListItemsAll yields every item.
func (n *CatalogNamespace) ListItems(
	ctx context.Context,
	query *CatalogListItemsQueryParams,
	page runtime.PageRequest,
) (runtime.Page[types.ItemView], error) {
	path := "/api/items"
	queryParamValidationErrors := types.NewValidationErrors()
	if query != nil {
		// ids is sent as one comma-separated value, and an empty list
		// is left out. Each item is checked at ids[i].
		if len(query.Ids) > 5 {
			queryParamValidationErrors.AddFieldError("ids", "listMax", "must contain at most 5 items")
		}
		for i, item := range query.Ids {
			itemPath := fmt.Sprintf("ids[%d]", i)
			itemText := fmt.Sprint(item)
			if !checkQueryListItem(queryParamValidationErrors, itemPath, itemText) {
				continue
			}
			validateListElement(queryParamValidationErrors, itemPath, &item)
		}
	}
	if queryParamValidationErrors.HasErrors() {
		var zero runtime.Page[types.ItemView]
		return zero, NewValidationError(queryParamValidationErrors)
	}
	if pageErrors := pageLimitErrors(page.Limit, 5); pageErrors.HasErrors() {
		var zero runtime.Page[types.ItemView]
		return zero, NewValidationError(pageErrors)
	}
	queryValues := url.Values{}
	if query != nil {
		runtime.AddQueryList(queryValues, "ids", query.Ids)
		if query.InStock != nil {
			runtime.AddQueryParam(queryValues, "inStock", *query.InStock)
		}
		if query.Sort != nil {
			runtime.AddQueryParam(queryValues, "sort", *query.Sort)
		}
	}
	runtime.AddPageRequest(queryValues, page)

	var requestBody any
	var out runtime.Page[types.ItemView]
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		queryValues,
		requestBody,
		&out,
	); err != nil {
		var zero runtime.Page[types.ItemView]
		return zero, err
	}
	return out, nil
}

// ListItemsAll yields the items of every page of ListItems in turn (D60),
// asking for each page with the Next of the page before, from page.Cursor
// ("" for the first page) with page.Limit items a page. It asks for a page
// only when the loop has taken every item of the one before, and stops
// after the page whose Next is nil. An error ends the iteration: the one a
// page failed with, or one for a page whose Next is the cursor it was
// asked with.
func (n *CatalogNamespace) ListItemsAll(
	ctx context.Context,
	query *CatalogListItemsQueryParams,
	page runtime.PageRequest,
) iter.Seq2[types.ItemView, error] {
	return runtime.AllItems("Catalog.listItems", page, func(page runtime.PageRequest) (runtime.Page[types.ItemView], error) {
		return n.ListItems(ctx, query, page)
	})
}

// ListTags The tags items carry: a page of a primitive, with the default limits.
//
// It returns one page of the list (D60): page.Cursor is the Next of the
// page before, "" for the first page, and page.Limit the most items the
// page holds, 1 to 100, or 0 for 20. ListTagsAll yields every item.
func (n *CatalogNamespace) ListTags(
	ctx context.Context,
	page runtime.PageRequest,
) (runtime.Page[string], error) {
	path := "/api/tags"
	if pageErrors := pageLimitErrors(page.Limit, 100); pageErrors.HasErrors() {
		var zero runtime.Page[string]
		return zero, NewValidationError(pageErrors)
	}
	queryValues := url.Values{}
	runtime.AddPageRequest(queryValues, page)

	var requestBody any
	var out runtime.Page[string]
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		queryValues,
		requestBody,
		&out,
	); err != nil {
		var zero runtime.Page[string]
		return zero, err
	}
	return out, nil
}

// ListTagsAll yields the items of every page of ListTags in turn (D60),
// asking for each page with the Next of the page before, from page.Cursor
// ("" for the first page) with page.Limit items a page. It asks for a page
// only when the loop has taken every item of the one before, and stops
// after the page whose Next is nil. An error ends the iteration: the one a
// page failed with, or one for a page whose Next is the cursor it was
// asked with.
func (n *CatalogNamespace) ListTagsAll(
	ctx context.Context,
	page runtime.PageRequest,
) iter.Seq2[string, error] {
	return runtime.AllItems("Catalog.listTags", page, func(page runtime.PageRequest) (runtime.Page[string], error) {
		return n.ListTags(ctx, page)
	})
}

// PlaceBasket Place a basket.
func (n *CatalogNamespace) PlaceBasket(
	ctx context.Context,
	input types.BasketInput,
) (types.BasketView, error) {
	path := "/api/baskets"
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.BasketView
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.BasketView
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.BasketView
		return zero, err
	}
	return out, nil
}
