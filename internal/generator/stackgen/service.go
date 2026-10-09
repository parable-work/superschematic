package stackgen

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// Service reads what resolution needs to know about one service
// (docs/stack-model.md, section 6.10) from its IR and the outputs block of
// its config:
//
//   - authDb, dependencies and calls from ir.Schema. The IR keeps authDb's
//     name, and the handle it held names a DB service;
//   - an API's outputs.api.language and a DB's outputs.sql.dialects;
//   - an API's operations, each with what admits a caller to it
//     (stack.OperationsOf), against which the resolver checks each edge to
//     the API (D37);
//   - an API's @envVars type, read as the env loaders read it
//     (envgen), with each field's Secret, Default and InheritedFrom;
//   - an API's jobs, each a deployable of the stack (D52).
//
// dependencies are the schemas the service imports types from, by service
// name, so that the default of an imported enum field reads as its value.
func Service(schema *ir.Schema, outputs *registry.Outputs, dependencies map[string]*ir.Schema) (stack.Service, error) {
	svc := stack.Service{
		Name:         schema.Name,
		Kind:         schema.Kind,
		Dependencies: schema.Dependencies,
		Calls:        schema.Calls,
	}
	if schema.AuthDB != "" {
		svc.AuthDB = &ir.ServiceRef{Name: schema.AuthDB, Kind: ir.SchemaKindDB}
	}
	switch schema.Kind {
	case ir.SchemaKindAPI:
		if outputs.APIEnabled() {
			svc.Language = outputs.API.Language
		}
		config, err := configOf(schema, dependencies)
		if err != nil {
			return stack.Service{}, err
		}
		svc.Config = config
		svc.Operations = stack.OperationsOf(schema)
		for _, job := range schema.Jobs {
			if job != nil {
				fact := *job
				fact.Comment = ""
				svc.Jobs = append(svc.Jobs, fact)
			}
		}
	case ir.SchemaKindDB:
		svc.Dialects = outputs.SQLDialects()
	case ir.SchemaKindSite:
		// Its calls are the APIs its code calls from the browser; where its
		// code is, the stack's build reads with SiteOf (D55).
	}
	return svc, nil
}

// configOf reads a service's @envVars type, or nil when it has none.
func configOf(schema *ir.Schema, dependencies map[string]*ir.Schema) (*stack.Config, error) {
	contract, err := envgen.GenerateWithOptions(schema, envgen.Options{SchemaName: schema.Name, Dependencies: dependencies})
	if err != nil {
		return nil, fmt.Errorf("service %s: @envVars: %w", schema.Name, err)
	}
	if contract == nil {
		return nil, nil
	}
	declaredBy := map[string]string{}
	if td := schema.Types[contract.TypeName]; td != nil {
		for _, field := range td.Fields {
			declaredBy[field.Name] = field.InheritedFrom
		}
	}
	config := &stack.Config{Type: contract.TypeName}
	for _, f := range contract.Fields {
		field := stack.ConfigField{
			Name:          f.Key,
			Required:      f.Required,
			Secret:        f.Secret,
			InheritedFrom: declaredBy[f.Key],
		}
		if f.HasDefault {
			value := f.DefaultValue
			field.Default = &value
		}
		config.Fields = append(config.Fields, field)
	}
	return config, nil
}
