package typegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestGeneratedDurationsReadTheirText builds a module whose types hold
// Temporal.Duration in every field shape, and an enum, against the real
// superscalar Go binding, and runs a test inside it. A duration's text is
// read as every validator reads it (D31, amended): a text the scalar's own
// rules and core accept decodes to the core's canonical value, and one they
// refuse decodes to no value and is reported by Validate at its path, by
// the rule it breaks, where Go's own duration parser would read "+1s" and
// refuse " 1s". An enum's decoder refuses a string the enum lacks.
func TestGeneratedDurationsReadTheirText(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module build in -short mode")
	}
	const service = "duration-text"
	dir := filepath.Join(t.TempDir(), service)
	field := func(name, typeName string, required bool, shape map[string]any) map[string]any {
		typeRef := map[string]any{"name": typeName}
		for key, value := range shape {
			typeRef[key] = value
		}
		return map[string]any{"name": name, "typeRef": typeRef, "required": required}
	}
	durationFields := []any{
		field("wait", "Temporal.Duration", true, nil),
		field("delay", "Temporal.Duration", false, nil),
		field("steps", "Temporal.Duration", false, map[string]any{"isArray": true}),
		field("grid", "Temporal.Duration", false, map[string]any{"isArray": true, "isArrayOfArrays": true}),
		field("named", "Temporal.Duration", false, map[string]any{"isMap": true}),
		field("stage", "Stage", false, nil),
		field("stages", "Stage", false, map[string]any{"isArray": true}),
	}
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    service,
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", "timers.schema.json"): map[string]any{
			"scalars": map[string]any{
				"Temporal.Duration": map[string]any{"name": "Temporal.Duration", "languagePrimitive": "string"},
			},
			"enums": map[string]any{
				"Stage": map[string]any{"name": "Stage", "values": []any{
					map[string]any{"name": "Draft", "serializedAs": "draft"},
					map[string]any{"name": "Done", "serializedAs": "done"},
				}},
			},
			"types": map[string]any{
				"Timer":      map[string]any{"name": "Timer", "role": "EmbeddedStruct", "fields": durationFields},
				"TimerInput": map[string]any{"name": "TimerInput", "role": "APIInput", "fields": []any{field("delay", "Temporal.Duration", false, nil)}},
			},
		},
	}
	for rel, doc := range files {
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	output, err := Generate(schema, Options{
		SchemaName: service,
		ModulePath: "example.com/schemas/types/go/" + service,
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), service)
	if err := SetReplacePaths(output, testpaths.Local(t), outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	code := strings.Replace(durationTextModuleTest, "PACKAGE", output.PackageName, 1)
	if err := os.WriteFile(filepath.Join(outDir, "duration_text_test.go"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = outDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	for _, args := range [][]string{{"vet", "."}, {"test", "-count=1", "."}} {
		run := exec.Command("go", args...)
		run.Dir = outDir
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated duration module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// durationTextModuleTest runs inside the generated module; PACKAGE is its
// package name.
const durationTextModuleTest = `package PACKAGE

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// flatten lists Validate's validator names by dotted path.
func flatten(prefix string, errs ValidationErrors, into map[string][]string) {
	for key, value := range errs {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch v := value.(type) {
		case []ValidationError:
			for _, e := range v {
				into[path] = append(into[path], e.Validator)
			}
			sort.Strings(into[path])
		case ValidationErrors:
			flatten(path, v, into)
		}
	}
}

func verdicts(t *testing.T, payload string) map[string][]string {
	t.Helper()
	var timer Timer
	if err := json.Unmarshal([]byte(payload), &timer); err != nil {
		t.Fatalf("decode %s: %v", payload, err)
	}
	got := map[string][]string{}
	flatten("", timer.Validate(), got)
	return got
}

func TestDurationTexts(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    map[string][]string
	}{
		{"every shape valid", ` + "`" + `{"wait": "1h30m", "delay": "1.5ns", "steps": ["1µs", "500ms"], "grid": [["1s"], []], "named": {"a": "2m"}}` + "`" + `, map[string][]string{}},
		{"a sign, which the pattern refuses", ` + "`" + `{"wait": "+1s"}` + "`" + `, map[string][]string{"wait": {"pattern"}}},
		{"space around, which the pattern refuses", ` + "`" + `{"wait": " 1s"}` + "`" + `, map[string][]string{"wait": {"pattern"}}},
		{"negative", ` + "`" + `{"wait": "1s", "delay": "-1s"}` + "`" + `, map[string][]string{"delay": {"pattern"}}},
		{"no unit", ` + "`" + `{"wait": "0"}` + "`" + `, map[string][]string{"wait": {"pattern"}}},
		{"too long", ` + "`" + `{"wait": "1h1h1h1h1h1h1h1h1h1h1h1h1h1h1h1h1h"}` + "`" + `, map[string][]string{"wait": {"maxLength"}}},
		{"beyond the core's range", ` + "`" + `{"wait": "99999999999999999999h"}` + "`" + `, map[string][]string{"wait": {"parse"}}},
		{"in a list, a list of lists and a map", ` + "`" + `{"wait": "1s", "steps": ["1s", "1 s"], "grid": [["1s", ".5s"]], "named": {"a": "1s", "b": "1d"}}` + "`" + `,
			map[string][]string{"steps[1]": {"pattern"}, "grid[0][1]": {"pattern"}, "named[b]": {"pattern"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := verdicts(t, tc.payload); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Validate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDurationsAreTheCoresValues(t *testing.T) {
	var timer Timer
	if err := json.Unmarshal([]byte(` + "`" + `{"wait": "90m", "delay": "1.5ns", "steps": ["1µs", "1500us"]}` + "`" + `), &timer); err != nil {
		t.Fatal(err)
	}
	if time.Duration(timer.Wait) != 90*time.Minute || timer.Delay == nil || time.Duration(*timer.Delay) != 2*time.Nanosecond {
		t.Fatalf("wait %v, delay %v; want 1h30m and the core's 2ns", time.Duration(timer.Wait), timer.Delay)
	}
	if time.Duration(timer.Steps[0]) != time.Microsecond || time.Duration(timer.Steps[1]) != 1500*time.Microsecond {
		t.Fatalf("steps %v", timer.Steps)
	}
	parsed, err := ParseTemporalDuration("1.5ns")
	if err != nil || time.Duration(parsed) != 2*time.Nanosecond {
		t.Fatalf("ParseTemporalDuration(1.5ns) = %v, %v; want the core's 2ns", time.Duration(parsed), err)
	}
}

func TestADurationOfAnotherJSONTypeIsRefused(t *testing.T) {
	var timer Timer
	if err := json.Unmarshal([]byte(` + "`" + `{"wait": 5}` + "`" + `), &timer); err == nil {
		t.Fatal("a number decoded as a duration")
	}
	// The rules and the core accept it, but a time.Duration cannot hold it.
	if err := json.Unmarshal([]byte(` + "`" + `{"wait": "2562047h1h"}` + "`" + `), &timer); err == nil {
		t.Fatal("a duration beyond a time.Duration decoded")
	}
}

func TestADurationReportFollowsTheField(t *testing.T) {
	var timer Timer
	if err := json.Unmarshal([]byte(` + "`" + `{"wait": "+1s"}` + "`" + `), &timer); err != nil {
		t.Fatal(err)
	}
	timer.Wait = TemporalDuration(time.Second)
	if errs := timer.Validate(); errs.HasErrors() {
		t.Fatalf("a field set after decoding is checked as set: %v", errs)
	}
	var input TimerInput
	if err := json.Unmarshal([]byte(` + "`" + `{"delay": "1 s"}` + "`" + `), &input); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	flatten("", input.Validate(), got)
	if want := map[string][]string{"delay": {"pattern"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("input Validate = %v, want %v", got, want)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{}` + "`" + `), &input); err != nil || input.Validate().HasErrors() {
		t.Fatalf("a later decode keeps an earlier one's report: %v, %v", err, input.Validate())
	}
}

func TestAnEnumRefusesAStringItLacks(t *testing.T) {
	var timer Timer
	err := json.Unmarshal([]byte(` + "`" + `{"wait": "1s", "stages": ["draft", "purple"]}` + "`" + `), &timer)
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Type != reflect.TypeFor[Stage]() || typeErr.Value != ` + "`" + `string "purple"` + "`" + ` || !strings.Contains(typeErr.Field, "stages") {
		t.Fatalf("decode of an unknown stage = %#v, want a *json.UnmarshalTypeError naming the field", err)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"wait": "1s", "stage": 3}` + "`" + `), &timer); !errors.As(err, &typeErr) || typeErr.Type != reflect.TypeFor[Stage]() || typeErr.Value != "number" {
		t.Fatalf("decode of a number stage = %#v", err)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"wait": "1s", "stage": "done", "stages": null}` + "`" + `), &timer); err != nil || timer.Stage == nil || *timer.Stage != Stage_Done {
		t.Fatalf("decode of a known stage = %v, %v", err, timer.Stage)
	}
	var stage Stage = Stage_Draft
	if err := json.Unmarshal([]byte("null"), &stage); err != nil || stage != Stage_Draft {
		t.Fatalf("null = %v, %q; it leaves the value as it was", err, stage)
	}
	if err := json.Unmarshal([]byte(` + "`" + `""` + "`" + `), &stage); err == nil {
		t.Fatal("an empty string decoded as a stage")
	}
}
`
