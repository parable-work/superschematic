package apigen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// A table's references and files (#723, D154), the operations the loader
// adds to a @rowFeatureOperations set, are the build's to serve: the Go
// server's router mounts each at its route behind the steps any route
// runs, from the set's clause, on the handler factory of any route, whose
// call is a method of rowfeatures.go rather than of the implementation.
// That method runs the operation through References() and Attachments()
// of the ORM of the API's database, as the request's caller, so every read
// keeps to what the caller may read (D156, D184), holds a file the path
// names to the row it names, and answers what the runtime answers as the
// API's types hold it. The implementation the project writes has no method
// for them. The OpenAPI document, the SDKs, the permission catalog and the
// tool documents read them as any other operation, so they stay in
// APIOutput.Endpoints, and ImplementedOutput moves them to
// APIOutput.RowFeatureEndpoints for the Go server's writers.

// RowFeatureOperation is the row feature operation the endpoint runs, or
// nil.
func (e EndpointInfo) RowFeatureOperation() *ir.RowFeatureOperation {
	if e.Operation == nil {
		return nil
	}
	return e.Operation.RowFeatureOperation
}

// rowFeatureDatabase is the database the endpoints' row feature
// operations run on, the API's database, or "" when none is one.
func rowFeatureDatabase(endpoints []EndpointInfo) string {
	for _, ep := range endpoints {
		if op := ep.RowFeatureOperation(); op != nil && op.Database != "" {
			return op.Database
		}
	}
	return ""
}

// HasRowFeatureOperations reports whether the API serves a table's
// references and files: an endpoint the loader added for a
// @rowFeatureOperations set.
func (o *APIOutput) HasRowFeatureOperations() bool {
	return slices.ContainsFunc(o.Endpoints, func(ep EndpointInfo) bool { return ep.RowFeatureOperation() != nil })
}

// HasRowFeatureEndpoints reports whether the Go server serves a table's
// references and files: one of its RowFeatureEndpoints in the view
// ImplementedOutput returns, or of its Endpoints that it would move there.
// Its Config then takes the ORM of its database, and the module holds
// rowfeatures.go.
func (o *APIOutput) HasRowFeatureEndpoints() bool {
	return len(o.RowFeatureEndpoints) > 0 || o.HasRowFeatureOperations()
}

// allRowFeatureEndpoints are the endpoints that run a row feature operation,
// among both lists.
func (o *APIOutput) allRowFeatureEndpoints() []EndpointInfo {
	var out []EndpointInfo
	for _, list := range [][]EndpointInfo{o.Endpoints, o.RowFeatureEndpoints} {
		for _, ep := range list {
			if ep.RowFeatureOperation() != nil {
				out = append(out, ep)
			}
		}
	}
	return out
}

// RowFeatureBucket is the Bucket service the API's file operations read,
// whose Deps field Config.Attachments is built from; "" for an API whose
// operations read none.
func (o *APIOutput) RowFeatureBucket() string {
	for _, ep := range o.allRowFeatureEndpoints() {
		if op := ep.RowFeatureOperation(); ir.RowFeatureOperationTakesBucket(op.Operation) && op.Bucket != "" {
			return op.Bucket
		}
	}
	return ""
}

// RowFeatureBucketField is the Deps field that holds the bucket the file
// operations read, or "".
func (o *APIOutput) RowFeatureBucketField() string {
	bucket := o.RowFeatureBucket()
	for _, b := range o.Deps.Buckets {
		if b.Service == bucket {
			return b.Field
		}
	}
	return ""
}

// RowFeatureRoutes is the type of rowfeatures.go whose method serves the
// endpoint: one per namespace, whose methods are named as an
// implementation's would be (ticketRowFeatures).
func (e EndpointInfo) RowFeatureRoutes() string {
	if e.RowFeatureOperation() == nil {
		return ""
	}
	return lowerFirst(codegen.ToPascalCase(e.Namespace)) + "RowFeatures"
}

// RowFeatureRouteTypes are the types of rowfeatures.go that serve the
// endpoints, each once, in route order.
func (o *APIOutput) RowFeatureRouteTypes() []string {
	var out []string
	for _, ep := range o.RowFeatureEndpoints {
		if name := ep.RowFeatureRoutes(); !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// RowFeatureTakesBucket reports whether the endpoint's operation reads the
// bucket.
func (e EndpointInfo) RowFeatureTakesBucket() bool {
	op := e.RowFeatureOperation()
	return op != nil && ir.RowFeatureOperationTakesBucket(op.Operation)
}

// RowFeatureKeyParam is the Go name of the path parameter that holds the
// row's key.
func (e EndpointInfo) RowFeatureKeyParam() string {
	op := e.RowFeatureOperation()
	if op == nil {
		return ""
	}
	for _, p := range e.PathParams {
		if p.Name == op.Key {
			return p.Name
		}
	}
	return op.Key
}

// checkRowFeatures refuses a Go server whose row feature operations it
// cannot serve: IR the loader did not fill, Deps that hold the ORM of
// another database, a public API whose Config.DB is another's, and a
// bucket Deps holds no field for.
func (o *APIOutput) checkRowFeatures() error {
	endpoints := o.allRowFeatureEndpoints()
	if len(endpoints) == 0 {
		return nil
	}
	for _, ep := range endpoints {
		op := ep.RowFeatureOperation()
		owner := ep.Namespace + "." + ep.Name
		if !slices.Contains(ir.RowFeatureOperationNames, op.Operation) || op.Type == "" || op.Key == "" || op.Database == "" {
			return fmt.Errorf("apigen: %s is a row feature operation (#723) whose marker is not whole (operation %q, table %q, key %q, database %q); the loader fills it, so load the schema before generating", owner, op.Operation, op.Type, op.Key, op.Database)
		}
		if o.Deps.Database != op.Database || o.Deps.ORMModule == "" {
			return fmt.Errorf("apigen: %s serves %s's references and files, of %s, and the Go server's Deps hold the ORM of %q (#723)", o.SchemaName, op.Type, op.Database, o.Deps.Database)
		}
		if o.IsPublic && o.ORMModule != o.Deps.ORMModule {
			return fmt.Errorf("apigen: %s serves %s's references and files, of %s, and its Config.DB is the ORM of %s, its auth schema: serve them from an API whose auth schema is %s (#723)", o.SchemaName, op.Type, op.Database, o.UpstreamSchema, op.Database)
		}
		if !slices.ContainsFunc(ep.PathParams, func(p Param) bool { return p.Name == op.Key }) {
			return fmt.Errorf("apigen: row feature operation %s is routed at %s %s, which has no path parameter %s, the key of %s (#723)", owner, ep.Method, ep.Path, op.Key, op.Type)
		}
	}
	if bucket := o.RowFeatureBucket(); bucket != "" {
		if o.RowFeatureBucketField() == "" {
			return fmt.Errorf("apigen: %s serves files, in the bucket %s, and its Deps hold no field for it: list %s in the API's buckets (#723)", o.SchemaName, bucket, bucket)
		}
		for _, b := range o.Deps.Buckets {
			if b.Service == bucket && b.ContentAddressed {
				return fmt.Errorf("apigen: %s serves files, in the bucket %s, which is content addressed (D142): the Go server's attachments sign their uploads of a plain bucket until D142's naming of an object slots in (#723)", o.SchemaName, bucket)
			}
		}
	}
	return nil
}

// RowFeatureCall is what the endpoint's method of rowfeatures.go hands
// the runtime, as Go: the operation's call of References() or
// Attachments(), over row, the row the path names, and file, the file it
// names, and body, the input decoded into the runtime's shape.
func (e EndpointInfo) RowFeatureCall() string {
	op := e.RowFeatureOperation()
	if op == nil {
		return ""
	}
	switch op.Operation {
	case ir.RowOpReferences:
		return "r.references().Of(ctx, row)"
	case ir.RowOpAddReference:
		return "r.references().Add(ctx, rowrefs.AddInput{From: row, To: body.To, Role: body.role()})"
	case ir.RowOpRemoveReference:
		return "r.removeReferences(ctx, rowrefs.RemoveInput{From: row, To: body.To, Role: body.Role})"
	case ir.RowOpBacklinks:
		return "r.references().ReferencedBy(ctx, row, rowrefs.ReferencedByOptions{})"
	case ir.RowOpAttachments:
		return "r.attachments().List(ctx, row)"
	case ir.RowOpBeginUpload:
		return "r.attachments().Begin(ctx, rowrefs.BeginInput{To: row, Name: body.Name, MediaType: body.MediaType, Size: body.Size, SHA256: body.SHA256, Caption: body.Caption}, r.bucket, 0)"
	case ir.RowOpCompleteUpload:
		return "r.attachments().Complete(ctx, ref, r.bucket)"
	case ir.RowOpDownload:
		return "r.attachments().Download(ctx, ref, r.bucket, 0)"
	case ir.RowOpFileAttachment:
		return "r.attachments().File(ctx, rowrefs.FileInput{FileRef: ref, To: body.To, Move: body.Move != nil && *body.Move, Role: body.role()})"
	case ir.RowOpRemoveAttachment:
		return "r.removeAttachment(ctx, ref)"
	}
	return ""
}

// RowFeatureNamesFile reports whether the endpoint's route names one of
// the row's files.
func (e EndpointInfo) RowFeatureNamesFile() bool {
	return slices.ContainsFunc(e.PathParams, func(p Param) bool { return p.Name == ir.RowFeatureFileParam })
}

// RowFeatureBody is the runtime's shape of the endpoint's body, the
// rowFeature... type of rowfeatures.go its input decodes into, or "".
func (e EndpointInfo) RowFeatureBody() string {
	op := e.RowFeatureOperation()
	if op == nil || !e.HasInput {
		return ""
	}
	switch op.Operation {
	case ir.RowOpAddReference, ir.RowOpRemoveReference:
		return "rowFeatureReferenceBody"
	case ir.RowOpBeginUpload:
		return "rowFeatureBeginBody"
	case ir.RowOpFileAttachment:
		return "rowFeatureFileBody"
	}
	return ""
}

// RowFeatureTables are the tables whose operations the endpoints run, by
// feature: the Go server's references() and attachments() refuse a
// database without them.
func (o *APIOutput) RowFeatureUses(feature string) bool {
	for _, ep := range o.allRowFeatureEndpoints() {
		op := ep.RowFeatureOperation()
		switch feature {
		case "references":
			if f := op.Feature(); f == ir.RowFeatureReferences || f == ir.RowFeatureBacklinks || op.Operation == ir.RowOpFileAttachment {
				if f != ir.RowFeatureAttachments {
					return true
				}
			}
		case "attachments":
			if op.Feature() == ir.RowFeatureAttachments {
				return true
			}
		}
	}
	return false
}

// rowFeatureDoc is the doc comment line of a row feature endpoint's
// handler factory.
func (e EndpointInfo) RowFeatureDoc() string {
	op := e.RowFeatureOperation()
	if op == nil {
		return ""
	}
	return fmt.Sprintf("It runs %s's %s (#723) through the %s of the ORM of %s, as the request's caller, through the method of rowfeatures.go that serves it: no implementation method serves it.",
		op.Type, op.Operation, strings.ToLower(map[string]string{ir.RowFeatureReferences: "References", ir.RowFeatureBacklinks: "References", ir.RowFeatureAttachments: "Attachments"}[op.Feature()]), op.Database)
}
