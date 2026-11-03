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
// IdentityOperation), which the identity runtime serves (D50), in
// IdentityEndpoints, and the flags, namespaces and imports the endpoints
// give. The router mounts the identity runtime's routes too, so the flags
// it reads count them (HasAuth, RoutesNeedTime, HasServiceCallers): a
// server whose only routes that need a caller are the user model's still
// wires its auth middleware. The
// OpenAPI document it embeds still describes every route. output itself is
// unchanged, and an output without such an operation is returned as it is.
//
// A table's behavior operations (D103, EndpointInfo.BehaviorOperation) go
// to BehaviorEndpoints in the same way: the generated handlers run them
// through the ORM of the API's database, so the interfaces and the
// scaffold have no method for them, and a namespace whose every operation
// is a behavior's has no implementation. The router mounts them, so the
// flags it reads count them, the permission check's imports included.
func ImplementedOutput(output *APIOutput) (*APIOutput, error) {
	identity, behavior := false, false
	for _, endpoint := range output.Endpoints {
		identity = identity || endpoint.IdentityOperation != ""
		behavior = behavior || endpoint.BehaviorOperation() != nil
	}
	if !identity && !behavior {
		return output, nil
	}
	view := *output
	view.Endpoints = make([]EndpointInfo, 0, len(output.Endpoints))
	view.IdentityEndpoints = nil
	view.BehaviorEndpoints = nil
	for _, endpoint := range output.Endpoints {
		switch {
		case endpoint.IdentityOperation != "":
			view.IdentityEndpoints = append(view.IdentityEndpoints, endpoint)
		case endpoint.BehaviorOperation() != nil:
			view.BehaviorEndpoints = append(view.BehaviorEndpoints, endpoint)
		default:
			view.Endpoints = append(view.Endpoints, endpoint)
		}
	}
	if err := view.summarizeEndpoints(); err != nil {
		return nil, err
	}
	// The router mounts the identity runtime's routes too: one that needs
	// a caller sits behind the auth middleware, and one with a rate limit
	// calls time. Its permission check is the identity service's, not the
	// provider's routePermissions, so HasPermissionEndpoints, which gates
	// the snippet's imports, counts the project's routes alone.
	for _, endpoint := range view.IdentityEndpoints {
		permissions := view.HasPermissionEndpoints
		view.summarizeRoute(endpoint)
		view.HasPermissionEndpoints = permissions
	}
	// A behavior operation's route checks its permissions as the
	// project's routes do, with the provider's routePermissions.
	for _, endpoint := range view.BehaviorEndpoints {
		view.summarizeRoute(endpoint)
	}
	imports, err := rawBodyCheckImports(view.HandlerEndpoints())
	if err != nil {
		return nil, err
	}
	view.RawBodyCheckImports = imports
	return &view, nil
}

// IdentityRegistration returns schema's @userSessions config when its
// register admits users otherwise than open registration with no roles
// does (D81): under approval, with default roles or with a first
// administrator. Every server's identity wiring hands it to its runtime;
// for any other API it is nil and the wiring is unchanged.
func IdentityRegistration(schema *ir.Schema) *ir.UserSessionsConfig {
	for _, set := range schema.OperationSets {
		cfg := set.UserSessions
		if cfg != nil && cfg.Register && !cfg.NoLogin && (cfg.Approval() || len(cfg.DefaultRoles) > 0 || cfg.FirstUser != nil) {
			return cfg
		}
	}
	return nil
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
