// This test runs inside the module typegen generates from
// nullElementsSchema (null_list_elements_test.go); it is not built here.

package types

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// decodeInto decodes payload into a new value of the named type.
func decodeInto(typeName, payload string) (any, error) {
	var value any
	switch typeName {
	case "Lists":
		value = &Lists{}
	case "ListsInput":
		value = &ListsInput{}
	case "StrictLists":
		value = &StrictLists{}
	default:
		return nil, fmt.Errorf("unknown type %s", typeName)
	}
	return value, json.Unmarshal([]byte(payload), value)
}

func TestNullListElementIsRefused(t *testing.T) {
	for _, tc := range []struct{ typeName, payload, want string }{
		{"Lists", `{"strs": ["a", null]}`, "decode Lists: strs[1]: null element"},
		{"Lists", `{"optStrs": [null]}`, "decode Lists: optStrs[0]: null element"},
		{"Lists", `{"nums": [1, null]}`, "decode Lists: nums[1]: null element"},
		{"Lists", `{"flags": [true, null]}`, "decode Lists: flags[1]: null element"},
		{"Lists", `{"names": [null]}`, "decode Lists: names[0]: null element"},
		{"Lists", `{"statuses": ["active", null]}`, "decode Lists: statuses[1]: null element"},
		{"Lists", `{"points": [{"x": 1}, null]}`, "decode Lists: points[1]: null element"},
		{"Lists", `{"payloads": [{"a": 1}, null]}`, "decode Lists: payloads[1]: null element"},
		{"Lists", `{"choices": [{"kind": "alpha", "value": "a"}, null]}`, "decode Lists: choices[1]: null element"},
		{"Lists", `{"grid": [["a"], ["b", null]]}`, "decode Lists: grid[1][1]: null element"},
		{"Lists", `{"numGrid": [[null]]}`, "decode Lists: numGrid[0][0]: null element"},
		{"Lists", `{"pointGrid": [[], [null]]}`, "decode Lists: pointGrid[1][0]: null element"},
		{"Lists", `{"choiceGrid": [[{"kind": "beta", "count": 1}, null]]}`, "decode Lists: choiceGrid[0][1]: null element"},
		// A nested object refuses a null element of its own lists.
		{"Lists", `{"points": [{"tags": ["a", null]}]}`, "decode Point: tags[1]: null element"},
		{"Lists", `{"pointGrid": [[{"tags": [null]}]]}`, "decode Point: tags[0]: null element"},
		// Keys match as encoding/json matches them: without regard to case,
		// and after unescaping.
		{"Lists", `{"STRS": ["a", null]}`, "decode Lists: strs[1]: null element"},
		{"Lists", `{"st\u0072s": [null]}`, "decode Lists: strs[0]: null element"},
		{"Lists", "{ \"strs\" :[ \"a\" ,\n\t null ] }", "decode Lists: strs[1]: null element"},
		// A repeated key is checked at every occurrence.
		{"Lists", `{"strs": [null], "strs": ["a"]}`, "decode Lists: strs[0]: null element"},
		{"ListsInput", `{"strs": [], "optStrs": ["a", null]}`, "decode ListsInput: optStrs[1]: null element"},
		{"ListsInput", `{"strs": [], "grid": [[null]]}`, "decode ListsInput: grid[0][0]: null element"},
		{"ListsInput", `{"strs": [], "choices": [null]}`, "decode ListsInput: choices[0]: null element"},
		{"StrictLists", `{"strs": ["a", null]}`, "decode StrictLists: strs[1]: null element"},
		{"StrictLists", `{"strs": [], "optStrs": [null]}`, "decode StrictLists: optStrs[0]: null element"},
	} {
		_, err := decodeInto(tc.typeName, tc.payload)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s %s: error = %v, want %q", tc.typeName, tc.payload, err, tc.want)
		}
	}
}

func TestNullOutsideListElementsDecodes(t *testing.T) {
	for _, tc := range []struct{ typeName, payload string }{
		{"Lists", `{}`},
		{"Lists", `{"strs": ["null", "a"], "note": "null"}`},
		{"Lists", `{"strs": ["a\"null"], "note": "[null]"}`},
		{"Lists", `{"strs": [], "note": null, "optStrs": null, "grid": null, "points": null}`},
		{"Lists", `{"grid": [["a"], null], "pointGrid": [null, []]}`},
		{"Lists", `{"points": [{"x": null, "tags": null}], "payloads": [{"a": null}, [null], 1, "null"]}`},
		{"Lists", `{"strsByKey": {"k": ["a", null]}, "reqStrsByKey": {"k": [null]}}`},
		{"Lists", `{"strs": [], "null": [null], "Note": null}`},
		{"Lists", `null`},
		{"ListsInput", `{"strs": [], "optStrs": null, "grid": null, "choices": null}`},
		{"StrictLists", `{"strs": ["a"], "optStrs": null}`},
	} {
		if _, err := decodeInto(tc.typeName, tc.payload); err != nil {
			t.Errorf("%s %s: %v", tc.typeName, tc.payload, err)
		}
	}
}

// TestUUIDElementRefusesNullItself: a UUID element's own decoder refuses a
// null before the list check sees it.
func TestUUIDElementRefusesNullItself(t *testing.T) {
	if _, err := decodeInto("Lists", `{"ids": ["7f9c24e8-3b12-4fef-91e0-3e0f6b2b1b3f", null]}`); err == nil || !strings.Contains(err.Error(), "UUID") {
		t.Fatalf("error = %v, want the UUID decoder's", err)
	}
}

// TestNullInnerListStillReachesValidate: a null inner list is not an
// element; it decodes to a nil list, which Validate reports.
func TestNullInnerListStillReachesValidate(t *testing.T) {
	var value Lists
	if err := json.Unmarshal([]byte(`{"strs": [], "grid": [["a"], null], "reqStrsByKey": {}}`), &value); err != nil {
		t.Fatal(err)
	}
	errs := value.Validate()
	if got := errs.GetFieldErrors("grid[1]"); len(got) != 1 || got[0].Validator != "required" {
		t.Fatalf("grid[1] errors = %v, want required", errs)
	}
}

// TestInputFieldKeepsNull: an input list that is itself null is still
// present and null.
func TestInputFieldKeepsNull(t *testing.T) {
	var value ListsInput
	if err := json.Unmarshal([]byte(`{"strs": [], "optStrs": null}`), &value); err != nil {
		t.Fatal(err)
	}
	if !value.OptStrs.IsNull() || value.Grid.IsSet() {
		t.Fatalf("optStrs = %+v, grid = %+v", value.OptStrs, value.Grid)
	}
}

// TestEveryDecodePathRefuses: FromJSON, FromMap and FromYAML all decode
// through UnmarshalJSON.
func TestEveryDecodePathRefuses(t *testing.T) {
	const want = "strs[1]: null element"
	if _, err := ListsFromJSONNonStrict([]byte(`{"strs": ["a", null]}`)); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("FromJSONNonStrict: %v", err)
	}
	if _, err := ListsFromMap(map[string]any{"strs": []any{"a", nil}}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("FromMap: %v", err)
	}
	if _, err := ListsFromYAMLNonStrict([]byte("strs:\n  - a\n  - null\n")); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("FromYAMLNonStrict: %v", err)
	}
	var request struct{ Body Lists }
	if err := json.Unmarshal([]byte(`{"Body": {"strs": ["a", null]}}`), &request); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("nested decode: %v", err)
	}
}

// referenceNullElement is what rejectNullListElements must find, worked out
// from a full decode: the first list field (in field order) that the
// payload's key names, exactly or else without regard to case, and that
// holds a null element.
func referenceNullElement(payload []byte, fields []jsonListField) (string, bool) {
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return "", false
	}
	for _, field := range fields {
		for key, value := range object {
			if !strings.EqualFold(key, field.name) {
				continue
			}
			list, _ := value.([]any)
			for _, elem := range list {
				if field.depth == 1 {
					if elem == nil {
						return field.name, true
					}
					continue
				}
				inner, _ := elem.([]any)
				for _, innerElem := range inner {
					if innerElem == nil {
						return field.name, true
					}
				}
			}
		}
	}
	return "", false
}

// randomJSON returns a random JSON value, favoring null, strings that hold
// "null" and nesting.
func randomJSON(r *rand.Rand, depth int) string {
	switch n := r.Intn(9); {
	case n == 0:
		return "null"
	case n == 1:
		return []string{`"null"`, `"a\"null\\"`, `"[null]"`, `"x"`, `""`}[r.Intn(5)]
	case n == 2:
		return []string{"0", "-1.5e3", "true", "false"}[r.Intn(4)]
	case n <= 5 && depth > 0:
		items := make([]string, r.Intn(4))
		for i := range items {
			items[i] = randomJSON(r, depth-1)
		}
		return "[" + strings.Join(items, randomSpace(r)+","+randomSpace(r)) + "]"
	case depth > 0:
		members := make([]string, r.Intn(3))
		for i := range members {
			members[i] = fmt.Sprintf("%q:%s", []string{"a", "null", "strs"}[r.Intn(3)]+fmt.Sprint(i), randomJSON(r, depth-1))
		}
		return "{" + strings.Join(members, ",") + "}"
	}
	return `"leaf"`
}

// randomList returns a random JSON list whose elements are random values
// or, below the top depth, random lists.
func randomList(r *rand.Rand, depth int) string {
	items := make([]string, r.Intn(5))
	for i := range items {
		if depth > 1 && r.Intn(2) == 0 {
			items[i] = randomList(r, depth-1)
		} else {
			items[i] = randomJSON(r, depth-1)
		}
	}
	return "[" + strings.Join(items, randomSpace(r)+","+randomSpace(r)) + "]"
}

func randomSpace(r *rand.Rand) string {
	return []string{"", " ", "\n\t"}[r.Intn(3)]
}

// TestScannerMatchesReference runs rejectNullListElements against the
// decode-based reference over random objects whose keys are list fields
// (in any case), other fields and unknown keys. Keys are not repeated: the
// scanner checks every occurrence, where a decode keeps the last.
func TestScannerMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	keys := []string{"strs", "STRS", "grid", "Grid", "note", "unknown", "null", "numGrid"}
	const iterations = 5000
	refused := 0
	for iteration := 0; iteration < iterations; iteration++ {
		picked := r.Perm(len(keys))[:r.Intn(4)]
		var members []string
		seen := map[string]bool{}
		for _, index := range picked {
			key := keys[index]
			if seen[strings.ToLower(key)] {
				continue
			}
			seen[strings.ToLower(key)] = true
			value := randomJSON(r, 3)
			if r.Intn(4) > 0 {
				value = randomList(r, 2)
			}
			members = append(members, fmt.Sprintf("%s%q%s:%s%s", randomSpace(r), key, randomSpace(r), randomSpace(r), value))
		}
		payload := []byte("{" + strings.Join(members, ",") + "}")
		if !json.Valid(payload) {
			t.Fatalf("generated invalid JSON %s", payload)
		}
		wantField, wantFound := referenceNullElement(payload, listFieldsOfLists)
		err := rejectNullListElements("Lists", payload, listFieldsOfLists)
		if wantFound {
			refused++
		}
		if (err != nil) != wantFound {
			t.Fatalf("payload %s: scanner error %v, reference found %v (%s)", payload, err, wantFound, wantField)
		}
		if wantFound && !strings.Contains(err.Error(), ": "+wantField+"[") {
			// Both found one; the scanner reports the first in payload
			// order, the reference the first in field order.
			if _, found := referenceNullElement(payload, []jsonListField{{name: fieldOf(err), depth: depthOf(fieldOf(err))}}); !found {
				t.Fatalf("payload %s: scanner reported %v", payload, err)
			}
		}
	}
	if refused < iterations/10 || refused > iterations*9/10 {
		t.Fatalf("%d of %d payloads had a null element; the generator no longer covers both outcomes", refused, iterations)
	}
}

// fieldOf returns the field an error from rejectNullListElements names.
func fieldOf(err error) string {
	rest := strings.TrimPrefix(err.Error(), "decode Lists: ")
	return rest[:strings.IndexByte(rest, '[')]
}

func depthOf(name string) int {
	for _, field := range listFieldsOfLists {
		if field.name == name {
			return field.depth
		}
	}
	return 0
}

// plainBatch is Batch without its UnmarshalJSON: a defined type drops the
// method, so json.Unmarshal fills the fields directly, which is all Batch's
// UnmarshalJSON did before it refused null elements. The benchmarks compare
// the two.
type plainBatch Batch

func benchmarkPayloads() []struct {
	name string
	data []byte
} {
	strs := make([]string, 10000)
	nums := make([]float64, 10000)
	for i := range strs {
		strs[i] = fmt.Sprintf("value-%05d", i)
		nums[i] = float64(i) * 1.5
	}
	samples := make([]map[string]any, 1000)
	for i := range samples {
		samples[i] = map[string]any{"x": float64(i), "label": strs[i]}
	}
	grid := make([][]string, 100)
	for i := range grid {
		grid[i] = strs[i*100 : (i+1)*100]
	}
	encode := func(value map[string]any) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		return data
	}
	// A payload without a null token is not scanned; one with a null
	// anywhere ("note": null) is.
	return []struct {
		name string
		data []byte
	}{
		{"strings10k", encode(map[string]any{"strs": strs})},
		{"strings10k_nullField", encode(map[string]any{"strs": strs, "note": nil})},
		{"numbers10k_nullField", encode(map[string]any{"nums": nums, "note": nil})},
		{"objects1k_nullField", encode(map[string]any{"samples": samples, "note": nil})},
		{"grid100x100_nullField", encode(map[string]any{"grid": grid, "note": nil})},
	}
}

func BenchmarkDecodeLargeList(b *testing.B) {
	for _, payload := range benchmarkPayloads() {
		b.Run(payload.name+"/before", func(b *testing.B) {
			b.SetBytes(int64(len(payload.data)))
			for b.Loop() {
				var value plainBatch
				if err := json.Unmarshal(payload.data, &value); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(payload.name+"/after", func(b *testing.B) {
			b.SetBytes(int64(len(payload.data)))
			for b.Loop() {
				var value Batch
				if err := json.Unmarshal(payload.data, &value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
