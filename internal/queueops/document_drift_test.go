package queueops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The runner's queue document and result (runtime/migrate/go/queue.go) and
// this package's are one wire format declared twice: the runner is a module
// of its own that holds the database drivers, which stay out of the
// compiler (D27), and a runtime module does not import the compiler, so
// neither can import the other's types. This test holds the two to the same
// JSON members: it reads the runner's struct tags from its source, and this
// package's by reflection.

// runnerQueueSource is the runner's queue document, from this package's
// directory.
var runnerQueueSource = filepath.Join("..", "..", "runtime", "migrate", "go", "queue.go")

func TestDocumentMatchesTheRunners(t *testing.T) {
	runner := runnerStructs(t, runnerQueueSource)
	for _, pair := range []struct {
		ours   reflect.Type
		theirs string
	}{
		{reflect.TypeFor[document](), "QueueDocument"},
		{reflect.TypeFor[statements](), "QueueStatements"},
		{reflect.TypeFor[Result](), "QueueResult"},
		{reflect.TypeFor[DeadMessage](), "DeadMessage"},
	} {
		theirs, ok := runner[pair.theirs]
		if !ok {
			t.Fatalf("%s declares no struct %s", runnerQueueSource, pair.theirs)
		}
		if ours := reflectedMembers(pair.ours); !slices.Equal(ours, theirs) {
			t.Errorf("queueops.%s has the JSON members %v, and the runner's %s %v", pair.ours.Name(), ours, pair.theirs, theirs)
		}
	}
}

// reflectedMembers lists a struct's JSON members, each its key and, when the
// encoder omits it empty, ",omitempty", in declaration order.
func reflectedMembers(t reflect.Type) []string {
	var members []string
	for i := range t.NumField() {
		if member, ok := jsonMember(t.Field(i).Tag.Get("json")); ok {
			members = append(members, member)
		}
	}
	return members
}

// runnerStructs reads the JSON members of every struct type path declares
// at its top level, as reflectedMembers lists them.
func runnerStructs(t *testing.T, path string) map[string][]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	structs := map[string][]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec := spec.(*ast.TypeSpec)
			st, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			var members []string
			for _, field := range st.Fields.List {
				if field.Tag == nil {
					continue
				}
				tag, err := strconv.Unquote(field.Tag.Value)
				if err != nil {
					t.Fatalf("%s: %s: %v", path, typeSpec.Name.Name, err)
				}
				if member, ok := jsonMember(reflect.StructTag(tag).Get("json")); ok {
					for range field.Names {
						members = append(members, member)
					}
				}
			}
			structs[typeSpec.Name.Name] = members
		}
	}
	return structs
}

func jsonMember(tag string) (string, bool) {
	name, options, _ := strings.Cut(tag, ",")
	if name == "" || name == "-" {
		return "", false
	}
	if slices.Contains(strings.Split(options, ","), "omitempty") {
		return name + ",omitempty", true
	}
	return name, true
}
