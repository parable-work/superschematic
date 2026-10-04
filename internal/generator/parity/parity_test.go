// Package parity runs the same JSON payloads through the validators superschematic
// generates for Go, TypeScript, Python and Rust and asserts every language
// returns the same verdicts. One schema (built in a temp dir, JSON-authored so the
// real loader runs), one vector table, one expected-outcome column: a
// validator semantic that drifts in any language fails here. This is the
// generator-layer counterpart of superscalar's shared conformance corpus
// (the scalar package/conformance/), and the class of bug it exists for is
// Regression: Go decode turned absent optional lists into empty ones, so
// only Go fired listMin on omitted fields. Reverting that fix makes the go
// subtest fail on every vector that omits optList.
//
// The same schema and vectors also drive the three schema runtimes. The
// harness writes them, with the loaded IR, to
// runtime/schema/testdata/validation_parity.json (TestRuntimeParityCorpus;
// -update rewrites it), and the Go, TypeScript and Python runtime suites
// each assert the expected column against it. A generated validator and a
// runtime that disagree on a payload therefore fail against the same row.
//
// A divergence a PR cannot fix on the spot gets pinned in knownDivergences
// with the reason and the follow-up that removes it, mirroring the
// unresolved flag in the scalar corpus. Fixing the language later fails the
// stale pin, so the table shrinks in the same change; a new divergence fails
// against the expected column immediately. Grow coverage by adding fields to
// the matrix schema and rows to the vector table.
//
// Where a typed decoder refuses a payload before the generated validator
// can see it, the vector lists that language in decodeRejects and the
// driver asserts the refusal instead. That is expected, not a divergence
// to close: in Go and Python the typed decoder is the first check, and the
// payload never becomes a value to validate. Go's json.Unmarshal refuses a
// non-list inner value and a value or element of the wrong JSON type (a
// number where a string belongs), and the generated UnmarshalJSON refuses
// a null list element, which json.Unmarshal alone would decode to the
// element type's zero value; pydantic's strict parse refuses a value or
// element of the wrong type, a bad enum element and a nested object
// element with a bad field. The Rust validators run on the JSON value
// before serde decodes it, as the TypeScript ones do, so Rust is listed
// only where its validator accepts a payload that serde then refuses.
//
// Verdict comparison is about semantics, not field-name idiom: the Python
// driver maps validate_all's snake_case attribute keys back to wire names
// before reporting. Nested object errors are flattened to dotted paths
// (pointGrid[0][1].shade).
package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// verdicts is one language's normalized output: vector name -> field name ->
// sorted validator names that failed. A passing vector maps to an empty map.
type verdicts map[string]map[string][]string

const schemaConfigJSON = `{
  "name": "parity-fixture",
  "kind": "General",
  "outputs": {
    "types": {
      "typescript": { "enabled": true },
      "python": { "enabled": true }
    }
  }
}`

// The validation matrix: every combination of required/optional x scalar/list
// the generated validators gate differently, with one constraint per axis.
// The url, email, name, names, rank, ranks and share fields are scalars
// (D14): every validator reports a failing scalar value once, by the name of
// the scalar's rule it breaks. A malformed value is "pattern", whether the
// scalar's pattern or the scalar core finds it (Contact.Email also has a
// custom validator in the core); a value out of the scalar's length bounds
// (Network.Url, Identity.Name) is "minLength" or "maxLength", and one out
// of its range (Ordering.Rank, an integer; Generic.Probability, a float) is
// "min" or "max".
//
// The builtin primitive fields (reqStr, optStr, reqList, optList, optNum,
// optNums, optBool, optBools) and the scalar fields also cover a value of
// the wrong JSON type (D14, amended): it is one "type" error at its path,
// required or optional, and the field's length, pattern and range rules do
// not check it. So do shade (an enum), point (an object), when and whens
// (Temporal.DateTime, a string scalar whose generated TypeScript type is a
// Date), and every list field given a value that is not a list.
//
// String lengths count Unicode code points in every validator (D14,
// amended): the length vectors hold astral characters (two UTF-16 units,
// four UTF-8 bytes) and multi-byte BMP characters (one UTF-16 unit, two
// UTF-8 bytes).
//
// ListMatrix holds the list rules for T[] and T[][] (D12): a required list
// means present, not non-empty; listMin and listMax bound the outer list; a
// field's own constraints apply to every element and every innermost
// element; an inner list is never null ("required" at field[i]) and any
// other non-list inner value is "type" at field[i]; a list element is never
// null ("required" at field[i] or field[i][j]). Its enum, scalar, object
// and builtin element types cover the element checks at both depths; flags,
// payloads and reqPayloads add boolean and Generic.JSON elements to T[], in
// an optional and a required list.
//
// JsonMatrix holds Generic.JSON, whose value is any JSON value but null: an
// object, an array, a string (JSON text or not), a number or a boolean, with
// no type or pattern check on it. A null or missing required one is
// "required", a null optional one passes as an absent one does (the
// decoders keep the two apart, which no verdict shows), and a null element
// of Generic.JSON[] or Generic.JSON[][] is "required" at its index (D12).
//
// StructuredMatrix holds Generic.StringMap and Embedding.Vector, whose value
// is a JSON object and a JSON array (D14, amended): the object or array, or
// a string holding its JSON text, is a value; any other JSON type is
// "type"; null, or an empty string, is a missing value. What the object or
// array holds is the scalar core's check, whose name differs by validator,
// so no vector breaks it.
//
// ScalarRuleMatrix holds scalar fields with rules of their own
// (Validate<Generic.Int64, { min: 0 }>): the scalar's validation runs
// first, and the field's minLength, maxLength, pattern, min and max follow
// it, on a single value, every T[] element and every innermost T[][]
// element. A value either rule set rejects fails with that rule's name; a
// rule checks only a value of its own JSON type, so a mistyped value is one
// "type" error.
//
// PatternMatrix holds field patterns that match one character at a time
// (".", "\W", a negated class with a count), on a builtin string, a list,
// a list of lists and a string scalar. A pattern matches code points in
// every validator (D14, amended): an astral character is one character, as
// in Go's regexp and Python's re, and TypeScript compiles every pattern
// with the "u" flag. Without it "." and "[^a-z]" match one UTF-16 unit,
// half an astral character.
const parityMatrixSchemaJSON = `{
  "scalars": {
    "Generic.Int64": {
      "name": "Generic.Int64",
      "languagePrimitive": "number"
    },
    "Network.Url": {
      "name": "Network.Url",
      "languagePrimitive": "string"
    },
    "Contact.Email": {
      "name": "Contact.Email",
      "languagePrimitive": "string"
    },
    "Identity.Name": {
      "name": "Identity.Name",
      "languagePrimitive": "string"
    },
    "Ordering.Rank": {
      "name": "Ordering.Rank",
      "languagePrimitive": "number"
    },
    "Generic.Probability": {
      "name": "Generic.Probability",
      "languagePrimitive": "number"
    },
    "Generic.JSON": {
      "name": "Generic.JSON",
      "languagePrimitive": "object"
    },
    "Generic.StringMap": {
      "name": "Generic.StringMap",
      "languagePrimitive": "string"
    },
    "Embedding.Vector": {
      "name": "Embedding.Vector",
      "languagePrimitive": "string"
    },
    "Temporal.DateTime": {
      "name": "Temporal.DateTime",
      "languagePrimitive": "string"
    }
  },
  "enums": {
    "ParityShade": {
      "name": "ParityShade",
      "values": [
        { "name": "Light", "serializedAs": "light" },
        { "name": "Dark", "serializedAs": "dark" }
      ]
    }
  },
  "types": {
    "ParityMatrix": {
      "name": "ParityMatrix",
      "role": "EmbeddedStruct",
      "jsonField": true,
      "fields": [
        {
          "name": "url",
          "typeRef": { "name": "Network.Url" }
        },
        {
          "name": "email",
          "typeRef": { "name": "Contact.Email" }
        },
        {
          "name": "name",
          "typeRef": { "name": "Identity.Name" }
        },
        {
          "name": "names",
          "typeRef": { "name": "Identity.Name", "isArray": true }
        },
        {
          "name": "rank",
          "typeRef": { "name": "Ordering.Rank" }
        },
        {
          "name": "ranks",
          "typeRef": { "name": "Ordering.Rank", "isArray": true }
        },
        {
          "name": "share",
          "typeRef": { "name": "Generic.Probability" }
        },
        {
          "name": "reqStr",
          "typeRef": { "name": "string" },
          "required": true,
          "validateMaxLength": 5
        },
        {
          "name": "optStr",
          "typeRef": { "name": "string" },
          "validateMaxLength": 5
        },
        {
          "name": "reqList",
          "typeRef": { "name": "string", "isArray": true },
          "required": true,
          "validateMaxLength": 5,
          "validateListMin": 1,
          "validateListMax": 3
        },
        {
          "name": "optList",
          "typeRef": { "name": "string", "isArray": true },
          "validateMaxLength": 5,
          "validateListMin": 1,
          "validateListMax": 3
        },
        {
          "name": "reqScalarList",
          "typeRef": { "name": "Network.Url", "isArray": true },
          "required": true
        },
        {
          "name": "optNum",
          "typeRef": { "name": "number" },
          "validateMin": 1,
          "validateMax": 10
        },
        {
          "name": "optNums",
          "typeRef": { "name": "number", "isArray": true },
          "validateMin": 1,
          "validateMax": 10
        },
        {
          "name": "optBool",
          "typeRef": { "name": "boolean" }
        },
        {
          "name": "optBools",
          "typeRef": { "name": "boolean", "isArray": true }
        },
        {
          "name": "shade",
          "typeRef": { "name": "ParityShade" }
        },
        {
          "name": "point",
          "typeRef": { "name": "ParityPoint" }
        },
        {
          "name": "when",
          "typeRef": { "name": "Temporal.DateTime" }
        },
        {
          "name": "whens",
          "typeRef": { "name": "Temporal.DateTime", "isArray": true }
        }
      ]
    },
    "ParityPoint": {
      "name": "ParityPoint",
      "role": "EmbeddedStruct",
      "jsonField": true,
      "fields": [
        {
          "name": "shade",
          "typeRef": { "name": "ParityShade" },
          "required": true
        }
      ]
    },
    "ListMatrix": {
      "name": "ListMatrix",
      "role": "EmbeddedStruct",
      "jsonField": true,
      "fields": [
        {
          "name": "reqGrid",
          "typeRef": { "name": "string", "isArray": true, "isArrayOfArrays": true },
          "required": true,
          "validateMaxLength": 5
        },
        {
          "name": "optGrid",
          "typeRef": { "name": "string", "isArray": true, "isArrayOfArrays": true },
          "validateMaxLength": 5,
          "validateListMin": 1,
          "validateListMax": 2
        },
        {
          "name": "numGrid",
          "typeRef": { "name": "number", "isArray": true, "isArrayOfArrays": true },
          "validateMin": 1,
          "validateMax": 10
        },
        {
          "name": "reqUrlGrid",
          "typeRef": { "name": "Network.Url", "isArray": true, "isArrayOfArrays": true },
          "required": true
        },
        {
          "name": "reqShadeGrid",
          "typeRef": { "name": "ParityShade", "isArray": true, "isArrayOfArrays": true },
          "required": true
        },
        {
          "name": "pointGrid",
          "typeRef": { "name": "ParityPoint", "isArray": true, "isArrayOfArrays": true }
        },
        {
          "name": "reqShadeList",
          "typeRef": { "name": "ParityShade", "isArray": true },
          "required": true
        },
        {
          "name": "pointList",
          "typeRef": { "name": "ParityPoint", "isArray": true }
        },
        {
          "name": "boolGrid",
          "typeRef": { "name": "boolean", "isArray": true, "isArrayOfArrays": true }
        },
        {
          "name": "rankGrid",
          "typeRef": { "name": "Ordering.Rank", "isArray": true, "isArrayOfArrays": true }
        },
        {
          "name": "flags",
          "typeRef": { "name": "boolean", "isArray": true }
        },
        {
          "name": "payloads",
          "typeRef": { "name": "Generic.JSON", "isArray": true }
        },
        {
          "name": "reqPayloads",
          "typeRef": { "name": "Generic.JSON", "isArray": true },
          "required": true
        }
      ]
    },
    "JsonMatrix": {
      "name": "JsonMatrix",
      "role": "EmbeddedStruct",
      "jsonField": true,
      "fields": [
        {
          "name": "reqJson",
          "typeRef": { "name": "Generic.JSON" },
          "required": true
        },
        {
          "name": "optJson",
          "typeRef": { "name": "Generic.JSON" }
        },
        {
          "name": "jsonList",
          "typeRef": { "name": "Generic.JSON", "isArray": true }
        },
        {
          "name": "jsonGrid",
          "typeRef": { "name": "Generic.JSON", "isArray": true, "isArrayOfArrays": true }
        }
      ]
    },
    "StructuredMatrix": {
      "name": "StructuredMatrix",
      "role": "EmbeddedStruct",
      "jsonField": true,
      "fields": [
        {
          "name": "reqMap",
          "typeRef": { "name": "Generic.StringMap" },
          "required": true
        },
        {
          "name": "optMap",
          "typeRef": { "name": "Generic.StringMap" }
        },
        {
          "name": "mapList",
          "typeRef": { "name": "Generic.StringMap", "isArray": true }
        },
        {
          "name": "reqVec",
          "typeRef": { "name": "Embedding.Vector" },
          "required": true
        },
        {
          "name": "optVec",
          "typeRef": { "name": "Embedding.Vector" }
        },
        {
          "name": "vecList",
          "typeRef": { "name": "Embedding.Vector", "isArray": true }
        }
      ]
    },
    "ScalarRuleMatrix": {
      "name": "ScalarRuleMatrix",
      "role": "EmbeddedStruct",
      "jsonField": true,
      "fields": [
        {
          "name": "reqAmount",
          "typeRef": { "name": "Generic.Int64" },
          "required": true,
          "validateMin": 0,
          "validateMax": 1000
        },
        {
          "name": "optRank",
          "typeRef": { "name": "Ordering.Rank" },
          "validateMax": 10
        },
        {
          "name": "optShare",
          "typeRef": { "name": "Generic.Probability" },
          "validateMax": 0.5
        },
        {
          "name": "optName",
          "typeRef": { "name": "Identity.Name" },
          "validateMaxLength": 5,
          "validatePattern": "^[a-z]+$"
        },
        {
          "name": "names",
          "typeRef": { "name": "Identity.Name", "isArray": true },
          "validateMaxLength": 5,
          "validateListMax": 2
        },
        {
          "name": "reqRanks",
          "typeRef": { "name": "Ordering.Rank", "isArray": true },
          "required": true,
          "validateMax": 10
        },
        {
          "name": "rankGrid",
          "typeRef": { "name": "Ordering.Rank", "isArray": true, "isArrayOfArrays": true },
          "validateMax": 10
        }
      ]
    },
    "PatternMatrix": {
      "name": "PatternMatrix",
      "role": "EmbeddedStruct",
      "jsonField": true,
      "fields": [
        {
          "name": "glyph",
          "typeRef": { "name": "string" },
          "validatePattern": "^.$"
        },
        {
          "name": "glyphs",
          "typeRef": { "name": "string", "isArray": true },
          "validatePattern": "^.$"
        },
        {
          "name": "glyphGrid",
          "typeRef": { "name": "string", "isArray": true, "isArrayOfArrays": true },
          "validatePattern": "^.$"
        },
        {
          "name": "spaced",
          "typeRef": { "name": "string" },
          "validatePattern": "^\\w\\W\\w$"
        },
        {
          "name": "pair",
          "typeRef": { "name": "string" },
          "validatePattern": "^[^a-z]{2}$"
        },
        {
          "name": "pairName",
          "typeRef": { "name": "Identity.Name" },
          "validatePattern": "^[^a-z]{2}$"
        },
        {
          "name": "pairNames",
          "typeRef": { "name": "Identity.Name", "isArray": true },
          "validatePattern": "^[^a-z]{2}$"
        }
      ]
    }
  }
}`

// parityVector is one shared payload with its language-agnostic expected
// verdicts. "want" is what EVERY implementation should return, the
// generated validators and the schema runtimes alike; knownDivergences
// overrides it per generated language where behavior differs today.
type parityVector struct {
	name string
	// typeName is the type the payload is validated as; empty means
	// ParityMatrix.
	typeName string
	payload  string
	want     map[string][]string
	// decodeRejects lists the generated languages ("go", "python", "rust")
	// whose typed decoder refuses the payload. Their driver reports
	// decodeRejected instead of validator verdicts: Go and Python decode
	// before the validator runs, Rust after it accepts the payload. This is
	// the expected answer for those languages, not a pin: see the package
	// comment.
	decodeRejects []string
}

// decodeRejected is the verdict a driver reports for a payload its typed
// decoder refused.
var decodeRejected = map[string][]string{"$decode": {"rejected"}}

func (v parityVector) typ() string {
	if v.typeName == "" {
		return "ParityMatrix"
	}
	return v.typeName
}

// listMatrix builds a ListMatrix payload: every required list present and
// empty unless fields sets it, plus the given fields.
func listMatrix(fields string) string {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte("{"+fields+"}"), &payload); err != nil {
		panic(fmt.Sprintf("listMatrix(%q): %v", fields, err))
	}
	for _, required := range []string{"reqGrid", "reqUrlGrid", "reqShadeGrid", "reqShadeList", "reqPayloads"} {
		if _, ok := payload[required]; !ok {
			payload[required] = json.RawMessage(`[]`)
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// scalarRuleMatrix builds a ScalarRuleMatrix payload: reqAmount 0 and
// reqRanks [] unless fields sets them, plus the given fields.
func scalarRuleMatrix(fields string) string {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte("{"+fields+"}"), &payload); err != nil {
		panic(fmt.Sprintf("scalarRuleMatrix(%q): %v", fields, err))
	}
	for key, value := range map[string]string{"reqAmount": `0`, "reqRanks": `[]`} {
		if _, ok := payload[key]; !ok {
			payload[key] = json.RawMessage(value)
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

var vectors = []parityVector{
	{
		name:    "valid_full",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "optStr": "ok", "reqList": ["a"], "optList": ["b"], "optNum": 5.5}`,
		want:    map[string][]string{},
	},
	{
		// The regression shape: omitted optional list must not
		// trip listMin.
		name:    "optional_fields_absent",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"]}`,
		want:    map[string][]string{},
	},
	{
		name:    "optional_fields_null",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optStr": null, "optList": null, "optNum": null}`,
		want:    map[string][]string{},
	},
	{
		name:    "opt_list_explicit_empty",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optList": []}`,
		want:    map[string][]string{"optList": {"listMin"}},
	},
	{
		name:    "opt_list_over_listmax",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optList": ["a", "b", "c", "d"]}`,
		want:    map[string][]string{"optList": {"listMax"}},
	},
	{
		name:    "opt_list_item_too_long",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optList": ["toolong"]}`,
		want:    map[string][]string{"optList[0]": {"maxLength"}},
	},
	{
		name:    "req_str_too_long",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "toolong", "reqList": ["a"]}`,
		want:    map[string][]string{"reqStr": {"maxLength"}},
	},
	{
		// All three languages report listMin (not required) for an
		// explicitly empty required list that carries a listMin constraint.
		name:    "req_list_explicit_empty",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": []}`,
		want:    map[string][]string{"reqList": {"listMin"}},
	},
	{
		name:    "opt_str_too_long",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optStr": "toolong"}`,
		want:    map[string][]string{"optStr": {"maxLength"}},
	},
	{
		// A required list means present, not non-empty. Go used to reject []
		// here as a missing required field while TypeScript checked null only
		// and Rust validated nothing, so one payload was invalid in one
		// language and valid in the others. Non-emptiness is declared with
		// listMin, which reqList above still carries.
		name:    "req_scalar_list_explicit_empty",
		payload: `{"reqScalarList": [], "reqStr": "ok", "reqList": ["a"]}`,
		want:    map[string][]string{},
	},
	{
		name:    "req_scalar_list_absent",
		payload: `{"reqStr": "ok", "reqList": ["a"]}`,
		want:    map[string][]string{"reqScalarList": {"required"}},
	},
	{
		name:    "req_scalar_list_null",
		payload: `{"reqScalarList": null, "reqStr": "ok", "reqList": ["a"]}`,
		want:    map[string][]string{"reqScalarList": {"required"}},
	},
	{
		name:    "opt_num_below_min",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optNum": 0.5}`,
		want:    map[string][]string{"optNum": {"min"}},
	},
	{
		// A list element is never null, in an optional list too. The
		// generated Go decoder refuses it: json.Unmarshal alone would
		// decode it to the element type's zero value.
		name:          "opt_list_null_element",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optList": ["a", null]}`,
		want:          map[string][]string{"optList[1]": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		name:          "req_list_null_element",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": [null, "a"]}`,
		want:          map[string][]string{"reqList[0]": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		// A scalar list element is never null either.
		name:          "scalar_list_null_element",
		payload:       `{"reqScalarList": ["https://a.test", null], "reqStr": "ok", "reqList": ["a"], "names": [null], "ranks": [null, 2]}`,
		want:          map[string][]string{"reqScalarList[1]": {"required"}, "names[0]": {"required"}, "ranks[0]": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		// A malformed scalar value is one "pattern" error, for a single
		// field and a list element alike.
		name:    "url_bad_format",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "url": "not a url"}`,
		want:    map[string][]string{"url": {"pattern"}},
	},
	{
		// Contact.Email is checked by its pattern and by the scalar core's
		// custom validator; the value is still reported once.
		name:    "email_bad_format",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "email": "not an email"}`,
		want:    map[string][]string{"email": {"pattern"}},
	},
	{
		name:    "req_scalar_list_bad_format",
		payload: `{"reqScalarList": ["https://a.test", "not a url"], "reqStr": "ok", "reqList": ["a"]}`,
		want:    map[string][]string{"reqScalarList[1]": {"pattern"}},
	},
	{
		// A scalar value out of its length bounds is "maxLength" or
		// "minLength", as a single field and a list element alike, not the
		// scalar core's own name for it.
		name:    "url_too_long",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "url": "https://` + strings.Repeat("a", 2050) + `.test"}`,
		want:    map[string][]string{"url": {"maxLength"}},
	},
	{
		name:    "name_too_short",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "name": "a"}`,
		want:    map[string][]string{"name": {"minLength"}},
	},
	{
		name:    "names_element_too_long",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "names": ["Ada", "` + strings.Repeat("n", 81) + `"]}`,
		want:    map[string][]string{"names[1]": {"maxLength"}},
	},
	{
		// A scalar value out of its range is "min" or "max". The rank is
		// negative, not 0: the Go type leaves an optional integer scalar
		// that is 0 unchecked, as unset.
		name:    "rank_below_min",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "rank": -3}`,
		want:    map[string][]string{"rank": {"min"}},
	},
	{
		name:    "ranks_element_below_min",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "ranks": [2, -1]}`,
		want:    map[string][]string{"ranks[1]": {"min"}},
	},
	{
		name:    "share_above_max",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "share": 1.5}`,
		want:    map[string][]string{"share": {"max"}},
	},
	{
		// A string scalar's element must be a string: a number is a type
		// error, not a format error. Go's json.Unmarshal and pydantic's
		// strict parse refuse the payload first.
		name:          "req_scalar_list_bad_element",
		payload:       `{"reqScalarList": ["https://a.test", 42], "reqStr": "ok", "reqList": ["a"]}`,
		want:          map[string][]string{"reqScalarList[1]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},

	// A present value of the wrong JSON type is "type", whatever the field's
	// rules: the type check comes before length, pattern and range, so the
	// value is reported once. It applies to a builtin (string, number,
	// boolean) and a scalar field, required or optional. Go's
	// json.Unmarshal and pydantic's strict parse refuse the payload first.
	{
		// A missing required string is "required" only: its length is not
		// measured.
		name:    "req_str_absent",
		payload: `{"reqScalarList": ["https://a.test"], "reqList": ["a"]}`,
		want:    map[string][]string{"reqStr": {"required"}},
	},
	{
		name:          "req_str_wrong_type",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": 1234567, "reqList": ["a"]}`,
		want:          map[string][]string{"reqStr": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "opt_str_wrong_type",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optStr": 42}`,
		want:          map[string][]string{"optStr": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "opt_num_wrong_type",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optNum": "50"}`,
		want:          map[string][]string{"optNum": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "opt_bool_wrong_type",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optBool": "true"}`,
		want:          map[string][]string{"optBool": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		// An integer scalar given a non-integer number is "type".
		name:          "rank_not_integer",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "rank": 1.5}`,
		want:          map[string][]string{"rank": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "rank_wrong_type",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "rank": "3"}`,
		want:          map[string][]string{"rank": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "share_wrong_type",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "share": "0.5"}`,
		want:          map[string][]string{"share": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		// An optional string scalar given a number is "type", as a
		// required one is.
		name:          "url_wrong_type",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "url": 42}`,
		want:          map[string][]string{"url": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		// A list given a value that is not a list is "type", required or
		// optional: the value is present, of the wrong JSON type.
		name:          "req_list_not_a_list",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": "a"}`,
		want:          map[string][]string{"reqList": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "req_scalar_list_not_a_list",
		payload:       `{"reqScalarList": "https://a.test", "reqStr": "ok", "reqList": ["a"]}`,
		want:          map[string][]string{"reqScalarList": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name: "opt_list_not_a_list",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optList": "a", "optNums": 5,
			"optBools": true, "names": "Ada", "ranks": 3, "whens": "2026-01-02T03:04:05Z"}`,
		want: map[string][]string{
			"optList":  {"type"},
			"optNums":  {"type"},
			"optBools": {"type"},
			"names":    {"type"},
			"ranks":    {"type"},
			"whens":    {"type"},
		},
		decodeRejects: []string{"go", "python"},
	},

	// An enum holds a string (D14, amended): a value of another JSON type is
	// "type", and a string outside the enum is "enum".
	{
		name:          "enum_wrong_type",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "shade": 1}`,
		want:          map[string][]string{"shade": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "enum_bad_value",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "shade": "purple"}`,
		want:          map[string][]string{"shade": {"enum"}},
		decodeRejects: []string{"python"},
	},

	// An object field holds a JSON object: any other value is "type".
	{
		name:          "object_wrong_type",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "point": "dark"}`,
		want:          map[string][]string{"point": {"type"}},
		decodeRejects: []string{"go", "python"},
	},

	// Temporal.DateTime is a string scalar: its wire value is a string, and
	// any other JSON type is "type". The generated TypeScript validator also
	// takes a Date, the value its generated type holds.
	{
		name:    "date_time_valid",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "when": "2026-01-02T03:04:05Z", "whens": ["2026-01-02T03:04:05Z"]}`,
		want:    map[string][]string{},
	},
	{
		name:          "date_time_wrong_type",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "when": 42, "whens": ["2026-01-02T03:04:05Z", {}]}`,
		want:          map[string][]string{"when": {"type"}, "whens[1]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},

	// String lengths count Unicode code points: an astral character such as
	// U+1F600 is one, not two UTF-16 units or four UTF-8 bytes, and a
	// multi-byte BMP character such as U+00E9 is one, not two UTF-8 bytes.
	// reqStr, optStr and optList allow 5, Identity.Name 2 to 80.
	{
		name: "lengths_count_code_points",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00",
			"optStr": "\u00e9\u00e9\u00e9\u00e9\u00e9", "reqList": ["\u00e9t\u00e9"], "optList": ["\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00"],
			"name": "\ud83d\ude00\ud83d\ude00", "names": ["` + strings.Repeat(`\ud83d\ude00`, 80) + `"]}`,
		want: map[string][]string{},
	},
	{
		name: "lengths_over_by_code_points",
		payload: `{"reqScalarList": ["https://a.test"], "reqStr": "\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00",
			"optStr": "\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9", "reqList": ["a"], "optList": ["\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00"],
			"name": "\ud83d\ude00", "names": ["Ada", "` + strings.Repeat(`\u00e9`, 81) + `"]}`,
		want: map[string][]string{
			"reqStr":     {"maxLength"},
			"optStr":     {"maxLength"},
			"optList[0]": {"maxLength"},
			"name":       {"minLength"},
			"names[1]":   {"maxLength"},
		},
	},
	{
		name:          "req_list_wrong_type_element",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a", 1234567]}`,
		want:          map[string][]string{"reqList[1]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "opt_nums_wrong_type_element",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optNums": [5, "50"]}`,
		want:          map[string][]string{"optNums[1]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "opt_bools_wrong_type_element",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "optBools": [true, "true"]}`,
		want:          map[string][]string{"optBools[1]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "ranks_element_not_integer",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "ranks": [2, 1.5]}`,
		want:          map[string][]string{"ranks[1]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		// An element of an optional string scalar list given a number is
		// "type", as one of a required list is.
		name:          "names_wrong_type_element",
		payload:       `{"reqScalarList": ["https://a.test"], "reqStr": "ok", "reqList": ["a"], "names": ["Ada", 42]}`,
		want:          map[string][]string{"names[1]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},

	// T[] element types beyond builtins and scalars.
	{
		// A list element is never null.
		name:          "list_null_element",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"reqShadeList": ["dark", null]`),
		want:          map[string][]string{"reqShadeList[1]": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		// Nor is a boolean element, or a Generic.JSON element: null is a
		// missing Generic.JSON value (D14, amended).
		name:          "list_null_element_every_kind",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"flags": [true, null], "payloads": [{"a": 1}, null]`),
		want:          map[string][]string{"flags[1]": {"required"}, "payloads[1]": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		// A Generic.JSON element is never null in a required list either:
		// requiredness decides only whether the list may be missing (D12,
		// amended). A null inside an element is part of its value.
		name:          "required_json_list_null_element",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"reqPayloads": [{"a": null}, null]`),
		want:          map[string][]string{"reqPayloads[1]": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		// A null object element is "required", not an object whose own
		// required fields are missing.
		name:          "list_null_object_element",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"pointList": [{"shade": "dark"}, null]`),
		want:          map[string][]string{"pointList[1]": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		name:          "list_bad_enum_element",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"reqShadeList": ["light", "purple"]`),
		want:          map[string][]string{"reqShadeList[1]": {"enum"}},
		decodeRejects: []string{"python"},
	},
	{
		name:          "list_bad_object_element",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"pointList": [{"shade": "dark"}, {"shade": "purple"}]`),
		want:          map[string][]string{"pointList[1].shade": {"enum"}},
		decodeRejects: []string{"python"},
	},
	{
		// A list of every element kind given a value that is not a list is
		// "type" at the field, required or optional.
		name:     "list_not_a_list",
		typeName: "ListMatrix",
		payload:  listMatrix(`"reqShadeList": "dark", "pointList": {"shade": "dark"}, "flags": true, "payloads": {"a": 1}`),
		want: map[string][]string{
			"reqShadeList": {"type"},
			"pointList":    {"type"},
			"flags":        {"type"},
			"payloads":     {"type"},
		},
		decodeRejects: []string{"go", "python"},
	},
	{
		// An enum element of another JSON type is "type", in a list, a list
		// of lists and a nested object alike.
		name:          "enum_element_wrong_type",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"reqShadeList": ["dark", 1], "reqShadeGrid": [["light", false]], "pointList": [{"shade": 2}]`),
		want:          map[string][]string{"reqShadeList[1]": {"type"}, "reqShadeGrid[0][1]": {"type"}, "pointList[0].shade": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		// An object element that is not an object is "type".
		name:          "object_element_wrong_type",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"pointList": [{"shade": "dark"}, "dark"], "pointGrid": [[5]]`),
		want:          map[string][]string{"pointList[1]": {"type"}, "pointGrid[0][0]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},

	// T[][] (D12).
	{
		name:     "grid_valid_ragged",
		typeName: "ListMatrix",
		payload: listMatrix(`"reqGrid": [["a", "b", "c"], [], ["d"]], "optGrid": [["e"], []], "numGrid": [[1, 2.5], [10]],
			"reqUrlGrid": [["https://a.test"], []], "reqShadeGrid": [["light"], ["dark", "light"]],
			"pointGrid": [[{"shade": "light"}], []], "reqShadeList": ["dark"], "pointList": [{"shade": "light"}],
			"flags": [true, false], "payloads": [{"a": 1}, [1, 2], "{}"]`),
		want: map[string][]string{},
	},
	{
		// A required list of lists means present, not non-empty.
		name:     "grid_required_outer_empty",
		typeName: "ListMatrix",
		payload:  listMatrix(""),
		want:     map[string][]string{},
	},
	{
		name:     "grid_required_outer_absent",
		typeName: "ListMatrix",
		payload:  `{"reqUrlGrid": [], "reqShadeGrid": [], "reqShadeList": [], "reqPayloads": []}`,
		want:     map[string][]string{"reqGrid": {"required"}},
	},
	{
		name:          "grid_required_outer_not_a_list",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"reqGrid": "a", "reqUrlGrid": "https://a.test", "reqShadeGrid": "dark"`),
		want:          map[string][]string{"reqGrid": {"type"}, "reqUrlGrid": {"type"}, "reqShadeGrid": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:     "grid_optional_outer_not_a_list",
		typeName: "ListMatrix",
		payload:  listMatrix(`"optGrid": "a", "numGrid": 5, "pointGrid": {"shade": "dark"}, "boolGrid": false, "rankGrid": 1`),
		want: map[string][]string{
			"optGrid":   {"type"},
			"numGrid":   {"type"},
			"pointGrid": {"type"},
			"boolGrid":  {"type"},
			"rankGrid":  {"type"},
		},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:     "grid_required_outer_null",
		typeName: "ListMatrix",
		payload:  listMatrix(`"reqGrid": null`),
		want:     map[string][]string{"reqGrid": {"required"}},
	},
	{
		name:     "grid_optional_null",
		typeName: "ListMatrix",
		payload:  listMatrix(`"optGrid": null, "numGrid": null, "pointGrid": null, "pointList": null`),
		want:     map[string][]string{},
	},
	{
		name:     "grid_inner_empty",
		typeName: "ListMatrix",
		payload:  listMatrix(`"reqGrid": [[]], "reqUrlGrid": [[], []], "optGrid": [[]], "reqShadeGrid": [[]], "pointGrid": [[], []]`),
		want:     map[string][]string{},
	},
	{
		name:     "grid_inner_null",
		typeName: "ListMatrix",
		payload:  listMatrix(`"reqGrid": [["a"], null], "reqUrlGrid": [null], "optGrid": [null], "reqShadeGrid": [[], null], "pointGrid": [null]`),
		want: map[string][]string{
			"reqGrid[1]":      {"required"},
			"reqUrlGrid[0]":   {"required"},
			"optGrid[0]":      {"required"},
			"reqShadeGrid[1]": {"required"},
			"pointGrid[0]":    {"required"},
		},
	},
	{
		// Go's json.Unmarshal refuses a value where a list belongs.
		name:          "grid_inner_not_a_list",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"reqGrid": [["a"], "b"], "optGrid": [7]`),
		want:          map[string][]string{"reqGrid[1]": {"type"}, "optGrid[0]": {"type"}},
		decodeRejects: []string{"go"},
	},
	{
		// An innermost element is never null.
		name:          "grid_innermost_null",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"reqShadeGrid": [["light", null]]`),
		want:          map[string][]string{"reqShadeGrid[0][1]": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		// An innermost element is never null, whatever its type.
		name:     "grid_innermost_null_every_kind",
		typeName: "ListMatrix",
		payload: listMatrix(`"reqGrid": [[null]], "optGrid": [["a", null]], "numGrid": [[null, 2]], "reqUrlGrid": [[null]], "pointGrid": [[null]],
			"boolGrid": [[true, null]], "rankGrid": [[null]]`),
		want: map[string][]string{
			"reqGrid[0][0]":    {"required"},
			"optGrid[0][1]":    {"required"},
			"numGrid[0][0]":    {"required"},
			"reqUrlGrid[0][0]": {"required"},
			"pointGrid[0][0]":  {"required"},
			"boolGrid[0][1]":   {"required"},
			"rankGrid[0][0]":   {"required"},
		},
		decodeRejects: []string{"go"},
	},
	{
		name:     "grid_bad_scalar_format",
		typeName: "ListMatrix",
		payload:  listMatrix(`"reqUrlGrid": [["https://a.test", "not a url"]]`),
		want:     map[string][]string{"reqUrlGrid[0][1]": {"pattern"}},
	},
	{
		// listMin and listMax bound the outer list only.
		name:     "grid_outer_over_listmax",
		typeName: "ListMatrix",
		payload:  listMatrix(`"optGrid": [["a"], ["b"], ["c"]]`),
		want:     map[string][]string{"optGrid": {"listMax"}},
	},
	{
		name:     "grid_outer_under_listmin",
		typeName: "ListMatrix",
		payload:  listMatrix(`"optGrid": []`),
		want:     map[string][]string{"optGrid": {"listMin"}},
	},
	{
		name:     "grid_inner_unbounded",
		typeName: "ListMatrix",
		payload:  listMatrix(`"optGrid": [["a", "b", "c", "d", "e"]]`),
		want:     map[string][]string{},
	},
	{
		// A field's own constraints apply to every innermost element.
		name:     "grid_element_constraints",
		typeName: "ListMatrix",
		payload:  listMatrix(`"reqGrid": [["ok"], ["fine", "toolong"]], "optGrid": [["toolong"]], "numGrid": [[5], [0.5, 11]]`),
		want: map[string][]string{
			"reqGrid[1][1]": {"maxLength"},
			"optGrid[0][0]": {"maxLength"},
			"numGrid[1][0]": {"min"},
			"numGrid[1][1]": {"max"},
		},
	},
	{
		name:          "grid_bad_enum_element",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"reqShadeGrid": [["light"], ["dark", "purple"]]`),
		want:          map[string][]string{"reqShadeGrid[1][1]": {"enum"}},
		decodeRejects: []string{"python"},
	},
	{
		name:          "grid_bad_scalar_element",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"reqUrlGrid": [[], ["https://a.test", 42]]`),
		want:          map[string][]string{"reqUrlGrid[1][1]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		// An innermost element of the wrong JSON type is "type", once.
		name:     "grid_wrong_type_innermost",
		typeName: "ListMatrix",
		payload: listMatrix(`"reqGrid": [["a", 1234567]], "numGrid": [[5, "50"]], "boolGrid": [[true, "true"]],
			"rankGrid": [[1, 1.5]]`),
		want: map[string][]string{
			"reqGrid[0][1]":  {"type"},
			"numGrid[0][1]":  {"type"},
			"boolGrid[0][1]": {"type"},
			"rankGrid[0][1]": {"type"},
		},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:          "grid_bad_object_element",
		typeName:      "ListMatrix",
		payload:       listMatrix(`"pointGrid": [[{"shade": "light"}, {"shade": "purple"}]]`),
		want:          map[string][]string{"pointGrid[0][1].shade": {"enum"}},
		decodeRejects: []string{"python"},
	},
	{
		// Innermost string lengths count code points (D14, amended).
		name:     "grid_lengths_count_code_points",
		typeName: "ListMatrix",
		payload: listMatrix(`"reqGrid": [["\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00", "\u00e9\u00e9\u00e9\u00e9\u00e9"],
			["\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00"]]`),
		want: map[string][]string{"reqGrid[1][0]": {"maxLength"}},
	},

	// Generic.JSON: any JSON value but null. A value of every JSON type is
	// valid in a required field, an optional one and a list element, and a
	// null nested inside an object or array is part of the value.
	{
		name:     "json_object",
		typeName: "JsonMatrix",
		payload:  `{"reqJson": {"k": 1, "none": null}, "optJson": {}, "jsonList": [{"k": [1, null]}], "jsonGrid": [[{"k": 1}], []]}`,
		want:     map[string][]string{},
	},
	{
		name:     "json_array",
		typeName: "JsonMatrix",
		payload:  `{"reqJson": [1, "two", null], "optJson": [], "jsonList": [[1, 2], []], "jsonGrid": [[[1], []]]}`,
		want:     map[string][]string{},
	},
	{
		// A string need not be JSON text: "not json" is the JSON string
		// "not json", not a malformed value.
		name:     "json_string",
		typeName: "JsonMatrix",
		payload:  `{"reqJson": "not json", "optJson": "", "jsonList": ["s", ""], "jsonGrid": [["{"]]}`,
		want:     map[string][]string{},
	},
	{
		name:     "json_number",
		typeName: "JsonMatrix",
		payload:  `{"reqJson": 42, "optJson": -1.5, "jsonList": [0, 3.25], "jsonGrid": [[1e3]]}`,
		want:     map[string][]string{},
	},
	{
		name:     "json_boolean",
		typeName: "JsonMatrix",
		payload:  `{"reqJson": true, "optJson": false, "jsonList": [false, true], "jsonGrid": [[false]]}`,
		want:     map[string][]string{},
	},
	{
		name:     "json_required_null",
		typeName: "JsonMatrix",
		payload:  `{"reqJson": null}`,
		want:     map[string][]string{"reqJson": {"required"}},
	},
	{
		name:     "json_required_absent",
		typeName: "JsonMatrix",
		payload:  `{"optJson": {"k": 1}}`,
		want:     map[string][]string{"reqJson": {"required"}},
	},
	{
		name:     "json_optional_null",
		typeName: "JsonMatrix",
		payload:  `{"reqJson": 1, "optJson": null, "jsonList": null, "jsonGrid": null}`,
		want:     map[string][]string{},
	},
	{
		name:     "json_optional_absent",
		typeName: "JsonMatrix",
		payload:  `{"reqJson": 1}`,
		want:     map[string][]string{},
	},
	{
		// The generated Go decoder refuses a null list element of every
		// type, Generic.JSON included (D12, amended).
		name:          "json_list_null_element",
		typeName:      "JsonMatrix",
		payload:       `{"reqJson": 1, "jsonList": [1, null], "jsonGrid": [[null, 1]]}`,
		want:          map[string][]string{"jsonList[1]": {"required"}, "jsonGrid[0][0]": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		// A list of Generic.JSON is still a list: an object or a string where
		// the list belongs is "type".
		name:          "json_list_not_a_list",
		typeName:      "JsonMatrix",
		payload:       `{"reqJson": 1, "jsonList": {"a": 1}, "jsonGrid": "x"}`,
		want:          map[string][]string{"jsonList": {"type"}, "jsonGrid": {"type"}},
		decodeRejects: []string{"go", "python"},
	},

	// Generic.StringMap and Embedding.Vector hold a JSON object and a JSON
	// array (D14, amended): the value every generated type puts on the wire.
	// A string holding that value's JSON text is accepted too. Any other JSON
	// type is "type", and null is a missing value.
	{
		name:     "structured_value",
		typeName: "StructuredMatrix",
		payload:  `{"reqMap": {"k": "v"}, "optMap": {}, "mapList": [{"a": "b"}, {}], "reqVec": [0.5, -1], "optVec": [], "vecList": [[1, 2.5], []]}`,
		want:     map[string][]string{},
	},
	{
		// The JSON text of the value, the form the runtimes took before. The
		// Go decoder takes only the object or array.
		name:          "structured_text",
		typeName:      "StructuredMatrix",
		payload:       `{"reqMap": "{\"k\": \"v\"}", "optMap": "{}", "mapList": ["{\"a\": \"b\"}"], "reqVec": "[0.5, -1]", "optVec": "[]", "vecList": ["[1, 2.5]"]}`,
		want:          map[string][]string{},
		decodeRejects: []string{"go", "rust"},
	},
	{
		// An array where an object belongs, a number and a boolean are "type".
		name:          "structured_map_wrong_type",
		typeName:      "StructuredMatrix",
		payload:       `{"reqMap": ["a"], "optMap": 42, "mapList": [{}, true], "reqVec": []}`,
		want:          map[string][]string{"reqMap": {"type"}, "optMap": {"type"}, "mapList[1]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		// An object where an array belongs, a number and a boolean are "type".
		name:          "structured_vector_wrong_type",
		typeName:      "StructuredMatrix",
		payload:       `{"reqMap": {}, "reqVec": {"k": 1}, "optVec": 1.5, "vecList": [[1], false]}`,
		want:          map[string][]string{"reqVec": {"type"}, "optVec": {"type"}, "vecList[1]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		name:     "structured_required_null",
		typeName: "StructuredMatrix",
		payload:  `{"reqMap": null, "reqVec": null}`,
		want:     map[string][]string{"reqMap": {"required"}, "reqVec": {"required"}},
	},
	{
		name:     "structured_required_absent",
		typeName: "StructuredMatrix",
		payload:  `{"optMap": {"k": "v"}, "optVec": [1]}`,
		want:     map[string][]string{"reqMap": {"required"}, "reqVec": {"required"}},
	},
	{
		// An empty object or array is a value, required or not.
		name:     "structured_optional_null",
		typeName: "StructuredMatrix",
		payload:  `{"reqMap": {}, "reqVec": [], "optMap": null, "optVec": null, "mapList": null, "vecList": null}`,
		want:     map[string][]string{},
	},
	{
		name:     "structured_optional_absent",
		typeName: "StructuredMatrix",
		payload:  `{"reqMap": {"k": "v"}, "reqVec": [1]}`,
		want:     map[string][]string{},
	},
	{
		// An empty string is no JSON text: a missing value, as for a string
		// scalar. A required one is "required"; an optional one, or a list
		// element, is absent.
		name:          "structured_empty_text",
		typeName:      "StructuredMatrix",
		payload:       `{"reqMap": "", "optMap": "", "mapList": [""], "reqVec": "", "optVec": "", "vecList": [""]}`,
		want:          map[string][]string{"reqMap": {"required"}, "reqVec": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		name:          "structured_list_null_element",
		typeName:      "StructuredMatrix",
		payload:       `{"reqMap": {}, "reqVec": [], "mapList": [{}, null], "vecList": [null, [1]]}`,
		want:          map[string][]string{"mapList[1]": {"required"}, "vecList[0]": {"required"}},
		decodeRejects: []string{"go"},
	},
	{
		// A list of JSON object or array scalars given one object, or one
		// JSON text, is "type" at the field.
		name:          "structured_list_not_a_list",
		typeName:      "StructuredMatrix",
		payload:       `{"reqMap": {}, "reqVec": [], "mapList": {"k": "v"}, "vecList": "[1]"}`,
		want:          map[string][]string{"mapList": {"type"}, "vecList": {"type"}},
		decodeRejects: []string{"go", "python"},
	},

	// A scalar field's own rules follow the scalar's validation. The
	// generated TypeScript validator dropped them: {"reqAmount": -1} passed
	// Validate<Generic.Int64, { min: 0 }> there and failed "min" elsewhere.
	{
		// Every bound is inclusive.
		name:     "scalar_rules_at_bounds",
		typeName: "ScalarRuleMatrix",
		payload: scalarRuleMatrix(`"reqAmount": 1000, "optRank": 10, "optShare": 0.5, "optName": "abcde",
			"names": ["ab", "cde"], "reqRanks": [1, 10], "rankGrid": [[1, 10], []]`),
		want: map[string][]string{},
	},
	{
		name:     "scalar_rules_optional_absent",
		typeName: "ScalarRuleMatrix",
		payload:  scalarRuleMatrix(``),
		want:     map[string][]string{},
	},
	{
		name:     "scalar_field_min",
		typeName: "ScalarRuleMatrix",
		payload:  scalarRuleMatrix(`"reqAmount": -1`),
		want:     map[string][]string{"reqAmount": {"min"}},
	},
	{
		// A float bound (0.5) and an integer scalar's field maximum under
		// the scalar's own.
		name:     "scalar_field_max",
		typeName: "ScalarRuleMatrix",
		payload:  scalarRuleMatrix(`"reqAmount": 1001, "optRank": 11, "optShare": 0.75`),
		want:     map[string][]string{"reqAmount": {"max"}, "optRank": {"max"}, "optShare": {"max"}},
	},
	{
		// The field's maxLength (5) is under the scalar's (80), and counts
		// code points: five astral characters pass, six accented ones fail.
		name:     "scalar_field_max_length",
		typeName: "ScalarRuleMatrix",
		payload: scalarRuleMatrix(`"optName": "abcdef",
			"names": ["\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00", "\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9"]`),
		want: map[string][]string{"optName": {"maxLength"}, "names[1]": {"maxLength"}},
	},
	{
		name:     "scalar_field_pattern",
		typeName: "ScalarRuleMatrix",
		payload:  scalarRuleMatrix(`"optName": "abC"`),
		want:     map[string][]string{"optName": {"pattern"}},
	},
	{
		// The field's rules apply to every element and innermost element.
		name:     "scalar_field_rules_on_elements",
		typeName: "ScalarRuleMatrix",
		payload:  scalarRuleMatrix(`"reqRanks": [1, 11], "rankGrid": [[2], [3, 12]]`),
		want:     map[string][]string{"reqRanks[1]": {"max"}, "rankGrid[1][1]": {"max"}},
	},
	{
		// The list bound and the element rules together.
		name:     "scalar_field_list_max_and_element",
		typeName: "ScalarRuleMatrix",
		payload:  scalarRuleMatrix(`"names": ["ab", "abcdef", "cd"]`),
		want:     map[string][]string{"names": {"listMax"}, "names[1]": {"maxLength"}},
	},
	{
		// A value only the scalar's own rules reject is still one error,
		// named by the scalar's rule. (An optional 0 would be the Go type's
		// zero value, which its Validate skips as absent.)
		name:     "scalar_own_rules_beside_field_rules",
		typeName: "ScalarRuleMatrix",
		payload:  scalarRuleMatrix(`"optRank": -1, "reqRanks": [0], "optName": "a"`),
		want:     map[string][]string{"optRank": {"min"}, "reqRanks[0]": {"min"}, "optName": {"minLength"}},
	},
	{
		// The field's rules run after a scalar failure, as in the Go types:
		// "A" is under the scalar's minLength and outside the field's pattern.
		name:     "scalar_and_field_rules_fail",
		typeName: "ScalarRuleMatrix",
		payload:  scalarRuleMatrix(`"optName": "A"`),
		want:     map[string][]string{"optName": {"minLength", "pattern"}},
	},
	{
		// A value of the wrong JSON type is one "type" error: the field's
		// rules check only a value of their own type.
		name:          "scalar_field_rules_wrong_type",
		typeName:      "ScalarRuleMatrix",
		payload:       scalarRuleMatrix(`"reqAmount": "-5", "optName": 42, "names": [7]`),
		want:          map[string][]string{"reqAmount": {"type"}, "optName": {"type"}, "names[0]": {"type"}},
		decodeRejects: []string{"go", "python"},
	},
	{
		// A pattern matches code points (D14, amended): an astral character
		// (U+1F600) is one "." and one "\W", and two of them are two
		// "[^a-z]". Without the "u" flag TypeScript saw two UTF-16 units in
		// each astral character and refused every value that holds one.
		name:     "pattern_astral_character_is_one_character",
		typeName: "PatternMatrix",
		payload: `{"glyph": "\ud83d\ude00", "glyphs": ["\ud83d\ude00", "\u00e9", "a"],
			"glyphGrid": [["\ud83d\ude00"], ["a", "\ud83d\ude00"]], "spaced": "a\ud83d\ude00b",
			"pair": "\ud83d\ude00\ud83d\ude00", "pairName": "\ud83d\ude00\ud83d\ude00", "pairNames": ["\ud83d\ude00\ud83d\ude00", "12"]}`,
		want: map[string][]string{},
	},
	{
		// One astral character is not two characters, which TypeScript
		// without the "u" flag accepted as two UTF-16 units; two are not one
		// "." or one "\W".
		name:     "pattern_astral_character_is_not_two",
		typeName: "PatternMatrix",
		payload: `{"glyph": "\ud83d\ude00\ud83d\ude00", "glyphs": ["a", "\ud83d\ude00\ud83d\ude00"],
			"glyphGrid": [["a"], ["\ud83d\ude00", "ab"]], "spaced": "a\ud83d\ude00\ud83d\ude00b",
			"pair": "\ud83d\ude00", "pairNames": ["\ud83d\ude00a"]}`,
		want: map[string][]string{
			"glyph": {"pattern"}, "glyphs[1]": {"pattern"}, "glyphGrid[1][1]": {"pattern"},
			"spaced": {"pattern"}, "pair": {"pattern"}, "pairNames[0]": {"pattern"},
		},
	},
}

// knownDivergences pins where a language's generated validator disagrees with
// the expected column today. Key: language -> vector name -> that language's
// actual verdicts. When the generator is fixed the pin goes stale and this
// test fails, forcing the entry's removal in the same change. The runtime
// suites take no pins: every runtime returns the expected column.
var knownDivergences = map[string]map[string]map[string][]string{
	"go": {
		// json.Unmarshal decodes a missing required string field into "",
		// which Validate cannot tell from a present empty string, and a
		// present "" satisfies a required builtin string in every
		// validator. D14's amendment lists it as open.
		"req_str_absent": {},
	},
}

func stageParityService(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(schemaConfigJSON), 0o644); err != nil {
		t.Fatalf("write schema.config.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "parity-matrix.schema.json"), []byte(parityMatrixSchemaJSON), 0o644); err != nil {
		t.Fatalf("write parity-matrix.schema.json: %v", err)
	}
	return dir
}

// driverVector is one vector as the language drivers read it.
type driverVector struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	// Decode is true when this language's typed decoder must refuse the
	// payload; the Python driver then runs the strict parse instead of
	// validate_all.
	Decode map[string]bool `json:"decode,omitempty"`
}

func writeVectorsFile(t *testing.T, dir string) string {
	t.Helper()
	payloads := map[string]driverVector{}
	for _, v := range vectors {
		dv := driverVector{Type: v.typ(), Payload: json.RawMessage(v.payload)}
		for _, lang := range v.decodeRejects {
			if dv.Decode == nil {
				dv.Decode = map[string]bool{}
			}
			dv.Decode[lang] = true
		}
		payloads[v.name] = dv
	}
	data, err := json.MarshalIndent(payloads, "", "  ")
	if err != nil {
		t.Fatalf("marshal vectors: %v", err)
	}
	path := filepath.Join(dir, "vectors.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write vectors: %v", err)
	}
	return path
}

func readResults(t *testing.T, path string) verdicts {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read driver results: %v", err)
	}
	var results verdicts
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("decode driver results: %v", err)
	}
	return results
}

// expectedVerdicts is the verdict lang must return for v: a knownDivergences
// pin, decodeRejected when lang's typed decoder refuses the payload, or the
// shared expected column.
func expectedVerdicts(lang string, v parityVector) map[string][]string {
	if pinned, ok := knownDivergences[lang][v.name]; ok {
		return pinned
	}
	for _, rejecting := range v.decodeRejects {
		if rejecting == lang {
			return decodeRejected
		}
	}
	return v.want
}

// assertVerdicts compares one language's results against the expected column,
// applying that language's knownDivergences pins and decode refusals.
func assertVerdicts(t *testing.T, lang string, results verdicts) {
	t.Helper()
	for _, v := range vectors {
		got, ok := results[v.name]
		if !ok {
			t.Errorf("%s: vector %s missing from driver results", lang, v.name)
			continue
		}
		want := expectedVerdicts(lang, v)
		if got == nil {
			got = map[string][]string{}
		}
		normalized := map[string][]string{}
		for field, validators := range got {
			sorted := append([]string(nil), validators...)
			sort.Strings(sorted)
			normalized[field] = sorted
		}
		if !reflect.DeepEqual(normalized, want) {
			t.Errorf("%s: vector %s verdicts diverge\n  payload: %s\n  want: %v\n  got:  %v",
				lang, v.name, v.payload, want, normalized)
		}
	}
	for name := range results {
		found := false
		for _, v := range vectors {
			if v.name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: driver returned verdicts for unknown vector %s", lang, name)
		}
	}
}

// TestParityTableIsConsistent keeps the table honest: every pin names a
// vector, and a pin never repeats the expected column.
func TestParityTableIsConsistent(t *testing.T) {
	byName := map[string]parityVector{}
	for _, v := range vectors {
		if _, dup := byName[v.name]; dup {
			t.Errorf("duplicate vector %s", v.name)
		}
		byName[v.name] = v
	}
	for lang, pins := range knownDivergences {
		for name, pinned := range pins {
			v, ok := byName[name]
			if !ok {
				t.Errorf("%s: pin for unknown vector %s", lang, name)
				continue
			}
			if reflect.DeepEqual(pinned, v.want) {
				t.Errorf("%s: pin for %s repeats the expected column; remove it", lang, name)
			}
		}
	}
}

// goDriverTest decodes each payload into the generated type the vector names
// and runs Validate. A payload json.Unmarshal refuses reports decodeRejected.
const goDriverTest = `package types

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

type parityValidatable interface{ Validate() ValidationErrors }

func newParityValue(typeName string) parityValidatable {
	switch typeName {
	case "ParityMatrix":
		return &ParityMatrix{}
	case "ListMatrix":
		return &ListMatrix{}
	case "JsonMatrix":
		return &JsonMatrix{}
	case "StructuredMatrix":
		return &StructuredMatrix{}
	case "ScalarRuleMatrix":
		return &ScalarRuleMatrix{}
	case "PatternMatrix":
		return &PatternMatrix{}
	}
	return nil
}

// parityFlatten maps nested object errors to dotted paths.
func parityFlatten(errs ValidationErrors, prefix string, out map[string][]string) {
	for key, value := range errs {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := value.(ValidationErrors); ok {
			parityFlatten(nested, path, out)
			continue
		}
		var validators []string
		for _, fe := range errs.GetFieldErrors(key) {
			validators = append(validators, fe.Validator)
		}
		sort.Strings(validators)
		out[path] = validators
	}
}

func TestValidationParityDriver(t *testing.T) {
	vectorsPath := os.Getenv("PARITY_VECTORS")
	resultsPath := os.Getenv("PARITY_RESULTS")
	if vectorsPath == "" || resultsPath == "" {
		t.Skip("PARITY_VECTORS / PARITY_RESULTS not set")
	}
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vectors map[string]struct {
		Type    string          ` + "`json:\"type\"`" + `
		Payload json.RawMessage ` + "`json:\"payload\"`" + `
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	results := map[string]map[string][]string{}
	for name, vector := range vectors {
		value := newParityValue(vector.Type)
		if value == nil {
			t.Fatalf("vector %s: unknown type %s", name, vector.Type)
		}
		if err := json.Unmarshal(vector.Payload, value); err != nil {
			results[name] = map[string][]string{"$decode": {"rejected"}}
			continue
		}
		fields := map[string][]string{}
		parityFlatten(value.Validate(), "", fields)
		results[name] = fields
	}
	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatalf("marshal results: %v", err)
	}
	if err := os.WriteFile(resultsPath, out, 0o644); err != nil {
		t.Fatalf("write results: %v", err)
	}
}
`

const tsDriver = `import { readFileSync, writeFileSync } from 'node:fs';
import {
  validateJsonMatrix,
  validateListMatrix,
  validateParityMatrix,
  validatePatternMatrix,
  validateScalarRuleMatrix,
  validateStructuredMatrix,
} from './validators/types';

type Errors = { [key: string]: { validator: string }[] | Errors };

// flatten maps nested object errors to dotted paths.
function flatten(errors: Errors, prefix: string, out: Record<string, string[]>): void {
  for (const [key, value] of Object.entries(errors)) {
    const path = prefix ? prefix + '.' + key : key;
    if (Array.isArray(value)) {
      out[path] = value.map((e) => e.validator).sort();
    } else {
      flatten(value, path, out);
    }
  }
}

const validators: Record<string, (value: never) => true | Errors> = {
  ParityMatrix: validateParityMatrix as never,
  ListMatrix: validateListMatrix as never,
  JsonMatrix: validateJsonMatrix as never,
  StructuredMatrix: validateStructuredMatrix as never,
  ScalarRuleMatrix: validateScalarRuleMatrix as never,
  PatternMatrix: validatePatternMatrix as never,
};
const vectors = JSON.parse(readFileSync(process.env.PARITY_VECTORS as string, 'utf8'));
const results: Record<string, Record<string, string[]>> = {};
for (const [name, vector] of Object.entries(vectors) as [string, { type: string; payload: unknown }][]) {
  const res = validators[vector.type](vector.payload as never);
  const fields: Record<string, string[]> = {};
  if (res !== true) {
    flatten(res, '', fields);
  }
  results[name] = fields;
}
writeFileSync(process.env.PARITY_RESULTS as string, JSON.stringify(results, null, 2));
`

// rustDriver parses each payload as the type the vector names with the
// generated parse_<type>, which fills defaults, runs validate_<type> and
// decodes the validated value. A payload the validator refuses reports its
// errors; one it accepts that serde then refuses reports decodeRejected.
// TYPES_CRATE is replaced with the generated crate's name.
const rustDriver = `use std::collections::BTreeMap;

use serde_json::Value;
use TYPES_CRATE::validators;

type Verdicts = BTreeMap<String, Vec<String>>;

fn verdicts<T>(parsed: Result<T, superschematic_schema_runtime::ParseError>) -> Verdicts {
    use superschematic_schema_runtime::ParseError;
    let mut out = Verdicts::new();
    match parsed {
        Ok(_) => {}
        Err(ParseError::Invalid(errors)) => {
            for (path, errs) in errors.flatten() {
                let mut names: Vec<String> = errs.into_iter().map(|e| e.validator).collect();
                names.sort();
                out.insert(path, names);
            }
        }
        Err(ParseError::Decode(_)) => {
            out.insert("$decode".to_owned(), vec!["rejected".to_owned()]);
        }
        Err(other) => panic!("unexpected parse error: {other}"),
    }
    out
}

fn check(type_name: &str, payload: Value) -> Verdicts {
    use superschematic_schema_runtime::UnknownFields::Allow;
    match type_name {
        "ParityMatrix" => verdicts(validators::parse_parity_matrix(payload, Allow)),
        "ListMatrix" => verdicts(validators::parse_list_matrix(payload, Allow)),
        "JsonMatrix" => verdicts(validators::parse_json_matrix(payload, Allow)),
        "StructuredMatrix" => verdicts(validators::parse_structured_matrix(payload, Allow)),
        "ScalarRuleMatrix" => verdicts(validators::parse_scalar_rule_matrix(payload, Allow)),
        "PatternMatrix" => verdicts(validators::parse_pattern_matrix(payload, Allow)),
        other => panic!("unknown type {other}"),
    }
}

#[test]
fn validation_parity_driver() {
    let (Ok(vectors_path), Ok(results_path)) = (std::env::var("PARITY_VECTORS"), std::env::var("PARITY_RESULTS")) else {
        return;
    };
    let vectors: BTreeMap<String, Value> =
        serde_json::from_str(&std::fs::read_to_string(vectors_path).unwrap()).unwrap();
    let mut results = BTreeMap::new();
    for (name, vector) in vectors {
        let type_name = vector["type"].as_str().unwrap().to_owned();
        results.insert(name, check(&type_name, vector["payload"].clone()));
    }
    std::fs::write(results_path, serde_json::to_string_pretty(&results).unwrap()).unwrap();
}
`

// pyDriver decodes via model_fields alias mapping + model_construct so
// validate_all sees the payload without pydantic's own decode validation in
// the way (mirrors how Go and TS drive their validators directly). A vector
// whose decode the Python model must refuse runs the strict parse instead
// and reports decodeRejected when it raises. Error keys come back as python
// attribute names (opt_list, opt_list[0]); the driver maps them to wire
// names so the comparison is about verdicts, not each language's
// field-name idiom.
func pyDriver(outDir, moduleName string) string {
	return fmt.Sprintf(`
import importlib
import json
import os
import re
import sys

from pydantic import ValidationError as PydanticValidationError

sys.path.insert(0, %q)
mod = importlib.import_module(%q)


def to_wire(model, key):
    aliases = {attr: field.alias or attr for attr, field in model.model_fields.items()}
    m = re.match(r"^([A-Za-z0-9_]+)(.*)$", key)
    if not m:
        return key
    return aliases.get(m.group(1), m.group(1)) + m.group(2)


with open(os.environ["PARITY_VECTORS"]) as f:
    vectors = json.load(f)

results = {}
for name, vector in vectors.items():
    model = getattr(mod, vector["type"])
    payload = vector["payload"]
    if vector.get("decode", {}).get("python"):
        try:
            model.model_validate(payload, strict=True)
        except PydanticValidationError:
            results[name] = {"$decode": ["rejected"]}
        else:
            results[name] = {"$decode": ["accepted"]}
        continue
    data = {}
    for attr, field in model.model_fields.items():
        key = field.alias or attr
        if key in payload:
            data[attr] = payload[key]
        elif field.is_required():
            # model_construct bypasses Pydantic presence validation. Supply
            # its missing value explicitly so validate_all can report required.
            data[attr] = None
    m = model.model_construct(**data)
    errs = m.validate_all()
    fields = {}
    for field_name, entries in errs.errors.items():
        fields[to_wire(model, field_name)] = sorted(e["validator"] for e in entries)
    results[name] = fields

with open(os.environ["PARITY_RESULTS"], "w") as f:
    json.dump(results, f)
`, outDir, moduleName)
}

func TestGeneratedValidatorParity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-runtime parity check in -short mode")
	}

	serviceDir := stageParityService(t)
	schema, err := loader.LoadService(serviceDir)
	if err != nil {
		t.Fatalf("load parity fixture: %v", err)
	}

	sharedDir := t.TempDir()
	vectorsPath := writeVectorsFile(t, sharedDir)
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	t.Run("go", func(t *testing.T) {
		paths := testpaths.Local(t)

		output, err := typegen.Generate(schema, typegen.Options{
			SchemaName: "parity-fixture",
			ModulePath: "example.com/schemas/types/go/parity-fixture",
			Clock:      fixedClock,
		})
		if err != nil {
			t.Fatalf("generate go: %v", err)
		}
		// Resolve symlinks (macOS /var -> /private/var) so the relative
		// replace path computed against the temp dir resolves at build time.
		tempRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve temp dir: %v", err)
		}
		outDir := filepath.Join(tempRoot, "parity-fixture")
		if err := typegen.SetReplacePaths(output, paths, outDir); err != nil {
			t.Fatalf("set replace paths: %v", err)
		}
		if err := typegen.WriteTypes(output, outDir); err != nil {
			t.Fatalf("write go types: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "parity_driver_test.go"), []byte(goDriverTest), 0o644); err != nil {
			t.Fatalf("write go driver: %v", err)
		}

		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = outDir
		if out, err := tidy.CombinedOutput(); err != nil {
			t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
		}

		resultsPath := filepath.Join(sharedDir, "results-go.json")
		run := exec.Command("go", "test", "-count=1", "-run", "TestValidationParityDriver", "./...")
		run.Dir = outDir
		run.Env = append(os.Environ(), "PARITY_VECTORS="+vectorsPath, "PARITY_RESULTS="+resultsPath)
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("go driver failed: %v\n%s", err, out)
		}
		assertVerdicts(t, "go", readResults(t, resultsPath))
	})

	t.Run("typescript", func(t *testing.T) {
		bunPath, err := exec.LookPath("bun")
		if err != nil {
			testpaths.RequireOrSkipTS(t, "bun not available for the TypeScript parity check")
		}
		paths := testpaths.Local(t)

		output, err := tsgen.Generate(schema, tsgen.Options{
			SchemaName: "parity-fixture",
			Clock:      fixedClock,
		})
		if err != nil {
			t.Fatalf("generate typescript: %v", err)
		}
		tempRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve temp dir: %v", err)
		}
		outDir := filepath.Join(tempRoot, "parity-fixture")
		if err := tsgen.SetScalarLibSpec(output, paths, outDir); err != nil {
			t.Fatalf("set superscalar spec: %v", err)
		}
		if err := tsgen.WriteTypes(output, outDir); err != nil {
			t.Fatalf("write typescript types: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "parity_driver.ts"), []byte(tsDriver), 0o644); err != nil {
			t.Fatalf("write ts driver: %v", err)
		}

		install := exec.Command(bunPath, "install")
		install.Dir = outDir
		if out, err := install.CombinedOutput(); err != nil {
			testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed (likely offline): %v\n%s", err, out))
		}

		resultsPath := filepath.Join(sharedDir, "results-ts.json")
		run := exec.Command(bunPath, "run", "parity_driver.ts")
		run.Dir = outDir
		run.Env = append(os.Environ(), "PARITY_VECTORS="+vectorsPath, "PARITY_RESULTS="+resultsPath)
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("typescript driver failed: %v\n%s", err, out)
		}
		assertVerdicts(t, "typescript", readResults(t, resultsPath))
	})

	t.Run("rust", func(t *testing.T) {
		cargoPath, err := exec.LookPath("cargo")
		if err != nil {
			t.Skip("cargo not available; skipping Rust parity check")
		}
		paths := testpaths.Local(t)

		output, err := rustgen.Generate(schema, rustgen.Options{
			SchemaName: "parity-fixture",
			Clock:      fixedClock,
		})
		if err != nil {
			t.Fatalf("generate rust: %v", err)
		}
		tempRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve temp dir: %v", err)
		}
		outDir := filepath.Join(tempRoot, "parity-fixture")
		if err := rustgen.SetLocalPaths(output, paths, outDir); err != nil {
			t.Fatalf("set local paths: %v", err)
		}
		if err := rustgen.WriteTypes(output, outDir); err != nil {
			t.Fatalf("write rust types: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(outDir, "tests"), 0o755); err != nil {
			t.Fatalf("mkdir tests: %v", err)
		}
		driver := strings.ReplaceAll(rustDriver, "TYPES_CRATE", strings.ReplaceAll(output.CrateName, "-", "_"))
		if err := os.WriteFile(filepath.Join(outDir, "tests", "parity_driver.rs"), []byte(driver), 0o644); err != nil {
			t.Fatalf("write rust driver: %v", err)
		}

		targetDir := os.Getenv("CARGO_TARGET_DIR")
		if targetDir == "" {
			targetDir = filepath.Join(tempRoot, "target")
		}
		resultsPath := filepath.Join(sharedDir, "results-rust.json")
		run := exec.Command(cargoPath, "test", "--quiet", "--test", "parity_driver")
		run.Dir = outDir
		run.Env = append(os.Environ(), "CARGO_TARGET_DIR="+targetDir, "PARITY_VECTORS="+vectorsPath, "PARITY_RESULTS="+resultsPath)
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("rust driver failed: %v\n%s", err, out)
		}
		assertVerdicts(t, "rust", readResults(t, resultsPath))
	})

	t.Run("python", func(t *testing.T) {
		pythonPath, err := exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 not available; skipping Python parity check")
		}
		probe := exec.Command(pythonPath, "-c", "import pydantic")
		if err := probe.Run(); err != nil {
			t.Skip("pydantic not available; skipping Python parity check")
		}

		output, err := pygen.Generate(schema, pygen.Options{
			SchemaName: "parity-fixture",
			Clock:      fixedClock,
		})
		if err != nil {
			t.Fatalf("generate python: %v", err)
		}
		outDir := filepath.Join(t.TempDir(), "parity-fixture")
		if err := pygen.WriteTypes(output, outDir); err != nil {
			t.Fatalf("write python types: %v", err)
		}

		resultsPath := filepath.Join(sharedDir, "results-py.json")
		run := exec.Command(pythonPath, "-c", pyDriver(outDir, output.PythonModuleName))
		run.Env = append(os.Environ(), "PARITY_VECTORS="+vectorsPath, "PARITY_RESULTS="+resultsPath)
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("python driver failed: %v\n%s", err, out)
		}
		assertVerdicts(t, "python", readResults(t, resultsPath))
	})
}
