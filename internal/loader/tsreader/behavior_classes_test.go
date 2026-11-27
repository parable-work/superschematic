package tsreader

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/registry"
)

// coreBehaviorsDir holds the core's declarations and the classes they are
// derived from (D103).
const coreBehaviorsDir = "../../registry/behaviors"

// crateDeclarationDirs are the directories `make behaviors` writes each
// package's canonical copies into, the files the core and the engine embed.
var crateDeclarationDirs = []string{
	"../../../runtime/engine/rust/behaviors/declarations",
	"../../../runtime/engine-workqueue/rust/declarations",
	"../../../runtime/engine-commerce/rust/declarations",
	"../../../runtime/engine/rust/rules-behaviors/declarations",
}

// TestCoreBehaviorClassesDeriveTheirCommittedDeclarations reads each core
// behavior's class (internal/registry/behaviors/<name>.behavior.ts) and
// holds the declaration derived from it, in its canonical form, byte for
// byte to the copy `make behaviors` wrote from the committed declaration
// and the core embeds, and to the committed declaration itself.
func TestCoreBehaviorClassesDeriveTheirCommittedDeclarations(t *testing.T) {
	requireSuperscalarCheckout(t)
	classes, err := ReadBehaviorClasses(coreBehaviorsDir, nil)
	if err != nil {
		t.Fatalf("ReadBehaviorClasses: %v", err)
	}
	if len(classes) == 0 {
		t.Fatal("no behavior class in internal/registry/behaviors")
	}
	for _, class := range classes {
		t.Run(class.Class, func(t *testing.T) {
			derived, err := registry.CanonicalDeclaration(class.Declaration)
			if err != nil {
				t.Fatalf("CanonicalDeclaration: %v", err)
			}
			source := strings.TrimSuffix(class.File, BehaviorClassSuffix) + ".behavior.json"
			committed := canonicalFile(t, filepath.Join(coreBehaviorsDir, source))
			if !bytes.Equal(derived, committed) {
				t.Errorf("%s derives a declaration that differs from %s:\n%s", class.File, source, firstDifference(derived, committed))
			}
			copied, err := crateCopy(class.Declaration.Name)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(derived, copied) {
				t.Errorf("%s derives a declaration that differs from the crate's copy %s.behavior.json:\n%s", class.File, class.Declaration.Name, firstDifference(derived, copied))
			}
		})
	}
}

// canonicalFile is the canonical form of the declaration file name.
func canonicalFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var decl registry.BehaviorDeclaration
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decl); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out, err := registry.CanonicalDeclaration(decl)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

// crateCopy is the canonical copy of the behavior name a crate embeds.
func crateCopy(name string) ([]byte, error) {
	for _, dir := range crateDeclarationDirs {
		data, err := os.ReadFile(filepath.Join(dir, name+".behavior.json"))
		if err == nil {
			return data, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return nil, os.ErrNotExist
}

// firstDifference shows the lines around the first line a and b differ at.
func firstDifference(a, b []byte) string {
	al := strings.Split(string(a), "\n")
	bl := strings.Split(string(b), "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return "line " + strconv.Itoa(i+1) + ":\n  derived:   " + x + "\n  committed: " + y
		}
	}
	return "(no line differs)"
}

// requireSuperscalarCheckout skips when the superscalar checkout the
// classes' tsconfig.json resolves is absent.
func requireSuperscalarCheckout(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("../../../third_party/superscalar/bindings/typescript/src/index.ts"); err != nil {
		t.Skip("the superscalar checkout (third_party/superscalar) is absent: scripts/superscalar-dep.sh --checkout")
	}
}

// acmeExtDir holds acme.Rating's class and the declaration the acme
// example's extension embeds (D103's step 5).
const acmeExtDir = "../../../examples/acme-schematic/ext"

// TestAcmeRatingClassDerivesItsDeclaration reads acme.Rating's class
// (examples/acme-schematic/ext/rating.behavior.ts), a behavior whose rules
// are the declarative vocabulary's (@declaresBehavior's rules), and holds
// the declaration derived from it byte for byte to the committed
// rating.behavior.json the extension registers, and to the acme-behaviors
// crate's copy, which acme-schematic behaviors writes from it.
func TestAcmeRatingClassDerivesItsDeclaration(t *testing.T) {
	requireSuperscalarCheckout(t)
	classes, err := ReadBehaviorClasses(acmeExtDir, nil)
	if err != nil {
		t.Fatalf("ReadBehaviorClasses: %v", err)
	}
	if len(classes) != 1 || classes[0].Declaration.Name != "acme.Rating" {
		t.Fatalf("classes %+v, want acme.Rating alone", classes)
	}
	class := classes[0]
	if !class.Declaration.Declarative() || class.Rules.Declared() {
		t.Fatalf("acme.Rating states rules %t and names crate %+v: it is declarative, with no crate", class.Declaration.Declarative(), class.Rules)
	}
	derived, err := registry.CanonicalDeclaration(class.Declaration)
	if err != nil {
		t.Fatalf("CanonicalDeclaration: %v", err)
	}
	for _, committed := range []string{
		filepath.Join(acmeExtDir, "rating.behavior.json"),
		"../../../examples/acme-schematic/crates/behaviors/declarations/acme.Rating.behavior.json",
	} {
		data, err := os.ReadFile(committed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(derived, data) {
			t.Errorf("%s derives a declaration that differs from %s:\n%s", class.File, committed, firstDifference(derived, data))
		}
	}
}
