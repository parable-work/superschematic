package rustgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestQueuesDescribeWhatTypeScriptWrites: for every queue of
// fixture-queue-db, whose message holds a field of every kind a column
// takes, and of fixture-queue-geo-db, whose message holds locations and
// durations, the descriptor queues.rs holds, read as JSON, is the
// definition queues.ts writes, read as data: the same members, by the same
// names, with the same values, the statements included (D65). So the Rust
// queue client reads what the TypeScript one reads, and through tsgen's
// TestQueuesWriteWhatTheGoORMWrites, what the Go ORM writes.
func TestQueuesDescribeWhatTypeScriptWrites(t *testing.T) {
	for _, fixture := range []string{"fixture-queue-db", "fixture-queue-geo-db"} {
		t.Run(fixture, func(t *testing.T) { testQueuesDescribeWhatTypeScriptWrites(t, fixture) })
	}
}

func testQueuesDescribeWhatTypeScriptWrites(t *testing.T, fixture string) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture))
	if err != nil {
		t.Fatal(err)
	}
	queues, err := RustQueues(schema, true)
	if err != nil {
		t.Fatal(err)
	}
	definitions := typeScriptDefinitions(t, schema)
	if len(queues) == 0 || len(queues) != len(definitions) {
		t.Fatalf("queues.rs describes %d queues and queues.ts defines %d", len(queues), len(definitions))
	}
	for _, q := range queues {
		if q.Unsupported != "" {
			t.Fatalf("queues.rs leaves out %s: %s", q.Name, q.Unsupported)
		}
		want, ok := definitions[q.Name]
		if !ok {
			t.Fatalf("queues.rs describes %s, which queues.ts does not define", q.Name)
		}
		if q.Const != strings.ToUpper(codegen.ToSnakeCase(q.Name))+"_QUEUE" {
			t.Errorf("queue %s is held in %s", q.Name, q.Const)
		}
		got := descriptorOf(t, q)
		if !reflect.DeepEqual(got, want) {
			gotText, _ := json.MarshalIndent(got, "", "  ")
			wantText, _ := json.MarshalIndent(want, "", "  ")
			t.Errorf("queue %s: queues.rs describes\n%s\nand queues.ts defines\n%s", q.Name, gotText, wantText)
		}
	}
}

// TestQueuesLeaveOutWhatTheClientCannotHold: a queue whose message holds a
// value the Rust queue client cannot write and read back yet, a list of
// Temporal.Duration values, which Postgres holds as an INTERVAL[], is left
// out of queues.rs with why, as queues.ts leaves it out, and the other
// queues are written.
func TestQueuesLeaveOutWhatTheClientCannotHold(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-queue-db"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Scalars["Temporal.Duration"] = &ir.ScalarDef{Name: "Temporal.Duration", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "INTERVAL"}}
	ping := schema.Types["Ping"]
	ping.Fields = append(ping.Fields, &ir.FieldDef{Name: "every", TypeRef: ir.TypeRef{Name: "Temporal.Duration", IsArray: true}})

	tsQueues, err := tsgen.TypeScriptQueues(schema, true)
	if err != nil {
		t.Fatal(err)
	}
	queues, err := RustQueues(schema, true)
	if err != nil {
		t.Fatal(err)
	}
	for i, q := range queues {
		if (q.Unsupported == "") != (tsQueues[i].Unsupported == "") {
			t.Errorf("queue %s: queues.rs leaves it out for %q, queues.ts for %q", q.Name, q.Unsupported, tsQueues[i].Unsupported)
		}
	}

	output, err := Generate(schema, Options{SchemaName: "fixture-queue-db", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(dir, "src", QueuesFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"//! Queue Ping is left out: field every is a list of Temporal.Duration values, an INTERVAL[] column, which the Rust queue client does not read yet.",
		"pub const ORDER_PLACED_QUEUE: &str = r#\"{",
	} {
		if !strings.Contains(string(text), want) {
			t.Errorf("queues.rs lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(string(text), "PING_QUEUE") {
		t.Errorf("queues.rs writes the queue it leaves out:\n%s", text)
	}
}

// TestQueuesOfOnlyLeftOutQueues: a crate whose every queue queues.rs
// leaves out declares the module, which says why, and re-exports nothing
// from it.
func TestQueuesOfOnlyLeftOutQueues(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-queue-db"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Scalars["Geo.Point"] = &ir.ScalarDef{Name: "Geo.Point", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "POINT"}}
	for _, td := range schema.Queues() {
		td.Fields = append(td.Fields, &ir.FieldDef{Name: "at", TypeRef: ir.TypeRef{Name: "Geo.Point", IsArray: true}})
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-queue-db", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	lib, err := os.ReadFile(filepath.Join(dir, "src", "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lib), "pub mod queues;") || strings.Contains(string(lib), "pub use queues::*;") {
		t.Errorf("lib.rs should declare queues and re-export nothing from it:\n%s", lib)
	}
	text, err := os.ReadFile(filepath.Join(dir, "src", QueuesFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := "//! Queue OrderPlaced is left out: field at is a list of Geo.Point values, a POINT[] column, which the Rust queue client does not read yet."; !strings.Contains(string(text), want) {
		t.Errorf("queues.rs lacks %q:\n%s", want, text)
	}
	if strings.Contains(string(text), "pub const") {
		t.Errorf("queues.rs writes a queue it leaves out:\n%s", text)
	}
}

// descriptorOf is the descriptor q's constant holds, read as JSON from its
// raw string literal.
func descriptorOf(t *testing.T, q QueueInfo) any {
	t.Helper()
	literal := strings.TrimLeft(strings.TrimPrefix(q.Literal, "r"), "#")
	literal = strings.TrimRight(literal, "#")
	literal = strings.TrimSuffix(strings.TrimPrefix(literal, `"`), `"`)
	var out any
	if err := json.Unmarshal([]byte(literal), &out); err != nil {
		t.Fatalf("queue %s: the constant holds no JSON: %v\n%s", q.Name, err, q.Literal)
	}
	return out
}

// definitionPattern matches a definition queues.ts exports: its message
// and its object literal, which ends at a closing brace of its own line.
var definitionPattern = regexp.MustCompile(`(?s)export const \w+: QueueDefinition<(\w+)> = (\{.*?\n\});`)

// typeScriptDefinitions are the definitions the queues.ts of schema's
// TypeScript types exports, by message, each read as data.
func typeScriptDefinitions(t *testing.T, schema *ir.Schema) map[string]any {
	t.Helper()
	output, err := tsgen.Generate(schema, tsgen.Options{SchemaName: "fixture-queue-db", SQLite: true, Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := tsgen.WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(dir, tsgen.QueuesFile))
	if err != nil {
		t.Fatal(err)
	}
	definitions := map[string]any{}
	for _, m := range definitionPattern.FindAllStringSubmatch(string(text), -1) {
		var definition any
		data := objectLiteralJSON(m[2])
		if err := json.Unmarshal(data, &definition); err != nil {
			t.Fatalf("the definition of %s in queues.ts is not data: %v\n%s", m[1], err, data)
		}
		definitions[m[1]] = definition
	}
	if len(definitions) == 0 {
		t.Fatalf("queues.ts defines no queue:\n%s", text)
	}
	return definitions
}

// objectLiteralJSON is the object literal queues.ts writes for a queue as
// JSON: its keys are bare identifiers, its strings JSON's or template
// literals, which hold no escape or substitution (tsgen refuses one that
// would), and its lists and objects may end in a comma.
func objectLiteralJSON(src string) []byte {
	ident := func(c byte) bool {
		return c == '_' || c == '$' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
	}
	var b strings.Builder
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '"':
			j := i + 1
			for ; j < len(src) && src[j] != '"'; j++ {
				if src[j] == '\\' {
					j++
				}
			}
			b.WriteString(src[i : j+1])
			i = j + 1
		case c == '`':
			j := i + 1 + strings.IndexByte(src[i+1:], '`')
			b.WriteString(strconv.Quote(src[i+1 : j]))
			i = j + 1
		case ident(c) && !('0' <= c && c <= '9'):
			j := i
			for j < len(src) && ident(src[j]) {
				j++
			}
			if word := src[i:j]; word == "true" || word == "false" || word == "null" {
				b.WriteString(word)
			} else {
				b.WriteString(strconv.Quote(word))
			}
			i = j
		case c == ',':
			k := i + 1
			for k < len(src) && strings.ContainsRune(" \t\r\n", rune(src[k])) {
				k++
			}
			if k == len(src) || src[k] != '}' && src[k] != ']' {
				b.WriteByte(c)
			}
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return []byte(b.String())
}
