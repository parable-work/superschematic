package namespaces

import (
	"context"
	"fmt"
	"net/url"
	"regexp"

	"example.com/schemas/sdk/go/query-lists-api/runtime"
	types "example.com/schemas/types/go/query-lists-api"
)

// PostNamespace contains API methods for the post namespace.
//
// Endpoints:
// - CountPosts: Count the posts that match every filter.
type PostNamespace struct {
	client jsonClient
}

// NewPostNamespace creates a PostNamespace.
func NewPostNamespace(
	client jsonClient,
) *PostNamespace {
	return &PostNamespace{
		client: client,
	}
}

type PostCountPostsQueryParams struct {
	Ids    []types.IdentityUUID `json:"ids"`
	Shades []types.Shade        `json:"shades,omitzero"`
	Ranks  []int64              `json:"ranks,omitzero"`
	Codes  []string             `json:"codes,omitzero"`
	Tags   []string             `json:"tags,omitzero"`
	Flags  []bool               `json:"flags,omitzero"`
	Limit  *float64             `json:"limit,omitempty"`
}

// CountPosts Count the posts that match every filter.
func (n *PostNamespace) CountPosts(
	ctx context.Context,
	query *PostCountPostsQueryParams,
) (float64, error) {
	path := "/api/posts/count"
	queryParamValidationErrors := types.NewValidationErrors()
	if query == nil || len(query.Ids) == 0 {
		queryParamValidationErrors.AddFieldError("ids", "required", "required field")
	}
	if query != nil {
		// ids is sent as one comma-separated value, and an empty list
		// is left out. Each item is checked at ids[i].
		for i, item := range query.Ids {
			itemPath := fmt.Sprintf("ids[%d]", i)
			itemText := fmt.Sprint(item)
			if !checkQueryListItem(queryParamValidationErrors, itemPath, itemText) {
				continue
			}
			validateListElement(queryParamValidationErrors, itemPath, &item)
		}
		// shades is sent as one comma-separated value, and an empty list
		// is left out. Each item is checked at shades[i].
		if n := len(query.Shades); n > 0 && n < 2 {
			queryParamValidationErrors.AddFieldError("shades", "listMin", "must contain at least 2 items")
		}
		if len(query.Shades) > 3 {
			queryParamValidationErrors.AddFieldError("shades", "listMax", "must contain at most 3 items")
		}
		for i, item := range query.Shades {
			itemPath := fmt.Sprintf("shades[%d]", i)
			itemText := fmt.Sprint(item)
			if !checkQueryListItem(queryParamValidationErrors, itemPath, itemText) {
				continue
			}
			validateListElement(queryParamValidationErrors, itemPath, &item)
		}
		// ranks is sent as one comma-separated value, and an empty list
		// is left out. Each item is checked at ranks[i].
		for i, item := range query.Ranks {
			itemPath := fmt.Sprintf("ranks[%d]", i)
			if item < 1 {
				queryParamValidationErrors.AddFieldError(itemPath, "min", "must be at least 1")
			} else if item > 100 {
				queryParamValidationErrors.AddFieldError(itemPath, "max", "must be at most 100")
			}
		}
		// codes is sent as one comma-separated value, and an empty list
		// is left out. Each item is checked at codes[i].
		for i, item := range query.Codes {
			itemPath := fmt.Sprintf("codes[%d]", i)
			itemText := fmt.Sprint(item)
			if !checkQueryListItem(queryParamValidationErrors, itemPath, itemText) {
				continue
			}
			if len([]rune(itemText)) < 2 {
				queryParamValidationErrors.AddFieldError(itemPath, "minLength", "must be at least 2 characters")
			} else if len([]rune(itemText)) > 4 {
				queryParamValidationErrors.AddFieldError(itemPath, "maxLength", "must be at most 4 characters")
			} else if matched, err := regexp.MatchString("^[a-z]+$", itemText); err != nil || !matched {
				queryParamValidationErrors.AddFieldError(itemPath, "pattern", "invalid format")
			}
		}
		// tags is sent as one comma-separated value, and an empty list
		// is left out. Each item is checked at tags[i].
		for i, item := range query.Tags {
			itemPath := fmt.Sprintf("tags[%d]", i)
			itemText := fmt.Sprint(item)
			checkQueryListItem(queryParamValidationErrors, itemPath, itemText)
		}
		if query.Limit != nil {
			queryParamValue := *query.Limit
			queryParamFieldErrors := []types.ValidationError{}
			if queryParamValue > 50 {
				queryParamFieldErrors = append(queryParamFieldErrors, types.ValidationError{Validator: "max", Message: "limit must be at most 50"})
			}
			if len(queryParamFieldErrors) > 0 {
				queryParamValidationErrors.SetFieldErrors("limit", queryParamFieldErrors)
			}
		}
	}
	if queryParamValidationErrors.HasErrors() {
		var zero float64
		return zero, NewValidationError(queryParamValidationErrors)
	}
	var queryValues url.Values
	if query != nil {
		queryValues = url.Values{}
		runtime.AddQueryList(queryValues, "ids", query.Ids)
		runtime.AddQueryList(queryValues, "shades", query.Shades)
		runtime.AddQueryList(queryValues, "ranks", query.Ranks)
		runtime.AddQueryList(queryValues, "codes", query.Codes)
		runtime.AddQueryList(queryValues, "tags", query.Tags)
		runtime.AddQueryList(queryValues, "flags", query.Flags)
		if query.Limit != nil {
			runtime.AddQueryParam(queryValues, "limit", *query.Limit)
		}
	}

	var requestBody any
	var out float64
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		queryValues,
		requestBody,
		&out,
	); err != nil {
		var zero float64
		return zero, err
	}
	return out, nil
}
