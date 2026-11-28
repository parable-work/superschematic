package rustrestgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A table's references, backlinks and files in a Rust API (#723, D154).
// An operation the loader adds for a @rowFeatureOperations set has no
// method in its namespace's trait: the router's handler hands what it
// decoded to src/rowfeatures.rs, which runs it through the references()
// and attachments() of the repositories of the API's database, in the
// database's Rust types, as the request's caller (the HTTP runtime's
// orm::rowfeatures::routes), and answers what they answer as the API's
// types hold it. The file operations sign their URLs of the bucket
// Implementations::new takes, the set's. TypeScript serves the same
// routes through the runtime's rowFeatureOperation and Go through
// apigen's rowfeatures.go.

// RowFeaturesFile is the file of an API crate that serves its row feature
// operations.
const RowFeaturesFile = "rowfeatures.rs"

// RowFeatureInfo is a row feature operation as the crate serves it.
type RowFeatureInfo struct {
	// Operation is the operation's name (ir.RowFeatureOperationNames), and
	// Variant the runtime's RowFeatureOp it is.
	Operation string
	Variant   string
	// Database, Type and Bucket are the loader's marker: the API's
	// database, the table, and the Bucket service of a file operation.
	Database string
	Type     string
	Bucket   string
	// KeyField is the Args' field of the row's key, FileField the one of
	// the file the path names (empty when it names none), and Input
	// whether the Args hold a body.
	KeyField  string
	FileField string
	Input     bool
}

// rowFeatureInfo is the row feature operation endpoint runs, nil for every
// other. info must hold its arguments (builder.params). IR the loader did
// not fill is an error that names the operation.
func rowFeatureInfo(endpoint apigen.EndpointInfo, info EndpointInfo) (*RowFeatureInfo, error) {
	op := endpoint.RowFeatureOperation()
	if op == nil {
		return nil, nil
	}
	owner := info.Namespace + "." + info.Name
	if !slices.Contains(ir.RowFeatureOperationNames, op.Operation) || op.Type == "" || op.Key == "" || op.Database == "" {
		return nil, fmt.Errorf("rustrestgen: %s is a row feature operation (#723) whose marker is not whole (operation %q, table %q, key %q, database %q); the loader fills it, so load the schema before generating", owner, op.Operation, op.Type, op.Key, op.Database)
	}
	r := &RowFeatureInfo{
		Operation: op.Operation,
		Variant:   rustutil.ToPascalCase(op.Operation),
		Database:  op.Database,
		Type:      op.Type,
		Input:     info.Input != nil,
	}
	if ir.RowFeatureOperationTakesBucket(op.Operation) {
		r.Bucket = op.Bucket
	}
	for _, arg := range info.PathArgs {
		switch arg.Name {
		case op.Key:
			r.KeyField = arg.Field
		case ir.RowFeatureFileParam:
			r.FileField = arg.Field
		}
	}
	if r.KeyField == "" {
		return nil, fmt.Errorf("rustrestgen: row feature operation %s is routed at %s %s, which has no path parameter %s, the key of %s (#723)", owner, info.Method, info.Path, op.Key, op.Type)
	}
	return r, nil
}

// HasRowFeatureOperations reports whether the router serves a table's
// references and files (#723): the crate then has src/rowfeatures.rs,
// Implementations holds the RowFeatures its handlers run them on, and the
// crate depends on the database's Rust types for its repositories.
func (o *APIOutput) HasRowFeatureOperations() bool {
	return len(o.RowFeatureEndpoints()) > 0
}

// RowFeatureEndpoints are the row feature operations the router serves, in
// route order.
func (o *APIOutput) RowFeatureEndpoints() []EndpointInfo {
	var out []EndpointInfo
	for _, endpoint := range o.Endpoints {
		if endpoint.RowFeature != nil {
			out = append(out, endpoint)
		}
	}
	return out
}

// RowFeatureDatabase is the DB service the row feature operations run on,
// the API's database; empty without one.
func (o *APIOutput) RowFeatureDatabase() string {
	for _, endpoint := range o.RowFeatureEndpoints() {
		return endpoint.RowFeature.Database
	}
	return ""
}

// RowFeatureBucket is the Bucket service the file operations sign their
// URLs of, which Implementations::new takes; empty for a router whose
// operations sign none.
func (o *APIOutput) RowFeatureBucket() string {
	for _, endpoint := range o.RowFeatureEndpoints() {
		if endpoint.RowFeature.Bucket != "" {
			return endpoint.RowFeature.Bucket
		}
	}
	return ""
}

// RowFeatureBucketField is the Deps field that holds the bucket the file
// operations sign their URLs of, or "".
func (o *APIOutput) RowFeatureBucketField() string {
	bucket := o.RowFeatureBucket()
	if bucket == "" || o.Deps == nil {
		return ""
	}
	for _, b := range o.Deps.Buckets {
		if b.Service == bucket {
			return b.Field
		}
	}
	return ""
}

// RowFeaturesInterfacesUse is src/rowfeatures.rs's import of the row
// feature operations' Args structs.
func (o *APIOutput) RowFeaturesInterfacesUse() string {
	var names []string
	for _, endpoint := range o.RowFeatureEndpoints() {
		if endpoint.HasArgs() {
			names = append(names, endpoint.ArgsName)
		}
	}
	slices.Sort(names)
	return rustutil.UseLine("crate::interfaces", names)
}

// RowFeaturesUseTypes reports whether a row feature operation answers a
// type of the API's types crate, which src/rowfeatures.rs then imports.
func (o *APIOutput) RowFeaturesUseTypes() bool {
	return slices.ContainsFunc(o.RowFeatureEndpoints(), func(endpoint EndpointInfo) bool {
		return strings.Contains(endpoint.OutputRustType, "types::")
	})
}

// checkRowFeatureDeps refuses row feature operations the crate cannot
// serve: an API with no database of its own in Deps, one whose operations
// are of another DB service, a database whose Rust types are not
// generated, and a bucket Deps holds no field for or holds as a content
// bucket (D142).
func (o *APIOutput) checkRowFeatureDeps() error {
	if !o.HasRowFeatureOperations() {
		return nil
	}
	db := o.RowFeatureDatabase()
	switch {
	case o.Deps == nil || o.Deps.Database == "":
		return fmt.Errorf("rustrestgen: %s serves the references and files of tables of %s (#723), and its Deps hold no database; depend on %s", o.SchemaName, db, db)
	case o.Deps.Database != db:
		return fmt.Errorf("rustrestgen: %s serves the references and files of tables of %s (#723), and its Deps hold %s's", o.SchemaName, db, o.Deps.Database)
	case o.Deps.DatabaseTypesCrate == "":
		return fmt.Errorf("rustrestgen: %s serves the references and files of tables of %s (#723), whose repositories are in its Rust types, and %s generates none; enable outputs.types.rust in %s's config", o.SchemaName, db, db, db)
	}
	bucket := o.RowFeatureBucket()
	if bucket == "" {
		return nil
	}
	if o.RowFeatureBucketField() == "" {
		return fmt.Errorf("rustrestgen: %s serves files, in the bucket %s, and its Deps hold no field for it: list %s in the API's buckets (#723)", o.SchemaName, bucket, bucket)
	}
	for _, b := range o.Deps.Buckets {
		if b.Service == bucket && b.ContentAddressed {
			return fmt.Errorf("rustrestgen: %s serves files, in the bucket %s, which is content addressed (D142): the attachments sign their uploads of a plain bucket until D142's naming of an object slots in (#723)", o.SchemaName, bucket)
		}
	}
	return nil
}
