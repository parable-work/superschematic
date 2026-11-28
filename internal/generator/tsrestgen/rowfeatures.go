package tsrestgen

import (
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// RowFeatureRoute is a row feature operation (#723, D154) as the router
// mounts it: an operation of a table's references, backlinks or files,
// which the loader added for a @rowFeatureOperations set, run by the
// runtime's rowFeatureOperation over RouterOptions.orm, within the
// request's caller, and over RouterOptions.attachmentsBucket for one that
// signs a URL or reads an upload back.
type RowFeatureRoute struct {
	// Set is the @rowFeatureOperations set that serves the operation.
	Set string
	// Database, Type, Key and Operation are the loader's marker: the API's
	// database, the table, its key field and the operation.
	Database  string
	Type      string
	Key       string
	Operation string
	// Bucket is the Bucket service the operation reads, for one that does
	// (ir.RowFeatureOperationTakesBucket).
	Bucket string
}

// Literal is the operation as router.ts's table of them holds it, a
// RowFeatureOperation: { type: 'Ticket', key: 'id', operation: 'backlinks' }.
func (r *RowFeatureRoute) Literal() string {
	return "{ type: " + tsString(r.Type) + ", key: " + tsString(r.Key) + ", operation: " + tsString(r.Operation) + " }"
}

// TakesBucket reports whether the operation reads the bucket.
func (r *RowFeatureRoute) TakesBucket() bool {
	return ir.RowFeatureOperationTakesBucket(r.Operation)
}

// RowFeatureRoutes are the row feature operations the router mounts, in
// the order of Endpoints.
func (o *APIOutput) RowFeatureRoutes() []*RowFeatureRoute {
	var out []*RowFeatureRoute
	for _, endpoint := range o.Endpoints {
		if endpoint.RowFeature != nil {
			out = append(out, endpoint.RowFeature)
		}
	}
	return out
}

// HasRowFeatures reports whether the API serves a table's references and
// files (#723), which the router runs over RouterOptions.orm.
func (o *APIOutput) HasRowFeatures() bool {
	return len(o.RowFeatureRoutes()) > 0
}

// RowFeatureBucket is the Bucket service the router's file operations
// read, RouterOptions.attachmentsBucket, or "" when none reads one. The
// loader holds every set of an API to one bucket.
func (o *APIOutput) RowFeatureBucket() string {
	for _, route := range o.RowFeatureRoutes() {
		if route.TakesBucket() && route.Bucket != "" {
			return route.Bucket
		}
	}
	return ""
}

// checkRowFeatureDeps refuses deps for an API that serves a table's
// references and files when they hold no repositories, which the router
// runs them through, or no member for the bucket its file operations read.
func (o *APIOutput) checkRowFeatureDeps(deps DepsInfo) error {
	routes := o.RowFeatureRoutes()
	if len(routes) == 0 {
		return nil
	}
	route := routes[0]
	if !depsORM(deps) {
		return fmt.Errorf("tsrestgen: %s serves %s's references and files (@rowFeatureOperations on %s), which run through Deps.orm, the repositories %s's TypeScript types export; enable outputs.types.typescript in %s's config", o.SchemaName, route.Type, route.Set, route.Database, route.Database)
	}
	if bucket := o.RowFeatureBucket(); bucket != "" && !slices.ContainsFunc(deps.Buckets, func(b DepsBucket) bool { return b.Service == bucket }) {
		return fmt.Errorf("tsrestgen: %s serves %s's files, in the bucket %s, and Deps holds no member for it: list %s in the API's buckets", o.SchemaName, route.Type, bucket, bucket)
	}
	return nil
}

// RowFeatureBucketField is the Deps member that holds the bucket the
// router's file operations read, or "".
func (o *APIOutput) RowFeatureBucketField() string {
	bucket := o.RowFeatureBucket()
	for _, b := range o.Deps.Buckets {
		if b.Service == bucket {
			return b.Field
		}
	}
	return ""
}

// rowFeatureEndpoint is a row feature operation (#723) as the router
// mounts it: its route, its parameters, its input and its result, decoded
// as an implemented endpoint's, and the marker it runs. Its types are
// rendered by a builder of their own, so they add no import to
// interfaces.ts, which has no method for it, and none but its input's
// validators to router.ts. The loader fills the marker; IR that did not go
// through it is refused here, naming the operation.
func (b *builder) rowFeatureEndpoint(ep apigen.EndpointInfo, op *ir.RowFeatureOperation) (EndpointInfo, error) {
	own := newBuilder(b.schema, b.opts)
	endpoint, err := own.endpoint(ep)
	if err != nil {
		return endpoint, err
	}
	owner := behaviorSet(b.schema, ep.Operation)
	if !slices.Contains(ir.RowFeatureOperationNames, op.Operation) || op.Type == "" || op.Key == "" || op.Database == "" {
		return endpoint, fmt.Errorf("tsrestgen: %s.%s is a row feature operation (#723) whose marker is not whole (operation %q, table %q, key %q, database %q); the loader fills it, so load the schema before generating", owner, ep.Operation.Name, op.Operation, op.Type, op.Key, op.Database)
	}
	if !slices.ContainsFunc(endpoint.PathParams, func(p ParamInfo) bool { return p.Name == op.Key }) {
		return endpoint, fmt.Errorf("tsrestgen: row feature operation %s.%s is routed at %s %s, which has no path parameter %s, the key of %s", owner, ep.Operation.Name, endpoint.Method, endpoint.Path, op.Key, op.Type)
	}
	endpoint.RowFeature = &RowFeatureRoute{
		Set:       owner,
		Database:  op.Database,
		Type:      op.Type,
		Key:       op.Key,
		Operation: op.Operation,
		Bucket:    op.Bucket,
	}
	for pkg, symbols := range own.validatorImports {
		for symbol := range symbols {
			b.addImport(b.validatorImports, pkg, symbol)
		}
	}
	return endpoint, nil
}
