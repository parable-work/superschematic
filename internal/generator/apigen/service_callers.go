package apigen

import ir "github.com/parable-work/superschematic/ir"

// OpenAPIServiceCallersKey is the vendor-extension key under which an
// operation lists the API services its service clause names (D37). It is
// absent when the clause names none, which admits every server with a calls
// edge to the API.
const OpenAPIServiceCallersKey = "x-service-callers"

// openAPIServiceAuthScheme names the security scheme of a service
// credential.
const openAPIServiceAuthScheme = "serviceAuth"

// openAPIServiceAuth is the serviceAuth security scheme. OpenAPI's http
// bearer scheme always means the Authorization header, which stays the end
// user's, so the service credential is an apiKey scheme on its own header
// whose value carries the Bearer prefix.
func openAPIServiceAuth() map[string]interface{} {
	return map[string]interface{}{
		"type":        "apiKey",
		"in":          "header",
		"name":        "Service-Authorization",
		"description": "The calling service's credential, a short-lived JWT sent as `Bearer <jwt>`. Authorization stays the end user's.",
	}
}

// ServiceOnly reports whether only a service may call the endpoint: its
// service clause is @requireService, with or without a user clause, so no
// end user's agent can call it and the tool documents leave it out.
func (e EndpointInfo) ServiceOnly() bool {
	return e.ServiceCallers != nil && e.ServiceCallers.Mode == ir.ServiceCallersRequire
}

// operationSecurity writes who may call the endpoint as an OpenAPI security
// list, which is an OR of ANDs (D37): the end user alone (bearerAuth, as
// before service clauses), the service alone (@requireService), the
// service and a forwarded end user (@requireService with a user clause),
// or either (@allowService, which needs a user clause). Nil means anyone.
func operationSecurity(endpoint EndpointInfo) []map[string]interface{} {
	user := map[string]interface{}{"bearerAuth": []string{}}
	service := map[string]interface{}{openAPIServiceAuthScheme: []string{}}
	clause := endpoint.ServiceCallers
	switch {
	case clause == nil && endpoint.RequiresAuth:
		return []map[string]interface{}{user}
	case clause == nil:
		return nil
	case clause.Mode == ir.ServiceCallersAllow:
		return []map[string]interface{}{user, service}
	case endpoint.RequiresAuth:
		return []map[string]interface{}{{openAPIServiceAuthScheme: []string{}, "bearerAuth": []string{}}}
	}
	return []map[string]interface{}{service}
}
