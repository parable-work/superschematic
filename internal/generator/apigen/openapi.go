package apigen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// generateOpenAPISpec builds the OpenAPI 3.0 document for the API output.
// Returns (rawJSON, backtickEscapedForGoEmbed, error). The version and base
// URL carry placeholder tokens that the generated routes.go replaces at
// runtime with configured values.
func generateOpenAPISpec(output *APIOutput, schema *ir.Schema, dependencies map[string]*ir.Schema) (string, string, error) {
	scalarMap := buildOpenAPIScalarMap(schema, dependencies)
	scalarExamples, scalarDescriptions := collectOpenAPIScalarMetadata(schema, dependencies)

	spec := map[string]interface{}{
		"openapi": "3.0.3",
		"info": map[string]interface{}{
			"title":       fmt.Sprintf("%s API", codegen.TitleCase(output.SchemaName)),
			"description": fmt.Sprintf("REST API generated from the %s schema", output.SchemaName),
			"version":     "__PSGEN_OPENAPI_VERSION__",
		},
		"servers": []map[string]interface{}{
			{
				"url":         "__PSGEN_OPENAPI_BASE_URL__",
				"description": "Runtime server",
			},
		},
		"paths": buildOpenAPIPaths(output, scalarExamples, scalarDescriptions, scalarMap, schema, dependencies),
		"components": map[string]interface{}{
			"schemas": buildOpenAPISchemas(output.Endpoints, scalarExamples, scalarDescriptions, scalarMap, schema, dependencies),
			"securitySchemes": map[string]interface{}{
				"bearerAuth": map[string]interface{}{
					"type":         "http",
					"scheme":       "bearer",
					"bearerFormat": "JWT",
				},
			},
		},
	}

	specJSON, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("marshal OpenAPI spec: %w", err)
	}

	rawSpec := string(specJSON)
	escapedSpec := strings.ReplaceAll(rawSpec, "`", "` + \"`\" + `")
	return rawSpec, escapedSpec, nil
}

// buildOpenAPIScalarMap maps every resolvable type name to a JSON-schema-ish
// primitive token: "string", "number", "integer", "boolean", or "object".
// Bare language primitives map directly; semantic scalars map through their
// LanguagePrimitive (refined to "integer" by scalar traits) with a
// json_schema type-mapping override.
func buildOpenAPIScalarMap(schema *ir.Schema, dependencies map[string]*ir.Schema) map[string]string {
	scalarMap := map[string]string{
		codegen.PrimitiveString:  "string",
		codegen.PrimitiveNumber:  "number",
		codegen.PrimitiveBoolean: "boolean",
	}

	addScalars := func(scalars map[string]*ir.ScalarDef) {
		for name, scalarDef := range scalars {
			primitive := scalarDef.LanguagePrimitive.String()
			if primitive == "number" {
				tokens := codegen.BuildScalarTokens(name)
				if codegen.BuildScalarTraits(scalarDef, tokens, "").IsIntegerLike {
					primitive = "integer"
				}
			}
			if jsType, ok := scalarDef.TypeMappings["json_schema"]; ok && jsType == "object" {
				primitive = "object"
			}
			if primitive == "" {
				primitive = "string"
			}
			scalarMap[name] = primitive
		}
	}

	addScalars(schema.Scalars)
	for _, depSchema := range dependencies {
		if depSchema != nil {
			addScalars(depSchema.Scalars)
		}
	}

	return scalarMap
}

func collectOpenAPIScalarMetadata(schema *ir.Schema, dependencies map[string]*ir.Schema) (map[string]string, map[string]string) {
	scalarExamples := make(map[string]string)
	scalarDescriptions := make(map[string]string)
	addMetadata := func(scalars map[string]*ir.ScalarDef) {
		for name, scalarDef := range scalars {
			if scalarDef.Example != "" {
				scalarExamples[name] = scalarDef.Example
			}
			if doc := codegen.DocText(scalarDef.Description, scalarDef.Comment); doc != "" {
				scalarDescriptions[name] = doc
			}
		}
	}

	addMetadata(schema.Scalars)
	for _, depSchema := range dependencies {
		if depSchema != nil {
			addMetadata(depSchema.Scalars)
		}
	}

	return scalarExamples, scalarDescriptions
}

// buildCommonHeaderParameters lists the header parameters every operation
// carries: the provider's own (X-Tenant for Parable) then X-Request-ID on
// public APIs.
func buildCommonHeaderParameters(output *APIOutput) []map[string]interface{} {
	parameters := []map[string]interface{}{}
	parameters = append(parameters, output.Provider.OpenAPIParameters(output)...)
	if !output.IsPublic {
		return parameters
	}
	return append(parameters, map[string]interface{}{
		"name":        "X-Request-ID",
		"in":          "header",
		"required":    false,
		"description": "Optional request correlation ID echoed back in the response.",
		"schema": map[string]interface{}{
			"type":    "string",
			"format":  "uuid",
			"example": "550e8400-e29b-41d4-a716-446655440000",
		},
	})
}

// buildScalarQueryParams converts GET endpoint scalar args into OpenAPI query
// parameter definitions. Array args use style=form + explode=false
// (comma-separated: ?k=a,b) per EDR-0002 format conventions.
func buildScalarQueryParams(args []Param, scalarExamples, scalarDescriptions, scalarMap map[string]string) []map[string]interface{} {
	var params []map[string]interface{}
	for _, arg := range args {
		argSchema := typeToOpenAPISchema(arg.Type, scalarExamples, scalarDescriptions, scalarMap)
		applyOpenAPIValidationConstraints(argSchema, arg.ValidateMin, arg.ValidateMax, arg.ValidateMinLength, arg.ValidateMaxLength, arg.ValidatePattern, nil, nil)
		param := map[string]interface{}{
			"name":     arg.Name,
			"in":       "query",
			"required": arg.Required,
			"schema":   argSchema,
		}
		if arg.IsArray {
			arraySchema := map[string]interface{}{
				"type":  "array",
				"items": argSchema,
			}
			applyOpenAPIValidationConstraints(arraySchema, nil, nil, nil, nil, "", arg.ValidateListMin, arg.ValidateListMax)
			param["schema"] = arraySchema
			param["style"] = "form"
			param["explode"] = false
		}
		params = append(params, param)
	}
	return params
}

// buildQueryParamDefs converts @query-decorated params into OpenAPI query
// parameter definitions.
func buildQueryParamDefs(params []Param, scalarExamples, scalarDescriptions, scalarMap map[string]string) []map[string]interface{} {
	var result []map[string]interface{}
	for _, param := range params {
		paramSchema := typeToOpenAPISchema(param.Type, scalarExamples, scalarDescriptions, scalarMap)
		applyOpenAPIValidationConstraints(paramSchema, param.ValidateMin, param.ValidateMax, param.ValidateMinLength, param.ValidateMaxLength, param.ValidatePattern, param.ValidateListMin, param.ValidateListMax)
		result = append(result, map[string]interface{}{
			"name":     param.Name,
			"in":       "query",
			"required": param.Required,
			"schema":   paramSchema,
		})
	}
	return result
}

func applyOpenAPIValidationConstraints(
	schema map[string]interface{},
	validateMin *float64,
	validateMax *float64,
	validateMinLength *int,
	validateMaxLength *int,
	validatePattern string,
	validateListMin *int,
	validateListMax *int,
) {
	if validateMin != nil {
		schema["minimum"] = *validateMin
	}
	if validateMax != nil {
		schema["maximum"] = *validateMax
	}
	if validateMinLength != nil {
		schema["minLength"] = *validateMinLength
	}
	if validateMaxLength != nil {
		schema["maxLength"] = *validateMaxLength
	}
	if validatePattern != "" {
		schema["pattern"] = validatePattern
	}
	if validateListMin != nil {
		schema["minItems"] = *validateListMin
	}
	if validateListMax != nil {
		schema["maxItems"] = *validateListMax
	}
}

func buildOpenAPIPaths(output *APIOutput, scalarExamples, scalarDescriptions, scalarMap map[string]string, schema *ir.Schema, dependencies map[string]*ir.Schema) map[string]interface{} {
	paths := make(map[string]interface{})

	for _, endpoint := range output.Endpoints {
		if paths[endpoint.Path] == nil {
			paths[endpoint.Path] = make(map[string]interface{})
		}
		pathItem := paths[endpoint.Path].(map[string]interface{})

		operation := map[string]interface{}{
			"summary":     endpoint.Name,
			"operationId": endpoint.HandlerName,
			"tags":        []string{endpoint.Namespace},
		}

		if endpoint.Description != "" {
			operation["description"] = endpoint.Description
		}

		parameters := buildCommonHeaderParameters(output)
		for _, param := range endpoint.PathParams {
			paramSchema := typeToOpenAPISchema(param.Type, scalarExamples, scalarDescriptions, scalarMap)
			parameters = append(parameters, map[string]interface{}{
				"name":        param.Name,
				"in":          "path",
				"required":    true, // OpenAPI 3.0 spec: path parameters MUST be required
				"description": fmt.Sprintf("%s parameter", param.Name),
				"schema":      paramSchema,
			})
		}

		// GET endpoints carry scalar args in the query string instead of a body.
		if endpoint.Method == "GET" && len(endpoint.ScalarArgs) > 0 {
			parameters = append(parameters, buildScalarQueryParams(endpoint.ScalarArgs, scalarExamples, scalarDescriptions, scalarMap)...)
		}

		if len(endpoint.QueryParams) > 0 {
			parameters = append(parameters, buildQueryParamDefs(endpoint.QueryParams, scalarExamples, scalarDescriptions, scalarMap)...)
		}

		if len(parameters) > 0 {
			operation["parameters"] = parameters
		}

		if endpoint.HasFileUpload && endpoint.HasInput {
			properties := make(map[string]interface{})
			required := []string{}

			properties["data"] = map[string]interface{}{
				"type":        "string",
				"format":      "json",
				"description": fmt.Sprintf("JSON string containing the %s fields (excluding file fields)", endpoint.InputType),
			}
			required = append(required, "data")

			for _, fileField := range endpoint.FileUploadFields {
				fieldDescription := fmt.Sprintf("File upload for %s", fileField.Name)
				if fileField.Category != "" {
					fieldDescription = fmt.Sprintf("%s file upload", fileField.Category)
				}
				if len(fileField.AllowedTypes) > 0 {
					fieldDescription = fmt.Sprintf("%s (allowed types: %s)", fieldDescription, strings.Join(fileField.AllowedTypes, ", "))
				}
				if fileField.MaxSize > 0 {
					fieldDescription = fmt.Sprintf("%s, max size: %d bytes", fieldDescription, fileField.MaxSize)
				}

				properties[fileField.Name] = map[string]interface{}{
					"type":        "string",
					"format":      "binary",
					"description": fieldDescription,
				}

				if fileField.Required {
					required = append(required, fileField.Name)
				}
			}

			schemaObj := map[string]interface{}{
				"type":       "object",
				"properties": properties,
			}
			if len(required) > 0 {
				schemaObj["required"] = required
			}

			operation["requestBody"] = map[string]interface{}{
				"required": true,
				"content": map[string]interface{}{
					"multipart/form-data": map[string]interface{}{
						"schema": schemaObj,
					},
				},
			}
		} else if endpoint.HasInput {
			operation["requestBody"] = map[string]interface{}{
				"required": true,
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": fmt.Sprintf("#/components/schemas/%s", endpoint.InputType),
						},
					},
				},
			}
		} else if len(endpoint.ScalarArgs) > 0 && endpoint.Method != "GET" {
			properties := make(map[string]interface{})
			required := []string{}

			for _, arg := range endpoint.ScalarArgs {
				argSchema := typeToOpenAPISchema(arg.Type, scalarExamples, scalarDescriptions, scalarMap)
				if arg.Required {
					required = append(required, arg.Name)
				} else {
					argSchema = nullableOpenAPISchema(argSchema, openAPIRefType(arg.Type, schema, dependencies))
				}
				properties[arg.Name] = argSchema
			}

			schemaObj := map[string]interface{}{
				"type":       "object",
				"properties": properties,
			}
			if len(required) > 0 {
				schemaObj["required"] = required
			}

			operation["requestBody"] = map[string]interface{}{
				"required": true,
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": schemaObj,
					},
				},
			}
		}

		responseSchema := typeToOpenAPISchema(endpoint.OutputType, scalarExamples, scalarDescriptions, scalarMap)
		if endpoint.OutputIsArray {
			responseSchema = map[string]interface{}{
				"type":  "array",
				"items": responseSchema,
			}
		}
		envelopeSchema := map[string]interface{}{
			"type":     "object",
			"required": []string{"data", "meta"},
			"properties": map[string]interface{}{
				"data":  responseSchema,
				"meta":  map[string]interface{}{"$ref": "#/components/schemas/ResponseMeta"},
				"links": map[string]interface{}{"$ref": "#/components/schemas/ResponseLinks"},
			},
		}

		operation["responses"] = map[string]interface{}{
			"200": map[string]interface{}{
				"description": "Successful response",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": envelopeSchema,
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/Error",
						},
					},
				},
			},
			"500": map[string]interface{}{
				"description": "Internal server error",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/Error",
						},
					},
				},
			},
		}

		if endpoint.RequiresAuth {
			operation["security"] = []map[string]interface{}{
				{
					"bearerAuth": []string{},
				},
			}
		}

		switch strings.ToLower(endpoint.Method) {
		case "get":
			pathItem["get"] = operation
		case "post":
			pathItem["post"] = operation
		case "put":
			pathItem["put"] = operation
		case "patch":
			pathItem["patch"] = operation
		case "delete":
			pathItem["delete"] = operation
		}
	}

	return paths
}

// buildOpenAPISchemas builds the components/schemas section from the types
// referenced by endpoints, recursing through field types.
func buildOpenAPISchemas(endpoints []EndpointInfo, scalarExamples, scalarDescriptions, scalarMap map[string]string, schema *ir.Schema, dependencies map[string]*ir.Schema) map[string]interface{} {
	schemas := make(map[string]interface{})
	processedTypes := make(map[string]bool)

	schemas["Error"] = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"error": map[string]interface{}{
				"type": "string",
			},
		},
	}

	schemas["ResponseMeta"] = map[string]interface{}{
		"type":     "object",
		"required": []string{"requestId"},
		"properties": map[string]interface{}{
			"requestId": map[string]interface{}{
				"type":        "string",
				"description": "Unique identifier for the request",
			},
			"totalCount": map[string]interface{}{
				"type":        "integer",
				"nullable":    true,
				"description": "Total count of items for paginated responses",
			},
		},
	}
	schemas["ResponseLinks"] = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"first": map[string]interface{}{"type": "string", "nullable": true},
			"prev":  map[string]interface{}{"type": "string", "nullable": true},
			"next":  map[string]interface{}{"type": "string", "nullable": true},
			"last":  map[string]interface{}{"type": "string", "nullable": true},
		},
	}

	for _, endpoint := range endpoints {
		if endpoint.InputType != "" && !processedTypes[endpoint.InputType] {
			addSchemaFromIR(schemas, endpoint.InputType, schema, dependencies, scalarExamples, scalarDescriptions, scalarMap, processedTypes)
		}
		if endpoint.OutputType != "" && !processedTypes[endpoint.OutputType] {
			addSchemaFromIR(schemas, endpoint.OutputType, schema, dependencies, scalarExamples, scalarDescriptions, scalarMap, processedTypes)
		}
		for _, param := range append(append(endpoint.PathParams, endpoint.QueryParams...), endpoint.ScalarArgs...) {
			if param.Type != "" && !processedTypes[param.Type] {
				addSchemaFromIR(schemas, param.Type, schema, dependencies, scalarExamples, scalarDescriptions, scalarMap, processedTypes)
			}
		}
	}

	return schemas
}

// addSchemaFromIR recursively adds a type and its dependencies to the
// OpenAPI schemas.
func addSchemaFromIR(schemas map[string]interface{}, typeName string, schema *ir.Schema, dependencies map[string]*ir.Schema, scalarExamples, scalarDescriptions, scalarMap map[string]string, processed map[string]bool) {
	if processed[typeName] {
		return
	}
	processed[typeName] = true

	if _, isScalar := scalarMap[typeName]; isScalar {
		return
	}

	if typeDef, ok := schema.Types[typeName]; ok && typeDef != nil {
		schemas[typeName] = openAPITypeSchema(typeDef, schemas, schema, dependencies, scalarExamples, scalarDescriptions, scalarMap, processed)
		return
	}

	if enumDef, ok := schema.Enums[typeName]; ok {
		schemas[typeName] = openAPIEnumSchema(enumDef)
		return
	}
	if unionDef, ok := schema.Unions[typeName]; ok && unionDef != nil {
		schemas[typeName] = openAPIUnionSchema(
			unionDef,
			schemas,
			schema,
			dependencies,
			scalarExamples,
			scalarDescriptions,
			scalarMap,
			processed,
		)
		return
	}
	for _, depSchema := range dependencies {
		if depSchema == nil {
			continue
		}
		if typeDef, ok := depSchema.Types[typeName]; ok && typeDef != nil {
			schemas[typeName] = openAPITypeSchema(typeDef, schemas, depSchema, dependencies, scalarExamples, scalarDescriptions, scalarMap, processed)
			return
		}
		if enumDef, ok := depSchema.Enums[typeName]; ok {
			schemas[typeName] = openAPIEnumSchema(enumDef)
			return
		}
		if unionDef, ok := depSchema.Unions[typeName]; ok && unionDef != nil {
			schemas[typeName] = openAPIUnionSchema(
				unionDef,
				schemas,
				depSchema,
				dependencies,
				scalarExamples,
				scalarDescriptions,
				scalarMap,
				processed,
			)
			return
		}
	}
}

func openAPIUnionSchema(
	unionDef *ir.UnionDef,
	schemas map[string]interface{},
	schema *ir.Schema,
	dependencies map[string]*ir.Schema,
	scalarExamples map[string]string,
	scalarDescriptions map[string]string,
	scalarMap map[string]string,
	processed map[string]bool,
) map[string]interface{} {
	oneOf := make([]map[string]interface{}, 0, len(unionDef.Types))
	for _, memberType := range unionDef.Types {
		oneOf = append(oneOf, map[string]interface{}{
			"$ref": fmt.Sprintf("#/components/schemas/%s", memberType),
		})
		addSchemaFromIR(
			schemas,
			memberType,
			schema,
			dependencies,
			scalarExamples,
			scalarDescriptions,
			scalarMap,
			processed,
		)
	}

	result := map[string]interface{}{"oneOf": oneOf}
	if doc := codegen.DocText(unionDef.Description, unionDef.Comment); doc != "" {
		result["description"] = doc
	}
	for _, union := range codegen.ExtractUnions(schema) {
		if union.Name != unionDef.Name || union.Discriminator == "" {
			continue
		}
		discriminator := map[string]interface{}{"propertyName": union.Discriminator}
		mapping := make(map[string]string, len(union.Members))
		for _, member := range union.Members {
			if member.DiscriminatorValue != "" {
				mapping[member.DiscriminatorValue] = fmt.Sprintf("#/components/schemas/%s", member.Name)
			}
		}
		if len(mapping) > 0 {
			discriminator["mapping"] = mapping
		}
		result["discriminator"] = discriminator
		break
	}
	return result
}

func openAPITypeSchema(typeDef *ir.TypeDef, schemas map[string]interface{}, schema *ir.Schema, dependencies map[string]*ir.Schema, scalarExamples, scalarDescriptions, scalarMap map[string]string, processed map[string]bool) map[string]interface{} {
	properties := make(map[string]interface{})
	required := []string{}

	for _, field := range typeDef.Fields {
		fieldSchema := typeRefToOpenAPISchema(field.TypeRef, !field.Required, scalarExamples, scalarDescriptions, scalarMap, schema, dependencies)

		if doc := codegen.DocText(field.Description, field.Comment); doc != "" {
			fieldSchema["description"] = doc
		}

		properties[field.Name] = fieldSchema

		if field.Required {
			required = append(required, field.Name)
		}

		addSchemaFromIR(schemas, field.TypeRef.Name, schema, dependencies, scalarExamples, scalarDescriptions, scalarMap, processed)
	}

	schemaObj := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schemaObj["required"] = required
	}
	if doc := codegen.DocText(typeDef.Description, typeDef.Comment); doc != "" {
		schemaObj["description"] = doc
	}

	return schemaObj
}

func openAPIEnumSchema(enumDef *ir.EnumDef) map[string]interface{} {
	enumValues := []string{}
	for _, value := range enumDef.Values {
		serialized := value.SerializedAs
		if serialized == "" {
			serialized = value.Name
		}
		enumValues = append(enumValues, serialized)
	}

	schemaObj := map[string]interface{}{
		"type": "string",
		"enum": enumValues,
	}
	if doc := codegen.DocText(enumDef.Description, enumDef.Comment); doc != "" {
		schemaObj["description"] = doc
	}
	return schemaObj
}

// typeRefToOpenAPISchema converts a TypeRef to an OpenAPI schema, wrapping
// array and map containers around the base type schema. nullable applies to
// the outermost container: the IR carries nullability at field level, not
// element level.
func typeRefToOpenAPISchema(typeRef ir.TypeRef, nullable bool, scalarExamples, scalarDescriptions, scalarMap map[string]string, schema *ir.Schema, dependencies map[string]*ir.Schema) map[string]interface{} {
	valueSchema := typeToOpenAPISchema(typeRef.Name, scalarExamples, scalarDescriptions, scalarMap)

	if typeRef.IsArray {
		valueSchema = map[string]interface{}{
			"type":  "array",
			"items": valueSchema,
		}
	}

	if typeRef.IsMap {
		valueSchema = map[string]interface{}{
			"type":                 "object",
			"additionalProperties": valueSchema,
		}
	}

	if nullable {
		valueSchema = nullableOpenAPISchema(valueSchema, openAPIRefType(typeRef.Name, schema, dependencies))
	}
	return valueSchema
}

// nullableOpenAPISchema returns a schema that accepts JSON null in addition
// to what schema accepts. An inline schema takes the OpenAPI 3.0 nullable
// flag, which 3.0.3 defines as adding null to the type declared in the same
// schema object. That rules out a bare $ref (3.0 ignores its sibling keys)
// and an allOf wrapper (no type, so the flag is a no-op, and allOf still
// requires the referenced schema, which rejects null). The 3.0 encoding that
// keeps the referenced constraints and admits null is a oneOf whose second
// branch admits nothing but null: type plus nullable make null legal and
// enum [null] excludes everything else. refType is the referenced
// component's JSON type, so the null branch is typed like the value it
// stands in for.
func nullableOpenAPISchema(schema map[string]interface{}, refType string) map[string]interface{} {
	if _, isRef := schema["$ref"]; !isRef {
		schema["nullable"] = true
		return schema
	}
	return map[string]interface{}{
		"oneOf": []interface{}{
			schema,
			map[string]interface{}{
				"type":     refType,
				"nullable": true,
				"enum":     []interface{}{nil},
			},
		},
	}
}

// openAPIRefType is the JSON type of the component typeName references:
// string for enums, object for types and unions.
func openAPIRefType(typeName string, schema *ir.Schema, dependencies map[string]*ir.Schema) string {
	if _, ok := schema.Enums[typeName]; ok {
		return "string"
	}
	for _, depSchema := range dependencies {
		if depSchema == nil {
			continue
		}
		if _, ok := depSchema.Enums[typeName]; ok {
			return "string"
		}
	}
	return "object"
}

// typeToOpenAPISchema converts a schema type name to an OpenAPI schema.
// Scalars and primitives become inline schemas; everything else becomes a
// component reference.
func typeToOpenAPISchema(typeName string, scalarExamples, scalarDescriptions, scalarMap map[string]string) map[string]interface{} {
	primitive, isScalar := scalarMap[typeName]
	if !isScalar {
		return map[string]interface{}{
			"$ref": fmt.Sprintf("#/components/schemas/%s", typeName),
		}
	}

	schema := make(map[string]interface{})
	switch primitive {
	case "string":
		schema["type"] = "string"
	case "number":
		schema["type"] = "number"
		schema["format"] = "double"
	case "integer":
		schema["type"] = "integer"
		schema["format"] = "int64"
	case "boolean":
		schema["type"] = "boolean"
	case "object":
		schema["type"] = "object"
	default:
		schema["type"] = "string"
	}

	if example, ok := scalarExamples[typeName]; ok {
		if schemaType, ok := schema["type"].(string); ok && schemaType == "object" {
			var parsedExample interface{}
			if err := json.Unmarshal([]byte(example), &parsedExample); err == nil {
				schema["example"] = parsedExample
			} else {
				schema["example"] = example
			}
		} else {
			schema["example"] = example
		}
	}

	if desc, ok := scalarDescriptions[typeName]; ok {
		schema["description"] = desc
	}

	return schema
}
