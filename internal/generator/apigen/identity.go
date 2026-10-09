package apigen

import (
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The user model's operations (D50), the ones the loader adds to a
// @userSessions or @userAdministration set, are the identity runtime's to
// serve: the implementation the project writes has no method for them. The
// OpenAPI document, the SDKs and the tool manifest read them as any other
// operation, so they stay in APIOutput.Endpoints, and each server writer
// leaves them out of what it renders the implementation from.

// ImplementedOutput returns the view of output the Go server's
// implementation interfaces, scaffolds and router are written from: the
// endpoints without the user model's operations (EndpointInfo.
// IdentityOperation), which the identity runtime serves (D50), and the
// flags, namespaces and imports those endpoints give. The OpenAPI document
// it embeds still describes every route. output itself is unchanged, and
// an output without such an operation is returned as it is.
func ImplementedOutput(output *APIOutput) (*APIOutput, error) {
	identity := false
	for _, endpoint := range output.Endpoints {
		identity = identity || endpoint.IdentityOperation != ""
	}
	if !identity {
		return output, nil
	}
	view := *output
	view.Endpoints = make([]EndpointInfo, 0, len(output.Endpoints))
	for _, endpoint := range output.Endpoints {
		if endpoint.IdentityOperation == "" {
			view.Endpoints = append(view.Endpoints, endpoint)
		}
	}
	if err := view.summarizeEndpoints(); err != nil {
		return nil, err
	}
	imports, err := rawBodyCheckImports(view.Endpoints)
	if err != nil {
		return nil, err
	}
	view.RawBodyCheckImports = imports
	return &view, nil
}

// addIdentityErrorResponses adds to an operation's OpenAPI responses the
// errors the identity runtime answers the user model's operation op with
// (ir.IdentityOperationErrors): one response per status, whose description
// names each code it carries and when.
func addIdentityErrorResponses(responses map[string]interface{}, op string) {
	var statuses []int
	meanings := map[int][]string{}
	for _, e := range ir.IdentityOperationErrors(op) {
		if _, seen := meanings[e.Status]; !seen {
			statuses = append(statuses, e.Status)
		}
		meanings[e.Status] = append(meanings[e.Status], e.Code+": "+e.Meaning)
	}
	for _, status := range statuses {
		responses[strconv.Itoa(status)] = map[string]interface{}{
			"description": strings.Join(meanings[status], " "),
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": map[string]interface{}{
						"$ref": "#/components/schemas/Error",
					},
				},
			},
		}
	}
}
