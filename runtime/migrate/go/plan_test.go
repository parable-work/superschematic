package migrate_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// TestReadPlanAcceptsAnyEncoding: the indented fixture and its canonical
// JSON read as the same plan.
func TestReadPlanAcceptsAnyEncoding(t *testing.T) {
	doc := fixture(t, migrate.SQLite, "02-evolve")
	canonical, err := migrate.CanonicalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	indented, compact := readPlan(t, doc), readPlan(t, canonical)
	if indented.Hash != compact.Hash || indented.To != compact.To {
		t.Fatalf("hash %s and %s, to %s and %s", indented.Hash, compact.Hash, indented.To, compact.To)
	}
	if string(indented.Model().Canonical) != string(compact.Model().Canonical) {
		t.Fatal("the two encodings read different models")
	}
	if len(indented.Steps) != 4 || !indented.Steps[1].ForeignKeysOff || indented.Steps[3].Phase != migrate.Contract {
		t.Fatalf("steps read as %+v", indented.Steps)
	}
}

// TestReadPlanRefuses: ReadPlan refuses, before anything runs, a plan of
// another version, a plan changed after it was sealed, a toModel that does
// not hash to to, an expandedModel that does not hash to expanded or is of
// another service, and steps the runner cannot run as written.
func TestReadPlanRefuses(t *testing.T) {
	sealed := func(edit func(plan map[string]any)) []byte {
		return sealDoc(t, fixture(t, migrate.SQLite, "02-evolve"), edit, true)
	}
	for _, c := range []struct {
		name   string
		doc    []byte
		refuse string
	}{
		{"an unknown version", sealed(func(p map[string]any) { p["version"] = 2 }), "version 2; this runner reads version 1"},
		{"no version", sealed(func(p map[string]any) { delete(p, "version") }), "no version"},
		{"an edited statement", func() []byte {
			var p map[string]any
			if err := json.Unmarshal(fixture(t, migrate.SQLite, "02-evolve"), &p); err != nil {
				t.Fatal(err)
			}
			steps(p)[0].(map[string]any)["statements"] = []any{"DROP TABLE customer"}
			doc, _ := json.Marshal(p)
			return doc
		}(), "changed after it was written"},
		{"no hash", func() []byte {
			var p map[string]any
			if err := json.Unmarshal(fixture(t, migrate.SQLite, "02-evolve"), &p); err != nil {
				t.Fatal(err)
			}
			delete(p, "hash")
			doc, _ := json.Marshal(p)
			return doc
		}(), "no hash"},
		{"a toModel that is not to", sealDoc(t, fixture(t, migrate.SQLite, "02-evolve"), func(p map[string]any) {
			p["toModel"].(map[string]any)["service"] = "other"
		}, false), "not to the plan's to"},
		{"no toModel", sealDoc(t, fixture(t, migrate.SQLite, "02-evolve"), func(p map[string]any) { delete(p, "toModel") }, false), "no toModel"},
		{"an expandedModel that is not expanded", sealDoc(t, fixture(t, migrate.SQLite, "02-evolve"), func(p map[string]any) {
			p["expandedModel"].(map[string]any)["tables"] = []any{}
		}, false), "the plan's expandedModel hashes to"},
		{"expanded with no expandedModel", sealed(func(p map[string]any) { delete(p, "expandedModel") }), "has expanded but no expandedModel"},
		{"an expandedModel with no expanded", sealDoc(t, fixture(t, migrate.SQLite, "02-evolve"), func(p map[string]any) { delete(p, "expanded") }, false), "has an expandedModel but no expanded"},
		{"an expandedModel of another service", sealed(func(p map[string]any) {
			p["expandedModel"].(map[string]any)["service"] = "other"
		}), "expandedModel is of sqlite service other"},
		{"an unknown dialect", sealed(func(p map[string]any) { p["dialect"] = "mysql" }), `dialect is "mysql"`},
		{"no service", sealed(func(p map[string]any) { p["service"] = "" }), "names no service"},
		{"a step out of order", sealed(func(p map[string]any) { steps(p)[1].(map[string]any)["index"] = 7 }), "step 2 has index 7"},
		{"an expand step after a contract step", sealed(func(p map[string]any) {
			steps(p)[3].(map[string]any)["phase"] = "expand"
			steps(p)[2].(map[string]any)["phase"] = "contract"
		}), "expand step after a contract step"},
		{"an unknown phase", sealed(func(p map[string]any) { steps(p)[0].(map[string]any)["phase"] = "later" }), `phase "later"`},
		{"a SQLite step outside a transaction", sealed(func(p map[string]any) { steps(p)[0].(map[string]any)["transactional"] = false }), "SQLite runs every step in a transaction"},
		{"a Postgres step with foreign keys off", sealDoc(t, fixture(t, migrate.Postgres, "02-evolve"), func(p map[string]any) {
			steps(p)[0].(map[string]any)["foreignKeysOff"] = true
		}, true), "only a SQLite step does"},
		{"a document that is not an object", []byte(`[]`), "a plan is a JSON object"},
		{"trailing data", append(fixture(t, migrate.SQLite, "02-evolve"), "{}"...), "trailing data"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := migrate.ReadPlan(c.doc)
			if err == nil || !strings.Contains(err.Error(), c.refuse) {
				t.Fatalf("ReadPlan = %v, want it refused with %q", err, c.refuse)
			}
		})
	}
}

// TestRefusalsAreErrRefused: a plan the runner will not run is ErrRefused,
// which the binary reports as exit code 1.
func TestRefusalsAreErrRefused(t *testing.T) {
	doc := sealDoc(t, fixture(t, migrate.SQLite, "01-create"), func(p map[string]any) { p["version"] = 2 }, true)
	if _, err := migrate.ReadPlan(doc); !errors.Is(err, migrate.ErrRefused) {
		t.Fatalf("ReadPlan = %v, want ErrRefused", err)
	}
}

// TestReadModel: a model's service and dialect key its state row; the hash
// is over its canonical JSON.
func TestReadModel(t *testing.T) {
	model, err := migrate.ReadModel([]byte(`{"version": 1, "service": "shop", "dialect": "sqlite", "tables": []}`))
	if err != nil {
		t.Fatal(err)
	}
	if model.Service != "shop" || model.Dialect != migrate.SQLite {
		t.Fatalf("read %+v", model)
	}
	if want := `{"dialect":"sqlite","service":"shop","tables":[],"version":1}`; string(model.Canonical) != want {
		t.Fatalf("canonical %s, want %s", model.Canonical, want)
	}
	if model.Hash != migrate.Hash(model.Canonical) {
		t.Fatal("the hash is not of the canonical JSON")
	}
	for doc, refuse := range map[string]string{
		`{"dialect": "sqlite"}`:                    "names no service",
		`{"service": "shop", "dialect": "oracle"}`: "not postgres or sqlite",
		`{"service": "shop"}`:                      "not postgres or sqlite",
		`[]`:                                       "read the model",
		`{"service": "shop", "dialect": "sqlite"} x`: "trailing data",
	} {
		if _, err := migrate.ReadModel([]byte(doc)); err == nil || !strings.Contains(err.Error(), refuse) {
			t.Errorf("ReadModel(%s) = %v, want %q", doc, err, refuse)
		}
	}
}
