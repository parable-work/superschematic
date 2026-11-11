package namespaces

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/url"

	"example.com/schemas/sdk/go/fixture-paged-api/runtime"
	types "example.com/schemas/types/go/fixture-paged-api"
	"github.com/parable-work/superschematic/runtime/http/go/filterparse"
)

// CatalogNamespace contains API methods for the catalog namespace.
//
// Endpoints:
// - GetItem: One item.
// - ListItems: The items, two to a page unless the caller asks for up to five, by their ids or their stock, in order of their names unless the caller sorts them by name or sku, either way.
// - ListParts: The parts, in order of their names unless the caller sorts them by quantity, kept to the ones the caller's filters find (D106): a filter on a field of each kind, beside a page and a sort.
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

// CatalogListPartsFilter is the filter of ListParts (FilterBy, D106): the rows
// the list keeps. Each member is a field's filter, nil for none: Eq, and
// In, one of the values, on every field; Gt, Gte, Lt and Lte on a number,
// a date and a time; Contains, the text a value holds in any case, on
// text; and IsNull, true for the rows whose field is null and false for
// the others, on a field that may be null. A row is kept when every
// member holds. The method sends the filter as filter[field]=value and
// filter[field][op]=value, and refuses, before the request, an In of more
// than 100 values or of one that is blank or holds a comma or surrounding
// space, a Contains of no text, and more than 50 filters.
type CatalogListPartsFilter struct {
	Id           *filterparse.Equals[types.IdentityUUID]      `json:"id,omitempty"`
	Name         *filterparse.Text[string]                    `json:"name,omitempty"`
	Kind         *filterparse.Equals[types.PartKind]          `json:"kind,omitempty"`
	Quantity     *filterparse.Ordered[int64]                  `json:"quantity,omitempty"`
	Weight       *filterparse.Ordered[float64]                `json:"weight,omitempty"`
	Price        *filterparse.Ordered[types.GenericDecimal]   `json:"price,omitempty"`
	Discontinued *filterparse.Equals[bool]                    `json:"discontinued,omitempty"`
	MadeAt       *filterparse.Ordered[types.TemporalDateTime] `json:"madeAt,omitempty"`
	RetiredOn    *filterparse.Ordered[types.TemporalDate]     `json:"retiredOn,omitempty"`
	SupplierId   *filterparse.Equals[types.IdentityUUID]      `json:"supplierId,omitempty"`
}

// Filters are the filters the members set, field by field, which
// filterparse.Query writes as the bracket form. A nil filter is none.
func (f *CatalogListPartsFilter) Filters() []filterparse.Filter {
	if f == nil {
		return nil
	}
	var filters []filterparse.Filter
	filters = append(filters, f.Id.Filters("id")...)
	filters = append(filters, f.Name.Filters("name")...)
	filters = append(filters, f.Kind.Filters("kind")...)
	filters = append(filters, f.Quantity.Filters("quantity")...)
	filters = append(filters, f.Weight.Filters("weight")...)
	filters = append(filters, f.Price.Filters("price")...)
	filters = append(filters, f.Discontinued.Filters("discontinued")...)
	filters = append(filters, f.MadeAt.Filters("madeAt")...)
	filters = append(filters, f.RetiredOn.Filters("retiredOn")...)
	filters = append(filters, f.SupplierId.Filters("supplierId")...)
	return filters
}

// UnmarshalJSON reads the filter as a tool call sends it: an object with
// a member per field, each a value, for equality, or an object of the
// field's operators. It refuses a field the filter does not hold and an
// operator the field does not take.
func (f *CatalogListPartsFilter) UnmarshalJSON(data []byte) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	var filter CatalogListPartsFilter
	for name, member := range members {
		var err error
		switch name {
		case "id":
			filter.Id, err = filterMember(member, func(m *filterparse.Equals[types.IdentityUUID]) any { return &m.Eq })
		case "name":
			filter.Name, err = filterMember(member, func(m *filterparse.Text[string]) any { return &m.Eq })
		case "kind":
			filter.Kind, err = filterMember(member, func(m *filterparse.Equals[types.PartKind]) any { return &m.Eq })
		case "quantity":
			filter.Quantity, err = filterMember(member, func(m *filterparse.Ordered[int64]) any { return &m.Eq })
		case "weight":
			filter.Weight, err = filterMember(member, func(m *filterparse.Ordered[float64]) any { return &m.Eq })
		case "price":
			filter.Price, err = filterMember(member, func(m *filterparse.Ordered[types.GenericDecimal]) any { return &m.Eq })
		case "discontinued":
			filter.Discontinued, err = filterMember(member, func(m *filterparse.Equals[bool]) any { return &m.Eq })
		case "madeAt":
			filter.MadeAt, err = filterMember(member, func(m *filterparse.Ordered[types.TemporalDateTime]) any { return &m.Eq })
		case "retiredOn":
			filter.RetiredOn, err = filterMember(member, func(m *filterparse.Ordered[types.TemporalDate]) any { return &m.Eq })
		case "supplierId":
			filter.SupplierId, err = filterMember(member, func(m *filterparse.Equals[types.IdentityUUID]) any { return &m.Eq })
		default:
			err = fmt.Errorf("the filter has no such field")
		}
		if err != nil {
			return fmt.Errorf("filter.%s: %w", name, err)
		}
	}
	*f = filter
	return nil
}

type CatalogListPartsQueryParams struct {
	Sort *string `json:"sort,omitempty"`
	// Filter keeps the rows its members find (D106); nil keeps every row.
	Filter *CatalogListPartsFilter `json:"filter,omitempty"`
}

// ListParts The parts, in order of their names unless the caller sorts them by quantity, kept to the ones the caller's filters find (D106): a filter on a field of each kind, beside a page and a sort.
//
// It returns one page of the list (D60): page.Cursor is the Next of the
// page before, "" for the first page, and page.Limit the most items the
// page holds, 1 to 5, or 0 for 2. ListPartsAll yields every item.
//
// query.Filter keeps the rows its members find (D106); a cursor holds no
// filter, so each next page is asked with the same one, as ListPartsAll does.
func (n *CatalogNamespace) ListParts(
	ctx context.Context,
	query *CatalogListPartsQueryParams,
	page runtime.PageRequest,
) (runtime.Page[types.PartView], error) {
	path := "/api/parts"
	if pageErrors := pageLimitErrors(page.Limit, 5); pageErrors.HasErrors() {
		var zero runtime.Page[types.PartView]
		return zero, NewValidationError(pageErrors)
	}
	queryValues := url.Values{}
	if query != nil {
		if query.Sort != nil {
			runtime.AddQueryParam(queryValues, "sort", *query.Sort)
		}
		// The filter (D106) travels as the bracket form, filter[field]=value
		// and filter[field][op]=value, which filterparse.Query writes. One
		// the route's reader would refuse or read as something else is
		// refused here (filterparse.Check).
		filters := query.Filter.Filters()
		if err := filterparse.Check(filters); err != nil {
			var zero runtime.Page[types.PartView]
			return zero, filterError(err)
		}
		for name, values := range filterparse.Query(filters) {
			queryValues[name] = values
		}
	}
	runtime.AddPageRequest(queryValues, page)

	var requestBody any
	var out runtime.Page[types.PartView]
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		queryValues,
		requestBody,
		&out,
	); err != nil {
		var zero runtime.Page[types.PartView]
		return zero, err
	}
	return out, nil
}

// ListPartsAll yields the items of every page of ListParts in turn (D60),
// asking for each page with the Next of the page before, from page.Cursor
// ("" for the first page) with page.Limit items a page. It asks for a page
// only when the loop has taken every item of the one before, and stops
// after the page whose Next is nil. An error ends the iteration: the one a
// page failed with, or one for a page whose Next is the cursor it was
// asked with.
func (n *CatalogNamespace) ListPartsAll(
	ctx context.Context,
	query *CatalogListPartsQueryParams,
	page runtime.PageRequest,
) iter.Seq2[types.PartView, error] {
	return runtime.AllItems("Catalog.listParts", page, func(page runtime.PageRequest) (runtime.Page[types.PartView], error) {
		return n.ListParts(ctx, query, page)
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
