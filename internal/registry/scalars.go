package registry

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	scalars "github.com/parable-work/superscalar/go"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
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
//
// A row whose primitive the loader reads as the object language primitive
// (ir.CatalogLanguagePrimitive, which reads a spelling it does not know as
// object too) must be one the validators hold to JSON
// (ir.ScalarDef.ObjectJSONError): its JSONSchemaType, which becomes the
// scalar's json_schema type mapping, is "any", or "object" or "array" with
// no pattern and no length, and it is not a file-upload scalar. A catalog
// with a row that is not is refused, with every such row named.
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
	if err := checkObjectScalarRows(owner, catalog); err != nil {
		return err
	}
	r.scalars = catalog
	r.scalarsOwner = owner
	r.noteExtension(owner)
	return nil
}

// checkObjectScalarRows refuses each row of catalog the loader would hydrate
// into an object scalar the validators do not hold to JSON: the row read as
// the loader fills a ScalarDef in from it, its upload metadata included
// (ir.ScalarDef.ObjectJSONError). A row whose primitive the loader does not
// know gets a message of its own, which names that primitive and the
// spellings it may have meant: the loader reads it as object without saying
// so.
func checkObjectScalarRows(owner string, catalog ScalarCatalog) error {
	uploads, declaresUploads := catalog.(UploadCatalog)
	var errs []error
	for _, name := range catalog.Names() {
		row, ok := catalog.Scalar(name)
		if !ok {
			continue
		}
		primitive, known := ir.CatalogLanguagePrimitive(row.Primitive)
		def := &ir.ScalarDef{
			Name:              name,
			LanguagePrimitive: primitive,
			Pattern:           row.Pattern,
			MinLength:         row.MinLength,
			MaxLength:         row.MaxLength,
			TypeMappings:      map[string]string{"json_schema": row.JSONSchemaType},
		}
		if declaresUploads {
			if upload, ok := uploads.Upload(name); ok {
				def.FileUpload = &upload.FileUpload
			}
		}
		err := def.ObjectJSONError()
		switch {
		case err == nil:
		case known:
			errs = append(errs, fmt.Errorf("registry: %s's scalar catalog: %w", owner, err))
		default:
			errs = append(errs, fmt.Errorf("registry: %s's scalar catalog: scalar %s has primitive %q, which the loader does not know and reads as object, so the validators check its values as strings: "+
				"spell the primitive String, Int, Float or Bool, or, for a scalar that holds JSON, Object with a JSONSchemaType of object, array or any", owner, name, row.Primitive))
		}
	}
	return errors.Join(errs...)
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

// ScalarUpload is the upload metadata of one file-upload scalar. The scalar
// package's ScalarMetadata row has no field for it, so a catalog carries it
// beside the row (UploadCatalog). The loader copies it onto the hydrated
// ScalarDef: FileUpload makes a field of the scalar a multipart file part in
// the generated APIs and SDKs and is what Validate<T, { uploadMaxBytes }>
// requires; ImageConstraints, when set, adds the image checks.
type ScalarUpload struct {
	FileUpload       ir.FileUploadConfig
	ImageConstraints *ir.ImageConstraints
}

// UploadCatalog is a ScalarCatalog that declares which of its scalars are
// file uploads. The loader asks the registered catalog for this interface
// and hydrates the upload metadata of every scalar it names.
type UploadCatalog interface {
	ScalarCatalog
	// Upload returns the upload metadata of canonical, false when it is not
	// a file-upload scalar.
	Upload(canonical string) (ScalarUpload, bool)
}

// ScalarCatalogWithUploads returns catalog with uploads declared on it,
// keyed by canonical scalar name. Every key must name a scalar of catalog.
// A distribution whose scalar package defines upload scalars registers the
// result through RegisterScalars.
func ScalarCatalogWithUploads(catalog ScalarCatalog, uploads map[string]ScalarUpload) (UploadCatalog, error) {
	if catalog == nil {
		return nil, fmt.Errorf("registry: upload metadata declared on a nil scalar catalog")
	}
	names := make([]string, 0, len(uploads))
	for name := range uploads {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := catalog.Scalar(name); !ok {
			return nil, fmt.Errorf("registry: upload metadata declared on %s, which the scalar catalog does not define", name)
		}
	}
	owned := make(map[string]ScalarUpload, len(uploads))
	for name, upload := range uploads {
		owned[name] = cloneScalarUpload(upload)
	}
	return uploadCatalog{ScalarCatalog: catalog, uploads: owned}, nil
}

type uploadCatalog struct {
	ScalarCatalog
	uploads map[string]ScalarUpload
}

func (c uploadCatalog) Upload(canonical string) (ScalarUpload, bool) {
	upload, ok := c.uploads[canonical]
	if !ok {
		return ScalarUpload{}, false
	}
	return cloneScalarUpload(upload), true
}

// RawBodyCheck answers for the wrapped catalog, so uploads declared over a
// catalog with raw-body checks keep them.
func (c uploadCatalog) RawBodyCheck(canonical string) (ScalarRawBodyCheck, bool) {
	if checks, ok := c.ScalarCatalog.(RawBodyCheckCatalog); ok {
		return checks.RawBodyCheck(canonical)
	}
	return ScalarRawBodyCheck{}, false
}

// ScalarRawBodyCheck is the raw-body check of one scalar: the Go function a
// generated route calls on the raw JSON of its request body before it
// decodes an input type with fields of the scalar (apigen.RawBodyCheck).
// The scalar package's ScalarMetadata row has no field for it, so a catalog
// carries it beside the row (RawBodyCheckCatalog).
type ScalarRawBodyCheck = apigen.RawBodyCheck

// RawBodyCheckCatalog is a ScalarCatalog that declares raw-body checks for
// some of its scalars. The api generator asks the registered catalog for
// this interface (Registry.RawBodyChecks) and renders a call to each check
// an input type's fields use. A scalar without one gets no call.
type RawBodyCheckCatalog interface {
	ScalarCatalog
	// RawBodyCheck returns the check of canonical, false when it has none.
	RawBodyCheck(canonical string) (ScalarRawBodyCheck, bool)
}

// ScalarCatalogWithRawBodyChecks returns catalog with raw-body checks
// declared on it, keyed by canonical scalar name. Every key must name a
// scalar of catalog, and every check must render a call that compiles
// (apigen.RawBodyCheck.Validate). A distribution registers the result
// through RegisterScalars; it composes with ScalarCatalogWithUploads in
// either order.
func ScalarCatalogWithRawBodyChecks(catalog ScalarCatalog, checks map[string]ScalarRawBodyCheck) (RawBodyCheckCatalog, error) {
	if catalog == nil {
		return nil, fmt.Errorf("registry: raw-body checks declared on a nil scalar catalog")
	}
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := catalog.Scalar(name); !ok {
			return nil, fmt.Errorf("registry: raw-body check declared on %s, which the scalar catalog does not define", name)
		}
		if err := checks[name].Validate(); err != nil {
			return nil, fmt.Errorf("registry: scalar %s: %w", name, err)
		}
	}
	owned := make(map[string]ScalarRawBodyCheck, len(checks))
	for name, check := range checks {
		owned[name] = check
	}
	return rawBodyCheckCatalog{ScalarCatalog: catalog, checks: owned}, nil
}

type rawBodyCheckCatalog struct {
	ScalarCatalog
	checks map[string]ScalarRawBodyCheck
}

func (c rawBodyCheckCatalog) RawBodyCheck(canonical string) (ScalarRawBodyCheck, bool) {
	check, ok := c.checks[canonical]
	return check, ok
}

// Upload answers for the wrapped catalog, so raw-body checks declared over
// a catalog with uploads keep them.
func (c rawBodyCheckCatalog) Upload(canonical string) (ScalarUpload, bool) {
	if uploads, ok := c.ScalarCatalog.(UploadCatalog); ok {
		return uploads.Upload(canonical)
	}
	return ScalarUpload{}, false
}

// RawBodyChecks returns the registered scalar catalog's raw-body checks,
// nil when the catalog declares none (it is not a RawBodyCheckCatalog).
// The api generator reads it as apigen.Options.RawBodyChecks.
func (r *Registry) RawBodyChecks() apigen.RawBodyChecks {
	if checks, ok := r.Scalars().(RawBodyCheckCatalog); ok {
		return checks
	}
	return nil
}

// cloneScalarUpload copies the slices and pointers of upload, so neither
// the declaring map nor a hydrated ScalarDef can change the catalog's row.
func cloneScalarUpload(upload ScalarUpload) ScalarUpload {
	upload.FileUpload.AllowedTypes = slices.Clone(upload.FileUpload.AllowedTypes)
	if upload.ImageConstraints != nil {
		image := *upload.ImageConstraints
		if image.MinAspectRatio != nil {
			ratio := *image.MinAspectRatio
			image.MinAspectRatio = &ratio
		}
		if image.MaxAspectRatio != nil {
			ratio := *image.MaxAspectRatio
			image.MaxAspectRatio = &ratio
		}
		upload.ImageConstraints = &image
	}
	return upload
}
