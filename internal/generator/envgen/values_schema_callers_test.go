package envgen_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/ir"
)

// valuesSchemaFile is the part of values-schema.json a values file is
// checked against.
type valuesSchemaFile struct {
	AdditionalProperties bool                         `json:"additionalProperties"`
	Properties           map[string]valuesSchemaShape `json:"properties"`
	PatternProperties    map[string]valuesSchemaShape `json:"patternProperties"`
	XSuperschematic      struct {
		EnvVars []struct {
			Name             string   `json:"name"`
			CallersOf        string   `json:"callersOf"`
			Variables        []string `json:"variables"`
			VariablePatterns []string `json:"variablePatterns"`
		} `json:"envVars"`
	} `json:"x-superschematic"`
}

type valuesSchemaShape struct {
	Type      string `json:"type"`
	Pattern   string `json:"pattern"`
	MinLength int    `json:"minLength"`
}

// checkValues checks a values file, variable by variable, against a
// values-schema.json, as JSON Schema reads its properties and
// patternProperties: each variable is a property or matches a pattern, and
// its value matches each shape that applies. It returns the problems.
func checkValues(t *testing.T, schema valuesSchemaFile, values map[string]string) []string {
	t.Helper()
	var problems []string
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range values {
			if !yield(name) {
				return
			}
		}
	}) {
		value := values[name]
		var shapes []valuesSchemaShape
		if shape, ok := schema.Properties[name]; ok {
			shapes = append(shapes, shape)
		}
		for pattern, shape := range schema.PatternProperties {
			if regexp.MustCompile(pattern).MatchString(name) {
				shapes = append(shapes, shape)
			}
		}
		if len(shapes) == 0 && !schema.AdditionalProperties {
			problems = append(problems, name+" is not declared")
		}
		for _, shape := range shapes {
			if shape.Type != "string" {
				problems = append(problems, fmt.Sprintf("%s is a %s, not a string", name, shape.Type))
			}
			if len(value) < shape.MinLength {
				problems = append(problems, fmt.Sprintf("%s is shorter than %d", name, shape.MinLength))
			}
			if shape.Pattern != "" && !regexp.MustCompile(shape.Pattern).MatchString(value) {
				problems = append(problems, fmt.Sprintf("%s is %q, which does not match %s", name, value, shape.Pattern))
			}
		}
	}
	return problems
}

// serviceAuthValues writes the values-schema.json of
// fixture-service-auth-api, whose operations have service clauses and
// which has no @envVars type, database, call or bucket, as each language's
// API build writes it: the Go build has no EnvConfig for it and writes
// CallersValues, and the TypeScript and Rust builds write their EnvConfig,
// which holds the callers field.
func serviceAuthValues(t *testing.T) map[string][]byte {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-service-auth-api"))
	if err != nil {
		t.Fatal(err)
	}
	const name = "fixture-service-auth-api"
	goOutput, err := envgen.GenerateWithOptions(schema, envgen.Options{SchemaName: name, Derived: true})
	if err != nil {
		t.Fatal(err)
	}
	if goOutput != nil {
		t.Fatalf("the Go build of %s has an EnvConfig: %+v", name, goOutput)
	}
	callersOutput, err := envgen.GenerateWithOptions(schema, envgen.Options{SchemaName: name, Derived: true, Callers: true, CORS: true})
	if err != nil {
		t.Fatal(err)
	}
	written := map[string][]byte{}
	for language, output := range map[string]*envgen.ConfigOutput{"go": envgen.CallersValues(name), "typescript and rust": callersOutput} {
		dir := t.TempDir()
		if err := envgen.WriteValuesSchema(output, dir); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "values-schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		written[language] = data
	}
	return written
}

// TestValuesSchemaListsTheCallersField: the values-schema.json of an API
// with a service clause lists its callers field, the same in every
// language: its count as a property, its issuers', keys' and callers'
// members as pattern properties, and an entry under x-superschematic that
// names the API and the variables. Regenerate with:
//
//	go test ./internal/generator/envgen -run TestValuesSchemaListsTheCallersField -update
func TestValuesSchemaListsTheCallersField(t *testing.T) {
	written := serviceAuthValues(t)
	got := written["go"]
	if string(written["typescript and rust"]) != string(got) {
		t.Errorf("the TypeScript and Rust values-schema.json differs from Go's:\n%s\nwant\n%s", written["typescript and rust"], got)
	}
	golden := filepath.Join("testdata", "golden", "service-auth", "values-schema.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(got) != string(want) {
		t.Errorf("values-schema.json differs from %s; run with -update and review the diff", golden)
	}
}

// TestTheCallersFieldsSchemaIsWhatTheReadersRead: every variable of every
// callers field the stackconfig parity vectors' readers accept is declared
// by values-schema.json and has the shape it gives, and so is every
// variable ir.DerivedVariables writes for a value with every member set. A
// member the contract lacks, which the readers refuse as no member of the
// field, is declared by none, and neither is a value the readers refuse
// for its shape. (A member under an index past its list's length is a
// name the schema declares, which the readers refuse by the count.)
func TestTheCallersFieldsSchemaIsWhatTheReadersRead(t *testing.T) {
	const field = "SHOP_API_CALLERS"
	dir := t.TempDir()
	if err := envgen.WriteValuesSchema(envgen.CallersValues("shop-api"), dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "values-schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema valuesSchemaFile
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.XSuperschematic.EnvVars) != 1 || schema.XSuperschematic.EnvVars[0].Name != field || schema.XSuperschematic.EnvVars[0].CallersOf != "shop-api" {
		t.Fatalf("x-superschematic.envVars = %+v, want shop-api's callers field", schema.XSuperschematic.EnvVars)
	}

	parity, err := os.ReadFile(filepath.Join("..", "..", "..", "runtime", "http", "testdata", "stackconfig_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Vectors []struct {
			Name      string             `json:"name"`
			Reader    string             `json:"reader"`
			Field     string             `json:"field"`
			Variables map[string]string  `json:"variables"`
			Edits     map[string]*string `json:"edits"`
			Want      json.RawMessage    `json:"want"`
			Refusals  []string           `json:"refusals"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(parity, &vectors); err != nil {
		t.Fatal(err)
	}
	accepted, unknown := 0, 0
	for _, v := range vectors.Vectors {
		if v.Reader != "callers" {
			continue
		}
		if v.Field != field {
			t.Fatalf("vector %s reads %s; the test checks %s", v.Name, v.Field, field)
		}
		env := map[string]string{}
		for name, value := range v.Variables {
			env[name] = value
		}
		for name, value := range v.Edits {
			if value == nil {
				delete(env, name)
				continue
			}
			env[name] = *value
		}
		// The readers read the field's variables alone.
		for name := range env {
			if !strings.HasPrefix(name, field+"_") {
				delete(env, name)
			}
		}
		if string(v.Want) != "null" {
			accepted++
			if problems := checkValues(t, schema, env); len(problems) > 0 {
				t.Errorf("%s: the readers accept what values-schema.json refuses:\n%s", v.Name, strings.Join(problems, "\n"))
			}
			continue
		}
		if v.Name != "callers: a member the contract lacks" {
			continue
		}
		for _, refusal := range v.Refusals {
			name, ok := strings.CutPrefix(refusal, "environment variable ")
			if !ok || !strings.Contains(name, " is no member of the callers field ") {
				continue
			}
			name, _, _ = strings.Cut(name, " ")
			unknown++
			if problems := checkValues(t, schema, map[string]string{name: env[name]}); len(problems) == 0 {
				t.Errorf("%s: values-schema.json declares %s, which the readers refuse as no member", v.Name, name)
			}
		}
	}
	if accepted == 0 || unknown == 0 {
		t.Fatalf("the parity vectors have %d accepted callers fields and %d refusals of no member; want some of each", accepted, unknown)
	}

	full := ir.ServiceAuth{Issuers: []*ir.ServiceAuthIssuer{
		{
			Issuer: "orders", IssuerAliases: []string{"orders-2", "orders-3"}, Audience: "shop-api",
			Algorithms: []string{ir.AlgorithmEdDSA, ir.AlgorithmES256}, SubjectClaim: "email", MaxLifetimeSeconds: 300,
			Keys:    []ir.ServiceAuthKey{{JWK: `{"kty":"OKP","crv":"Ed25519","x":"AA","kid":"k"}`}, {PEM: "-----BEGIN PUBLIC KEY-----\nAA\n-----END PUBLIC KEY-----\n"}},
			Callers: []ir.ServiceAuthCaller{{Subject: "orders", Deployable: "orders", Serves: []string{"shop-orders"}}, {Subject: "jobs", Deployable: "jobs", Serves: []string{"shop-orders", "shop-jobs"}}},
		},
		{Issuer: "https://accounts.google.com", Audience: "shop-api", Algorithms: []string{ir.AlgorithmRS256}, JWKSURL: "https://www.googleapis.com/oauth2/v3/certs", Callers: []ir.ServiceAuthCaller{{Subject: "a@b", Deployable: "billing", Serves: []string{"billing"}}}},
	}}
	for i := range 11 {
		full.Issuers[1].Callers = append(full.Issuers[1].Callers, ir.ServiceAuthCaller{Subject: fmt.Sprintf("s%d", i), Deployable: "d", Serves: []string{"x"}})
	}
	variables, err := ir.DerivedVariables(field, full)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, v := range variables {
		env[v.Name] = fmt.Sprint(v.Value)
	}
	if problems := checkValues(t, schema, env); len(problems) > 0 {
		t.Errorf("values-schema.json refuses what ir.DerivedVariables writes:\n%s", strings.Join(problems, "\n"))
	}
	for name, value := range map[string]string{
		field + "_ISSUERS_0_JWKS_BEARER_TOKEN_FILE": "/token",
		field + "_ISSUERS_01_ISSUER":                "orders",
		field + "_ISSUER":                           "orders",
		field + "_ISSUERS_0_ALGORITHMS":             "HS256",
		field + "_ISSUERS":                          "many",
		field + "_ISSUERS_0_CALLERS_0_SERVES":       "a,,b",
		field + "_ISSUERS_0_AUDIENCE":               "",
	} {
		if problems := checkValues(t, schema, map[string]string{name: value}); len(problems) == 0 {
			t.Errorf("values-schema.json takes %s=%q, which the readers refuse", name, value)
		}
	}
}
