package namespaces

import (
	"context"

	types "example.com/schemas/types/go/fixture-user-routes-api"
)

// GreetingNamespace contains API methods for the greeting namespace.
//
// Endpoints:
// - Greet: Greet endpoint. Requires authentication.
type GreetingNamespace struct {
	client jsonClient
}

// NewGreetingNamespace creates a GreetingNamespace.
func NewGreetingNamespace(
	client jsonClient,
) *GreetingNamespace {
	return &GreetingNamespace{
		client: client,
	}
}

// Greet calls the GET /api/greeting endpoint.
// Requires authentication.
func (n *GreetingNamespace) Greet(
	ctx context.Context,
) (types.Greeting, error) {
	path := "/api/greeting"

	var requestBody any
	var out types.Greeting
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.Greeting
		return zero, err
	}
	return out, nil
}
