package tsreader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// symbolIdentity is the resolved identity of a name: the originating package
// it was declared in and its declared name. Identity is resolved through the
// checker (alias chains are skipped), so it is import-path independent and
// robust to re-exports and renamed imports.
type symbolIdentity struct {
	// pkg is the originating package name (for example "@superschematic/db"), or ""
	// when the symbol is declared outside any package.
	pkg string

	// name is the declared symbol name.
	name string

	// decl is the (first) declaration node.
	decl *astNode
}

// fromPackage reports whether the identity originates from the given package
// with the given declared name.
func (id symbolIdentity) is(pkg, name string) bool {
	return id.pkg == pkg && id.name == name
}

// identityOf resolves the identity of the symbol behind a name node (an
// identifier, the rightmost segment of a qualified name, or a property
// access). It returns false when the checker cannot resolve a symbol.
func (w *walker) identityOf(node *astNode) (symbolIdentity, bool) {
	target := node
	switch node.Kind {
	case kindQualifiedName:
		target = node.AsQualifiedName().Right
	case kindPropertyAccessExpression:
		target = node.Name()
	}
	sym := w.checker.GetSymbolAtLocation(target)
	if sym == nil {
		return symbolIdentity{}, false
	}
	sym = w.checker.SkipAlias(sym)
	return w.identityOfSymbol(sym)
}

// identityOfSymbol resolves the identity of an already-resolved symbol.
func (w *walker) identityOfSymbol(sym *astSymbol) (symbolIdentity, bool) {
	if sym == nil || len(sym.Declarations) == 0 {
		return symbolIdentity{}, false
	}
	decl := sym.Declarations[0]
	file := getSourceFileOfNode(decl)
	if file == nil {
		return symbolIdentity{}, false
	}
	return symbolIdentity{
		pkg:  w.packages.nameForFile(file.FileName()),
		name: sym.Name,
		decl: decl,
	}, true
}

// packageIndex resolves source file paths to the name of the nearest
// enclosing package.json. Results are cached per directory. Resolution by
// on-disk package identity (instead of import specifier text) is what makes
// decorator and wrapper recognition alias-robust: a re-export or renamed
// import still declares in the same package.
type packageIndex struct {
	mu    sync.Mutex
	byDir map[string]string
}

func newPackageIndex() *packageIndex {
	return &packageIndex{byDir: make(map[string]string)}
}

// nameForFile returns the package name owning the file, or "" when no
// package.json is found.
func (p *packageIndex) nameForFile(fileName string) string {
	return p.nameForDir(filepath.Dir(filepath.FromSlash(fileName)))
}

func (p *packageIndex) nameForDir(dir string) string {
	p.mu.Lock()
	if name, ok := p.byDir[dir]; ok {
		p.mu.Unlock()
		return name
	}
	p.mu.Unlock()

	name := ""
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err == nil {
		var pkg struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			name = pkg.Name
		}
	} else {
		parent := filepath.Dir(dir)
		if parent != dir {
			name = p.nameForDir(parent)
		}
	}

	p.mu.Lock()
	p.byDir[dir] = name
	p.mu.Unlock()
	return name
}

// serviceNameForPackage derives the schema service name from a package name:
// the scope prefix is stripped ("@schemas/web-db" -> "web-db").
func serviceNameForPackage(pkg string) string {
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		return pkg[i+1:]
	}
	return pkg
}
