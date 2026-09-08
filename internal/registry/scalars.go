package registry

import (
	"fmt"
	"sort"

	scalars "github.com/parable-work/superscalar/go"
)

// ScalarCatalog is the read side of a scalar registry: one metadata row per
// canonical scalar name. The loader hydrates every ScalarDef a schema
// references from it (internal/loader.hydrateScalarsFromRegistry), so the
// catalog decides which scalar names a schema may use and what they
// generate to.
//
// The row type is the scalar Go package's ScalarMetadata: the engine takes a
// hard dependency on that package rather than declaring a mirror of its
// shape. An extension that assembles its own scalars over the core catalog
// registers the assembled package's rows through RegisterScalars.
type ScalarCatalog interface {
	// Scalar returns the row for canonical, false when the catalog has no
	// scalar of that name.
	Scalar(canonical string) (*scalars.ScalarMetadata, bool)
	// Names returns every canonical name in the catalog, sorted.
	Names() []string
}

// RegisterScalars installs the catalog the loader hydrates from, replacing
// CoreScalars. One catalog per registry: a second call is an error, as is a
// call after Finalize. owner labels the error message; extensions pass their
// Name().
func (r *Registry) RegisterScalars(owner string, catalog ScalarCatalog) error {
	if err := r.registrable("scalar catalog"); err != nil {
		return err
	}
	if owner == "" {
		return fmt.Errorf("registry: scalar catalog has no owner")
	}
	if catalog == nil {
		return fmt.Errorf("registry: %s registered a nil scalar catalog", owner)
	}
	if r.scalars != nil {
		return fmt.Errorf("registry: scalar catalog already registered by %s; %s cannot register another", r.scalarsOwner, owner)
	}
	r.scalars = catalog
	r.scalarsOwner = owner
	r.noteExtension(owner)
	return nil
}

// Scalars returns the registered scalar catalog, or CoreScalars when no
// extension registered one.
func (r *Registry) Scalars() ScalarCatalog {
	if r.scalars != nil {
		return r.scalars
	}
	return CoreScalars()
}

// CoreScalars is the catalog of the scalar Go package the engine links:
// scalars.ScalarMetadataByCanonical, read on every call so the catalog
// cannot drift from the package.
func CoreScalars() ScalarCatalog {
	return ScalarCatalogOf(scalars.ScalarMetadataByCanonical)
}

// ScalarCatalogOf wraps a metadata map as a ScalarCatalog. Tests use it to
// build restricted catalogs; extensions use it over their assembled
// package's map.
func ScalarCatalogOf(rows map[string]*scalars.ScalarMetadata) ScalarCatalog {
	return metadataCatalog(rows)
}

type metadataCatalog map[string]*scalars.ScalarMetadata

func (c metadataCatalog) Scalar(canonical string) (*scalars.ScalarMetadata, bool) {
	row, ok := c[canonical]
	if !ok || row == nil {
		return nil, false
	}
	return row, true
}

func (c metadataCatalog) Names() []string {
	names := make([]string, 0, len(c))
	for name, row := range c {
		if row != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
