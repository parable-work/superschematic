package registry

import (
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// bucketKind is the core Bucket kind (docs/stack-model.md, section 8.9,
// D54): private object storage that APIs list in their config's buckets,
// and that each stack reaching it deploys as a deployable of kind bucket.
// Its config, a name and a kind, is all it has: its schema declares
// nothing, and its pipeline is empty. It has a sentinel, since the configs
// that list it import its handle.
func bucketKind() KindSpec {
	return KindSpec{
		Name:       string(ir.SchemaKindBucket),
		StructRole: ir.RoleEmbeddedStruct,
		ForbiddenPackages: map[string]bool{
			pkgAPI:   true,
			pkgDB:    true,
			pkgStack: true,
		},
		Verify: verifyBucket,
	}
}

// verifyBucket refuses a declaration in a Bucket schema: a bucket's
// objects are bytes its APIs put and get, so its schema holds no types,
// and what a bucket takes per environment is its platform's settings.
func verifyBucket(schema *ir.Schema, r VerifyReporter) {
	var declared []string
	for name := range schema.Types {
		declared = append(declared, "type "+name)
	}
	for name := range schema.Enums {
		declared = append(declared, "enum "+name)
	}
	for name := range schema.Unions {
		declared = append(declared, "union "+name)
	}
	for name := range schema.Scalars {
		declared = append(declared, "scalar "+name)
	}
	for _, set := range schema.OperationSets {
		if set != nil {
			declared = append(declared, "operation set "+set.Name)
		}
	}
	for _, job := range schema.Jobs {
		if job != nil {
			declared = append(declared, "job "+job.Name)
		}
	}
	sort.Strings(declared)
	for _, what := range declared {
		r.Errorf("", "%s is a %s service, which declares nothing in its schema files: its config is all it has, and the APIs that use it list it in their buckets (%s)", schema.Name, ir.SchemaKindBucket, what)
	}
}
