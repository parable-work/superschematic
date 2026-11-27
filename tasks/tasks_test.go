package tasks

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const testImage = "sha256:6a1f0c5d2b7e4f8093a1c6d5e4b3a29187f6e5d4c3b2a1908f7e6d5c4b3a2918"

func testFile() *File {
	return &File{
		Version:  FileVersion,
		Image:    testImage,
		Platform: "linux/amd64",
		Tasks: map[string]Task{
			"build": {
				Run:      []string{"make", "build"},
				Inputs:   []Input{{Kind: InputPath, Spec: json.RawMessage(`{"paths":["src"]}`)}},
				Outputs:  []Output{{Name: "bin", Kind: OutputPaths, Paths: []string{"bin"}}},
				Hermetic: true,
			},
			"test": {
				Run:      []string{"make", "test"},
				Uses:     []Use{{Task: "build", Output: "bin"}},
				After:    []string{"build"},
				Services: []Service{{Name: "postgres", Image: "postgres@" + testImage}},
				Env:      map[string]string{"B": "2", "A": "<1>"},
				Hermetic: true,
				Sections: map[string]json.RawMessage{"forge": json.RawMessage(`{"tier":"pr","b":1,"a":2}`)},
			},
		},
		Sections: map[string]json.RawMessage{"forge": json.RawMessage(`{"tiers":["pr"]}`)},
	}
}

// TestTheFileIsWrittenOneWay: keys sorted at every depth, a section's
// among them, a two-space indent, no HTML escaping and a trailing newline,
// so the same file gives the same bytes on every machine.
func TestTheFileIsWrittenOneWay(t *testing.T) {
	data, err := testFile().Encode()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasSuffix(text, "}\n") || strings.HasSuffix(text, "\n\n") {
		t.Errorf("the file ends in one newline, got %q", text[len(text)-4:])
	}
	for _, want := range []string{
		"{\n  \"image\": ",
		"\"A\": \"<1>\",\n        \"B\": \"2\"",
		"\"a\": 2,\n          \"b\": 1,\n          \"tier\": \"pr\"",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the file does not hold %q:\n%s", want, text)
		}
	}
	keys := regexpKeys(text)
	if !sort.StringsAreSorted(keys) {
		t.Errorf("the top-level keys are not sorted: %v", keys)
	}
	file, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := file.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != text {
		t.Errorf("a decoded file encodes to other bytes:\n%s\n%s", text, again)
	}
}

func regexpKeys(text string) []string {
	var keys []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "  \"") {
			keys = append(keys, strings.SplitN(strings.TrimPrefix(line, "  \""), "\"", 2)[0])
		}
	}
	return keys
}

// TestAReaderRefusesWhatItDoesNotKnow: a field this version does not hold
// and a version newer than it reads are refused, and named. The three
// open places take anything.
func TestAReaderRefusesWhatItDoesNotKnow(t *testing.T) {
	valid, err := testFile().Encode()
	if err != nil {
		t.Fatal(err)
	}
	edit := func(change func(doc map[string]any)) []byte {
		var doc map[string]any
		if err := json.Unmarshal(valid, &doc); err != nil {
			t.Fatal(err)
		}
		change(doc)
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	task := func(doc map[string]any, name string) map[string]any {
		return doc["tasks"].(map[string]any)[name].(map[string]any)
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		want   string
	}{
		{"a newer version", func(d map[string]any) { d["version"] = 2 }, "task file has version 2, and this reader reads up to version 1: update the reader before the file"},
		{"no version", func(d map[string]any) { delete(d, "version") }, "task file has no version"},
		{"an unknown field of the file", func(d map[string]any) { d["triggers"] = []any{} }, `unknown field "triggers"`},
		{"an unknown field of a task", func(d map[string]any) { task(d, "build")["tier"] = "pr" }, `unknown field "tier"`},
		{"an unknown field of an input", func(d map[string]any) {
			task(d, "build")["inputs"].([]any)[0].(map[string]any)["glob"] = "src/**"
		}, `unknown field "glob"`},
		{"a task with no command", func(d map[string]any) { task(d, "build")["run"] = []any{} }, "run"},
		{"a task that does not say whether it is hermetic", func(d map[string]any) { delete(task(d, "build"), "hermetic") }, "hermetic"},
		{"an image by tag", func(d map[string]any) { d["image"] = "ubuntu:24.04" }, "image"},
		{"a service container by tag", func(d map[string]any) {
			task(d, "test")["services"].([]any)[0].(map[string]any)["image"] = "postgres:16"
		}, "image"},
		{"a task name that is no name", func(d map[string]any) { d["tasks"].(map[string]any)["Build It"] = task(d, "build") }, "Build It"},
	} {
		_, err := Decode(edit(tc.change))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error holding %q, got %v", tc.name, tc.want, err)
		}
	}
	open := edit(func(d map[string]any) {
		d["sections"].(map[string]any)["another-forge"] = map[string]any{"anything": []any{1, "two"}}
		task(d, "build")["sections"] = map[string]any{"another-forge": map[string]any{"deep": map[string]any{"er": true}}}
		task(d, "build")["inputs"] = append(task(d, "build")["inputs"].([]any), map[string]any{"kind": "go-module", "spec": map[string]any{"dir": "go", "tags": []any{"x"}}})
	})
	if _, err := Decode(open); err != nil {
		t.Errorf("an input's spec, a section and a task's part of a section are open: %v", err)
	}
}

// TestTheWorkHashCoversTheWorkFields: after and the sections are outside
// it, and the image and the platform in effect are inside it.
func TestTheWorkHashCoversTheWorkFields(t *testing.T) {
	hash := func(change func(*File)) string {
		file := testFile()
		change(file)
		h, err := file.WorkHash("test")
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	set := func(change func(*Task)) func(*File) {
		return func(f *File) {
			task := f.Tasks["test"]
			change(&task)
			f.Tasks["test"] = task
		}
	}
	base := hash(func(*File) {})
	for name, change := range map[string]func(*File){
		"after":           set(func(t *Task) { t.After = nil }),
		"a task section":  set(func(t *Task) { t.Sections = nil }),
		"a file section":  func(f *File) { f.Sections = nil },
		"another task":    func(f *File) { f.Tasks["lint"] = Task{Run: []string{"lint"}} },
		"its own default": set(func(t *Task) { t.Image = testImage }),
	} {
		if hash(change) != base {
			t.Errorf("%s changed the work hash", name)
		}
	}
	for name, change := range map[string]func(*File){
		"run":                 set(func(t *Task) { t.Run = []string{"make", "check"} }),
		"uses":                set(func(t *Task) { t.Uses = nil }),
		"env":                 set(func(t *Task) { t.Env["A"] = "2" }),
		"services":            set(func(t *Task) { t.Services = nil }),
		"hermetic":            set(func(t *Task) { t.Hermetic = false }),
		"outputs":             set(func(t *Task) { t.Outputs = []Output{{Name: "report", Kind: OutputValue}} }),
		"inputs":              set(func(t *Task) { t.Inputs = []Input{{Kind: InputPath, Spec: json.RawMessage(`{"paths":["go"]}`)}} }),
		"the file's image":    func(f *File) { f.Image = strings.Replace(testImage, "6a", "7b", 1) },
		"the file's platform": func(f *File) { f.Platform = "linux/arm64" },
		"its own platform":    set(func(t *Task) { t.Platform = "linux/arm64" }),
	} {
		if hash(change) == base {
			t.Errorf("%s did not change the work hash", name)
		}
	}
	// A spec's key order is not its content.
	a := hash(set(func(t *Task) { t.Inputs = []Input{{Kind: "x", Spec: json.RawMessage(`{"a":1,"b":2}`)}} }))
	b := hash(set(func(t *Task) { t.Inputs = []Input{{Kind: "x", Spec: json.RawMessage(`{ "b": 2, "a": 1 }`)}} }))
	if a != b {
		t.Error("the order of a spec's keys changed the work hash")
	}
	if _, err := testFile().WorkHash("deploy"); err == nil {
		t.Error("a work hash of no task")
	}
}

func TestCanonical(t *testing.T) {
	got, err := Canonical(map[string]any{"b": []any{1, "<&>"}, "a": json.RawMessage(`{"z": 1.50, "y": null}`)})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":{"y":null,"z":1.50},"b":[1,"<&>"]}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestTheSchemasHoldTheTypes: each JSON Schema names exactly the fields
// its Go type has, so the published schema and the type a Go reader
// decodes into cannot drift apart.
func TestTheSchemasHoldTheTypes(t *testing.T) {
	for _, tc := range []struct {
		schema []byte
		path   []string
		typ    reflect.Type
	}{
		{fileSchema, nil, reflect.TypeOf(File{})},
		{fileSchema, []string{"$defs", "task"}, reflect.TypeOf(Task{})},
		{fileSchema, []string{"$defs", "input"}, reflect.TypeOf(Input{})},
		{fileSchema, []string{"$defs", "use"}, reflect.TypeOf(Use{})},
		{fileSchema, []string{"$defs", "output"}, reflect.TypeOf(Output{})},
		{fileSchema, []string{"$defs", "service"}, reflect.TypeOf(Service{})},
		{planSchema, nil, reflect.TypeOf(Plan{})},
		{planSchema, []string{"$defs", "task"}, reflect.TypeOf(PlanTask{})},
		{planSchema, []string{"$defs", "task", "properties", "inputs", "items"}, reflect.TypeOf(PlanInput{})},
		{planSchema, []string{"$defs", "task", "properties", "uses", "items"}, reflect.TypeOf(Use{})},
	} {
		var node map[string]any
		if err := json.Unmarshal(tc.schema, &node); err != nil {
			t.Fatal(err)
		}
		for _, key := range tc.path {
			node = node[key].(map[string]any)
		}
		if node["additionalProperties"] != false {
			t.Errorf("%s at %v admits fields it does not name", tc.typ, tc.path)
		}
		var inSchema, inType, required, mustHave []string
		for name := range node["properties"].(map[string]any) {
			inSchema = append(inSchema, name)
		}
		for _, name := range node["required"].([]any) {
			required = append(required, name.(string))
		}
		for i := 0; i < tc.typ.NumField(); i++ {
			name, options, _ := strings.Cut(tc.typ.Field(i).Tag.Get("json"), ",")
			inType = append(inType, name)
			if options != "omitempty" {
				mustHave = append(mustHave, name)
			}
		}
		for _, list := range [][]string{inSchema, inType, required, mustHave} {
			sort.Strings(list)
		}
		if !reflect.DeepEqual(inSchema, inType) {
			t.Errorf("%s at %v: the schema names %v and the type has %v", tc.typ, tc.path, inSchema, inType)
		}
		if !reflect.DeepEqual(required, mustHave) {
			t.Errorf("%s at %v: the schema requires %v and the type always writes %v", tc.typ, tc.path, required, mustHave)
		}
	}
}

func TestAPlanIsReadAsAFileIs(t *testing.T) {
	key := strings.Repeat("ab", 32)
	plan := &Plan{
		Version: PlanVersion, KeyVersion: "1:sha1", Commit: strings.Repeat("1", 40), Order: []string{"build", "smoke"},
		Tasks: map[string]PlanTask{
			"build": {WorkHash: key, Key: &key, Inputs: []PlanInput{{Kind: InputPath, Digest: key, Files: 3}}},
			"smoke": {WorkHash: key, After: []string{"build"}},
		},
	}
	data, err := plan.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\"key\": null") {
		t.Errorf("a task with no key says so:\n%s", data)
	}
	got, err := DecodePlan(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks["smoke"].Key != nil || *got.Tasks["build"].Key != key {
		t.Errorf("keys: %+v", got.Tasks)
	}
	newer := strings.Replace(string(data), "\"version\": 1", "\"version\": 3", 1)
	if _, err := DecodePlan([]byte(newer)); err == nil || !strings.Contains(err.Error(), "plan has version 3") {
		t.Errorf("a newer plan: %v", err)
	}
	unknown := strings.Replace(string(data), "\"order\"", "\"reuse\": {}, \"order\"", 1)
	if _, err := DecodePlan([]byte(unknown)); err == nil || !strings.Contains(err.Error(), `unknown field "reuse"`) {
		t.Errorf("an unknown field of a plan: %v", err)
	}
}

func TestTheSchemasAreTheirOwnCopies(t *testing.T) {
	a, b := FileSchema(), FileSchema()
	a[0] = 'x'
	if b[0] == 'x' || PlanSchema()[0] != '{' {
		t.Error("a caller's edit reached the package's schema")
	}
}
