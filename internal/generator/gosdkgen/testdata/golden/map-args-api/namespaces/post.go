package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/map-args-api"
)

// PostNamespace contains API methods for the post namespace.
//
// Endpoints:
// - NameThings: Name the shades, links, points and weights of a post.
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

type PostNameThingsInput struct {
	ShadeByName   map[string]types.Shade        `json:"shadeByName"`
	LinksByLocale map[string][]types.NetworkUrl `json:"linksByLocale,omitzero"`
	PointByName   map[string]types.Point        `json:"pointByName,omitzero"`
	WeightByName  map[string]float64            `json:"weightByName,omitzero"`
}

// NameThings Name the shades, links, points and weights of a post.
func (n *PostNamespace) NameThings(
	ctx context.Context,
	Id string,
	input PostNameThingsInput,
) (bool, error) {
	path := fmt.Sprintf("/api/posts/%v/names", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	// shadeByName is a map, sent as a JSON object; {} is a value.
	// Each value runs its own validation, reported at shadeByName[key].
	if input.ShadeByName == nil {
		validationErrors.AddFieldError("shadeByName", "required", "required field")
	}
	for key, value := range input.ShadeByName {
		validateListElement(validationErrors, fmt.Sprintf("shadeByName[%s]", key), &value)
	}
	// linksByLocale is a map, sent as a JSON object; {} is a value.
	// A list value is never nil, reported at linksByLocale[key].
	// Each element runs its own validation, reported at linksByLocale[key][i].
	for key, value := range input.LinksByLocale {
		if value == nil {
			validationErrors.AddFieldError(fmt.Sprintf("linksByLocale[%s]", key), "required", "required field")
			continue
		}
		for i := range value {
			validateListElement(validationErrors, fmt.Sprintf("linksByLocale[%s][%d]", key, i), &value[i])
		}
	}
	// pointByName is a map, sent as a JSON object; {} is a value.
	// Each value runs its own validation, reported at pointByName[key].
	for key, value := range input.PointByName {
		validateListElement(validationErrors, fmt.Sprintf("pointByName[%s]", key), &value)
	}
	// weightByName is a map, sent as a JSON object; {} is a value.
	if validationErrors.HasErrors() {
		var zero bool
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out bool
	if err := n.client.DoJSON(
		ctx,
		"PUT",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero bool
		return zero, err
	}
	return out, nil
}
