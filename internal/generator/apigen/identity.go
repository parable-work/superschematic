package apigen

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
