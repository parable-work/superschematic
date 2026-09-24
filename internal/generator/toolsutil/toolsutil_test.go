package toolsutil

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

func byName(name, _ string) string { return name }

func intPtr(value int) *int { return &value }

func TestParametersSchemaPreservesNestedObjectsAndListBounds(t *testing.T) {
	listMin, listMax := 5, 5
	minLength, maxLength := 1, 80
	fields := map[string][]apigen.Param{
		"CreateProfileInput": {
			{Name: "name", Type: "Name", Required: true},
			{Name: "labels", Type: "ProfileLabels", Required: true},
			{Name: "rules", Type: "ProfileRuleInput", Required: true, IsArray: true, ValidateListMin: &listMin, ValidateListMax: &listMax},
		},
		"ProfileLabels": {
			{Name: "active", Type: "string", Required: true, ValidateMinLength: &minLength, ValidateMaxLength: &maxLength},
			{Name: "done", Type: "string", Required: true, ValidateMinLength: &minLength, ValidateMaxLength: &maxLength},
		},
		"ProfileRuleInput": {
			{Name: "prompt", Type: "string", Required: true},
			{Name: "settings", Type: "RuleSetting", Required: true, IsArray: true},
		},
		"LevelSetting": {
			{Name: "kind", Type: "string", Required: true},
			{Name: "level", Type: "string", Required: true},
		},
		"SpeedSetting": {
			{Name: "kind", Type: "string", Required: true},
			{Name: "multiplier", Type: "number", Required: true},
		},
	}
	unions := map[string]apigen.ToolUnionInfo{
		"RuleSetting": {
			Description:   "A rule setting",
			Discriminator: "kind",
			Members: []apigen.ToolUnionMemberInfo{
				{Name: "LevelSetting", DiscriminatorValue: "level"},
				{Name: "SpeedSetting", DiscriminatorValue: "speed"},
			},
		},
	}
	nameMin, nameMax := 1, 80
	schema := BuildParametersSchema(nil, nil, true, "CreateProfileInput", fields, unions, nil, false,
		map[string]apigen.ScalarJSONSchemaInfo{
			"string": {Type: "string"},
			"number": {Type: "number"},
			"Name":   {Type: "string", MinLength: &nameMin, MaxLength: &nameMax},
		},
		byName, apigen.DefaultToolKeys(),
	)
	name := schema.Properties["name"]
	if name.MinLength == nil || *name.MinLength != 1 || name.MaxLength == nil || *name.MaxLength != 80 {
		t.Fatalf("scalar constraints were overwritten: %#v", name)
	}
	labels := schema.Properties["labels"]
	if labels.Type != "object" || labels.Properties["active"].MinLength == nil || *labels.Properties["active"].MinLength != 1 {
		t.Fatalf("labels schema = %#v", labels)
	}
	rules := schema.Properties["rules"]
	if rules.MinItems == nil || *rules.MinItems != 5 || rules.MaxItems == nil || *rules.MaxItems != 5 {
		t.Fatalf("rule bounds = %#v", rules)
	}
	if rules.Items == nil || rules.Items.Type != "object" || rules.Items.Properties["prompt"].Type != "string" {
		t.Fatalf("rule items = %#v", rules.Items)
	}
	settings := rules.Items.Properties["settings"]
	if settings.Items == nil || settings.Items.Type != "object" || len(settings.Items.OneOf) != 2 {
		t.Fatalf("setting union = %#v", settings.Items)
	}
	if got := settings.Items.OneOf[0].Properties["kind"].Enum; len(got) != 1 || got[0] != "level" {
		t.Fatalf("level discriminator = %#v", got)
	}
	if settings.Items.OneOf[0].Properties["level"].Type != "string" ||
		settings.Items.OneOf[1].Properties["multiplier"].Type != "number" {
		t.Fatalf("setting members = %#v", settings.Items.OneOf)
	}
}

func TestParametersSchemaIncludesRequiredAndArrayQueryArguments(t *testing.T) {
	min, max := 1.0, 100.0
	query := []ToolQueryArg{
		{
			Name: "search", TSName: "search", Type: "Identity.Name", Required: true,
			ValidateMinLength: intPtr(1), ValidateMaxLength: intPtr(120),
		},
		{
			Name: "stage", TSName: "stage", Type: "StageEnum", IsArray: true,
			ValidateListMin: intPtr(0), ValidateListMax: intPtr(10),
		},
		{
			Name: "limit", TSName: "limit", Type: "Count",
			ValidateMin: &min, ValidateMax: &max,
		},
	}
	schema := BuildParametersSchema(nil, query, false, "", nil, nil, nil, false,
		map[string]apigen.ScalarJSONSchemaInfo{
			"Identity.Name": {Type: "string"},
			"Count":         {Type: "integer"},
		},
		func(name, tsName string) string {
			if tsName != "" {
				return tsName
			}
			return name
		},
		apigen.DefaultToolKeys(),
	)
	if len(schema.Required) != 1 || schema.Required[0] != "search" {
		t.Fatalf("required query fields = %#v", schema.Required)
	}
	if search := schema.Properties["search"]; search.Type != "string" ||
		search.MinLength == nil || *search.MinLength != 1 ||
		search.MaxLength == nil || *search.MaxLength != 120 {
		t.Fatalf("search query schema = %#v", search)
	}
	stage := schema.Properties["stage"]
	if stage.Type != "array" || stage.Items == nil || stage.Items.Type != "string" ||
		stage.MinItems == nil || *stage.MinItems != 0 ||
		stage.MaxItems == nil || *stage.MaxItems != 10 {
		t.Fatalf("array query schema = %#v", stage)
	}
	limit := schema.Properties["limit"]
	if limit.Type != "integer" || limit.Minimum == nil || *limit.Minimum != 1 ||
		limit.Maximum == nil || *limit.Maximum != 100 {
		t.Fatalf("bounded query schema = %#v", limit)
	}
}

func TestArbitraryJSONScalarPreservesAnyTypeSentinel(t *testing.T) {
	property := ScalarToJSONSchemaProperty("Generic.JSON", map[string]apigen.ScalarJSONSchemaInfo{
		"Generic.JSON": {Type: "any", Description: "Any valid JSON root"},
	})
	encoded, err := json.Marshal(property)
	if err != nil {
		t.Fatalf("marshal property: %v", err)
	}
	if !strings.Contains(string(encoded), `"type":"any"`) || !strings.Contains(string(encoded), `"description":"Any valid JSON root"`) {
		t.Fatalf("property = %s", encoded)
	}
	literal := JSONSchemaPropertyLiteral(property)
	if !strings.Contains(literal, `"type":["object","array","string","number","boolean","null"]`) {
		t.Fatalf("any sentinel was not expanded: %s", literal)
	}
}

func TestToolSchemaCarriesScalarProvenanceAndClosesOnlyStructuralObjects(t *testing.T) {
	fields := map[string][]apigen.Param{
		"Input": {
			{Name: "target", Type: "Identity.UUID", Required: true},
			{Name: "payload", Type: "Generic.JSON", Required: true},
			{Name: "settings", Type: "Settings", Required: true},
		},
		"Settings": {{Name: "label", Type: "string", Required: true}},
	}
	schema := BuildParametersSchema(nil, nil, true, "Input", fields, nil, nil, false,
		map[string]apigen.ScalarJSONSchemaInfo{
			"Identity.UUID": {CanonicalName: "Identity.UUID", Type: "string"},
			"Generic.JSON":  {CanonicalName: "Generic.JSON", Type: "any"},
			"string":        {Type: "string"},
		}, byName, apigen.DefaultToolKeys())
	if schema.AdditionalProperties || len(schema.Vendor) != 0 {
		t.Fatalf("root is not a closed object without vendor keys: %#v", schema)
	}
	if got := schema.Properties["target"]; got.CanonicalScalar != "Identity.UUID" || got.Type != "string" {
		t.Fatalf("UUID lost its scalar: %#v", got)
	}
	if got := schema.Properties["payload"]; got.CanonicalScalar != "Generic.JSON" || got.AdditionalProperties != nil {
		t.Fatalf("arbitrary JSON was closed as a structural object: %#v", got)
	}
	if got := schema.Properties["settings"]; !isClosedObject(got) || got.Properties["label"].CanonicalScalar != "" {
		t.Fatalf("structural fields lost their exact shape: %#v", got)
	}
	literal := JSONSchemaPropertyLiteral(schema.Properties["target"])
	if !strings.Contains(literal, `"x-superschematic-scalar":"Identity.UUID"`) {
		t.Fatalf("the scalar name was dropped on the wire: %s", literal)
	}
}

// TestToolKeysRenameAndAddVendorKeys: the scalar key comes from ToolKeys,
// nested properties included, an empty key leaves the name out, and the
// Parameters keys are encoded first, in order, where the digest hashes
// them.
func TestToolKeysRenameAndAddVendorKeys(t *testing.T) {
	fields := map[string][]apigen.Param{
		"Input": {
			{Name: "target", Type: "Identity.UUID", Required: true},
			{Name: "ids", Type: "Identity.UUID", Required: true, IsArray: true},
			{Name: "byName", Type: "Identity.UUID", Required: true, IsMap: true},
		},
	}
	scalars := map[string]apigen.ScalarJSONSchemaInfo{"Identity.UUID": {CanonicalName: "Identity.UUID", Type: "string"}}
	keys := apigen.ToolKeys{
		Scalar:     "x-acme-scalar",
		Parameters: []apigen.ToolKeyValue{{Key: "x-acme-version", Value: 2}, {Key: "x-acme-owner", Value: "shop"}},
	}
	schema := BuildParametersSchema(nil, nil, true, "Input", fields, nil, nil, false, scalars, byName, keys)
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(encoded), `{"x-acme-version":2,"x-acme-owner":"shop","additionalProperties":false,"type":"object","properties":{`) {
		t.Fatalf("vendor keys are not first: %s", encoded)
	}
	for _, want := range []string{
		`"target":{"x-acme-scalar":"Identity.UUID","type":"string"}`,
		`"items":{"x-acme-scalar":"Identity.UUID","type":"string"}`,
		`"additionalProperties":{"x-acme-scalar":"Identity.UUID","type":"string"}`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("encoding lacks %s:\n%s", want, encoded)
		}
	}
	if strings.Contains(string(encoded), "x-superschematic-scalar") {
		t.Fatalf("the core scalar key survived a rename: %s", encoded)
	}

	unkeyed := BuildParametersSchema(nil, nil, true, "Input", fields, nil, nil, false, scalars, byName, apigen.ToolKeys{})
	if encoded, _ := json.Marshal(unkeyed); strings.Contains(string(encoded), `:"Identity.UUID"`) {
		t.Fatalf("an empty scalar key still wrote the name: %s", encoded)
	}
}

func TestParametersSchemaRendersTypedMapsAsObjects(t *testing.T) {
	listMin, listMax := 1, 3
	fields := map[string][]apigen.Param{
		"Input": {
			{Name: "panes", Type: "string", Required: true, IsMap: true},
			{Name: "refs", Type: "Ref", Required: true, IsMap: true},
			{Name: "tagsByPane", Type: "Identity.Name", Required: true, IsArray: true, IsMap: true, ValidateListMin: &listMin, ValidateListMax: &listMax},
		},
		"Ref": {
			{Name: "packageKey", Type: "Identity.Name", Required: true},
			{Name: "itemKey", Type: "Identity.Name", Required: true},
		},
	}
	schema := BuildParametersSchema(nil, nil, true, "Input", fields, nil, nil, false,
		map[string]apigen.ScalarJSONSchemaInfo{
			"string":        {Type: "string"},
			"Identity.Name": {Type: "string", CanonicalName: "Identity.Name"},
		},
		byName, apigen.DefaultToolKeys(),
	)

	panes := schema.Properties["panes"]
	if panes.Type != "object" || panes.AdditionalProperties == nil ||
		panes.AdditionalProperties.Schema == nil ||
		panes.AdditionalProperties.Schema.Type != "string" {
		t.Fatalf("string map schema = %#v", panes)
	}
	refs := schema.Properties["refs"]
	if refs.Type != "object" || refs.AdditionalProperties == nil ||
		refs.AdditionalProperties.Schema == nil ||
		refs.AdditionalProperties.Schema.Type != "object" ||
		!isClosedObject(*refs.AdditionalProperties.Schema) ||
		refs.AdditionalProperties.Schema.Properties["itemKey"].Type != "string" {
		t.Fatalf("object map schema = %#v", refs)
	}
	tags := schema.Properties["tagsByPane"]
	if tags.Type != "object" || tags.AdditionalProperties == nil ||
		tags.AdditionalProperties.Schema == nil ||
		tags.AdditionalProperties.Schema.Type != "array" ||
		tags.AdditionalProperties.Schema.MinItems == nil ||
		*tags.AdditionalProperties.Schema.MinItems != 1 ||
		tags.AdditionalProperties.Schema.Items == nil ||
		tags.AdditionalProperties.Schema.Items.CanonicalScalar != "Identity.Name" {
		t.Fatalf("array-valued map schema = %#v", tags)
	}

	var literal map[string]any
	if err := json.Unmarshal([]byte(JSONSchemaPropertyLiteral(panes)), &literal); err != nil {
		t.Fatal(err)
	}
	if additional := literal["additionalProperties"].(map[string]any); additional["type"] != "string" {
		t.Fatalf("string map literal additionalProperties = %#v", additional)
	}
}

func isClosedObject(property JSONSchemaProperty) bool {
	return property.AdditionalProperties != nil &&
		property.AdditionalProperties.Bool != nil &&
		!*property.AdditionalProperties.Bool
}

func TestNullableBodyFieldsPreserveContainerAndUnionContracts(t *testing.T) {
	fields := map[string][]apigen.Param{
		"Input": {
			{Name: "packageKey", Type: "Identity.UUID"},
			{Name: "packages", Type: "Package", IsArray: true},
			{Name: "selection", Type: "Choice"},
			{Name: "requiredId", Type: "Identity.UUID", Required: true},
			{Name: "payload", Type: "Generic.JSON", Required: true},
		},
		"Package": {{Name: "id", Type: "Identity.UUID", Required: true}},
		"ChoiceA": {{Name: "kind", Type: "string", Required: true}, {Name: "id", Type: "Identity.UUID", Required: true}},
		"ChoiceB": {{Name: "kind", Type: "string", Required: true}, {Name: "label", Type: "string", Required: true}},
	}
	schema := BuildParametersSchema(nil, []ToolQueryArg{{Name: "limit", Type: "Count"}}, true, "Input", fields,
		map[string]apigen.ToolUnionInfo{"Choice": {Discriminator: "kind", Members: []apigen.ToolUnionMemberInfo{{Name: "ChoiceA", DiscriminatorValue: "a"}, {Name: "ChoiceB", DiscriminatorValue: "b"}}}},
		[]ToolScalarArg{{Name: "reason", Type: "string"}}, false,
		map[string]apigen.ScalarJSONSchemaInfo{"Identity.UUID": {Type: "string", CanonicalName: "Identity.UUID"}, "Generic.JSON": {Type: "any", CanonicalName: "Generic.JSON"}, "Count": {Type: "integer"}, "string": {Type: "string"}},
		byName, apigen.DefaultToolKeys())
	document := make(map[string]any)
	// The same property rendering every SDK generator uses.
	for name, property := range schema.Properties {
		var value any
		if err := json.Unmarshal([]byte(JSONSchemaPropertyLiteral(property)), &value); err != nil {
			t.Fatal(err)
		}
		document[name] = value
	}
	for _, name := range []string{"packageKey", "packages", "selection", "reason"} {
		value := document[name].(map[string]any)
		kinds, ok := value["type"].([]any)
		if !ok || len(kinds) != 2 || kinds[1] != "null" {
			t.Fatalf("%s lost nullable contract: %#v", name, value)
		}
	}
	if document["limit"].(map[string]any)["type"] != "integer" {
		t.Fatal("optional query became nullable")
	}
	if document["requiredId"].(map[string]any)["type"] != "string" {
		t.Fatal("required scalar became nullable")
	}
	packages := document["packages"].(map[string]any)
	if packages["items"].(map[string]any)["type"] != "object" {
		t.Fatal("nullable collection made items nullable")
	}
	union := document["selection"].(map[string]any)["oneOf"].([]any)
	if len(union) != 3 || union[2].(map[string]any)["type"] != "null" {
		t.Fatal("nullable union lacks exactly one null branch")
	}
	for _, member := range union[:2] {
		if member.(map[string]any)["type"] != "object" {
			t.Fatal("union member became nullable")
		}
	}
}

func TestValidateReplayContract(t *testing.T) {
	fields := map[string][]apigen.Param{
		"Input": {
			{Name: "requestId", Type: "Identity.UUID", Required: true},
			{Name: "revision", Type: "number", Required: true},
			{Name: "order", Type: "OrderRef", Required: true},
			{Name: "note", Type: "string"},
			{Name: "tags", Type: "string", Required: true, IsArray: true},
		},
		"OrderRef": {
			{Name: "revision", Type: "number", Required: true},
			{Name: "id", Type: "Identity.UUID", Required: true},
		},
	}
	schema := BuildParametersSchema(nil, nil, true, "Input", fields, nil, nil, false,
		map[string]apigen.ScalarJSONSchemaInfo{
			"Identity.UUID": {CanonicalName: "Identity.UUID", Type: "string"},
			"string":        {Type: "string"},
			"number":        {Type: "number"},
		}, byName, apigen.DefaultToolKeys())

	for _, test := range []struct {
		name      string
		mode      string
		keys      []string
		revisions []string
		want      string
	}{
		{name: "no mode"},
		{name: "read only", mode: "read_only"},
		{name: "idempotent", mode: "idempotent", keys: []string{"/requestId"}},
		{name: "nested revision", mode: "compare_and_swap", revisions: []string{"/order/revision"}},
		{name: "top revision and key", mode: "compare_and_swap", keys: []string{"/order/id"}, revisions: []string{"/revision"}},
		{name: "pointers without mode", keys: []string{"/requestId"}, want: "replay pointers require a replay mode"},
		{name: "read only with pointer", mode: "read_only", keys: []string{"/requestId"}, want: "read_only operations cannot declare replay pointers"},
		{name: "unknown argument", mode: "idempotent", keys: []string{"/missing"}, want: `idempotency pointer "/missing": does not resolve at segment "missing"`},
		{name: "optional argument", mode: "idempotent", keys: []string{"/note"}, want: `segment "note" is optional`},
		{name: "through a scalar", mode: "idempotent", keys: []string{"/requestId/x"}, want: `segment "requestId" is not a concrete object`},
		{name: "key not a string", mode: "idempotent", keys: []string{"/revision"}, want: `resolves to a number argument, want a string`},
		{name: "key is an array", mode: "idempotent", keys: []string{"/tags"}, want: `resolves to a array argument, want a string`},
		{name: "revision not a number", mode: "compare_and_swap", revisions: []string{"/requestId"}, want: `expected revision pointer "/requestId" resolves to a string argument, want a number`},
		{name: "bad escape", mode: "idempotent", keys: []string{"/a~2"}, want: "invalid RFC 6901 escape"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateReplayContract(schema, test.mode, test.keys, test.revisions)
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateReplayContract() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}
