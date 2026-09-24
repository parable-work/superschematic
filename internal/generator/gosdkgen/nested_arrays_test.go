package gosdkgen

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

const (
	nestedArraysService     = "fixture-nested-arrays-api"
	nestedArraysTypesModule = "example.com/schemas/types/go/fixture-nested-arrays-api"
	nestedArraysSDKModule   = "example.com/schemas/sdk/go/fixture-nested-arrays-api"
)

var nestedArraysClock = codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

// loadNestedArraysAPI loads fixture-nested-arrays-api: an input type, a
// PUT body argument and a bare response that are arrays of arrays. With
// withPaint it adds grid.paint (sdktest.AddPaintOperation).
func loadNestedArraysAPI(t *testing.T, withPaint bool) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, nestedArraysService))
	if err != nil {
		t.Fatalf("load %s: %v", nestedArraysService, err)
	}
	if withPaint {
		if err := sdktest.AddPaintOperation(schema); err != nil {
			t.Fatal(err)
		}
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  nestedArraysService,
		ModulePath:  "example.com/schemas/api/fixture-nested-arrays-api",
		TypesModule: nestedArraysTypesModule,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// writeNestedArraysSDK writes the Go SDK of fixture-nested-arrays-api to
// sdkDir against the types module in typesDir.
func writeNestedArraysSDK(t *testing.T, apiOutput *apigen.APIOutput, sdkDir, typesDir string) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, nestedArraysSDKModule, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return sdkOutput
}

// TestWriteSDKGoldenNestedArrays pins every file of the Go SDK for
// fixture-nested-arrays-api: [][]T arguments and responses, the inner-list
// and element checks of a list-of-lists argument, and tool documents whose
// schemas nest items. Regenerate with:
// go test ./internal/generator/gosdkgen -run TestWriteSDKGoldenNestedArrays -update
func TestWriteSDKGoldenNestedArrays(t *testing.T) {
	_, apiOutput := loadNestedArraysAPI(t, false)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", nestedArraysService)
	typesDir := filepath.Join(root, "types", "go", nestedArraysService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNestedArraysSDK(t, apiOutput, sdkDir, typesDir)
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", nestedArraysService))
}

// compareGoldenTree compares every file under outDir with the file at the
// same path under goldenDir, and fails on a golden file the output no
// longer writes. With -update it rewrites goldenDir from outDir.
func compareGoldenTree(t *testing.T, outDir, goldenDir string) {
	t.Helper()
	got := listFiles(t, outDir)
	if *update {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatal(err)
		}
		for _, name := range got {
			data, err := os.ReadFile(filepath.Join(outDir, name))
			if err != nil {
				t.Fatal(err)
			}
			goldenPath := filepath.Join(goldenDir, name)
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	want := listFiles(t, goldenDir)
	if len(want) == 0 {
		t.Fatalf("no golden files under %s (run with -update)", goldenDir)
	}
	wantSet := map[string]bool{}
	for _, name := range want {
		wantSet[name] = true
	}
	for _, name := range got {
		if !wantSet[name] {
			t.Errorf("%s has no golden file (run with -update)", name)
			continue
		}
		delete(wantSet, name)
		gotData, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		wantData, err := os.ReadFile(filepath.Join(goldenDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(gotData) != string(wantData) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
	for name := range wantSet {
		t.Errorf("golden %s is no longer written", name)
	}
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return filepath.SkipDir
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}
