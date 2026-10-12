package gcp

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The Cloud Storage platform (section 7.2, D54): a bucket is a Cloud
// Storage bucket in the environment's region, private whatever its
// settings. Uniform bucket-level access leaves IAM the only way in, with no
// object ACL to admit a reader, and public access prevention refuses any
// grant to the public, so the bucket's objects leave it only to the
// accounts its edges grant, or through a URL one of them signed.
//
// A member of a parameterized environment gets a bucket of its own,
// named with the parameter's value, which its destroy empties and
// removes. Any other bucket keeps its objects: a destroy fails on a bucket
// that holds some, rather than delete what an environment kept.

// bucketName names a bucket `<project>-<stack>-<bucket>`, since a bucket's
// name is global across Google Cloud and the project's id is the
// environment's own, then the parameter's name and value under a
// parameter: `acme-staging-shop-stack-shop-media`, or
// `acme-staging-shop-stack-shop-media-pr123`.
func bucketName(ctx registry.PlatformContext) any {
	v := valuesOf(ctx.Environment)
	return suffixed(ctx.Environment, v.project+"-"+kebab(ctx.Environment.Stack)+"-"+kebab(ctx.Deployable.Name), "-")
}

// bucketAddress is the bucket's name, an output of the bucket, so each
// workload the bucket's edges connect waits for it.
func bucketAddress(ctx registry.PlatformContext) any {
	return ir.Output{Resource: ctx.Deployable.Name + ".bucket", Name: "name"}
}

// lowerBucket lowers a bucket to a Cloud Storage bucket in the
// environment's region, with uniform bucket-level access and public access
// prevention. Settings turn object versioning on (`versioning`, off
// unless set) and delete each object a number of days after it was
// written (`deleteAfterDays`), through a lifecycle rule. Only a member of
// a parameterized environment sets forceDestroy, so its destroy removes
// the bucket with its objects.
func lowerBucket(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := valuesOf(env)
	rename := "the stack or the Bucket service"
	if err := checkLength("the bucket name of "+d.Name, d.ResourceName, 3, 63, rename); err != nil {
		return registry.Lowered{}, err
	}
	// Cloud Storage refuses a name that holds `google`, and the project's
	// id holds none.
	for _, part := range []string{kebab(env.Stack), kebab(d.Name)} {
		if strings.Contains(part, "google") {
			return registry.Lowered{}, fmt.Errorf("the bucket name of %s holds %q, which Cloud Storage refuses in a bucket's name; give %s another name", d.Name, "google", rename)
		}
	}
	versioning, _ := d.Settings["versioning"].(bool)
	props := map[string]any{
		"project":                  v.project,
		"name":                     d.ResourceName,
		"location":                 strings.ToUpper(v.region),
		"uniformBucketLevelAccess": true,
		"publicAccessPrevention":   publicAccessEnforced,
		"versioning":               map[string]any{"enabled": versioning},
		"forceDestroy":             len(env.Parameters) > 0,
	}
	if days, ok := d.Settings["deleteAfterDays"]; ok {
		props["lifecycleRules"] = []any{map[string]any{
			"action":    map[string]any{"type": "Delete"},
			"condition": map[string]any{"age": days},
		}}
	}
	return registry.Lowered{Resources: []*ir.Resource{{
		ID:         d.Name + ".bucket",
		Type:       TypeBucket,
		Properties: props,
	}}}, nil
}

// publicAccessEnforced is the public access prevention that refuses every
// grant to allUsers and allAuthenticatedUsers on a bucket and its objects.
const publicAccessEnforced = "enforced"

// connectBucket realizes a bucket edge from a Cloud Run server, or a Cloud
// Run job (D52), to a bucket (section 7.2, D54). The workload's account
// gets `roles/storage.objectUser` on the bucket alone: it reads, writes,
// lists and deletes its objects, and changes nothing of the bucket. It
// also gets `roles/iam.serviceAccountTokenCreator` on itself, which IAM's
// `signBlob` asks of an account that signs as itself, as the runtime
// signs a URL with no key of its own. Every bucket edge of a workload
// grants that one node, which the graph keeps once.
//
// The derived value is the bucket's name alone. The client reaches Cloud
// Storage with the workload's own credentials from the metadata server, so
// there is no credential to derive.
//
// A Rust server or job has no Bucket in its runtime, so the edge is
// refused.
func connectBucket(ctx registry.ConnectorContext) (registry.Connected, error) {
	from, to := ctx.From, ctx.To
	if from.Language == registry.APILanguageRust {
		return registry.Connected{}, fmt.Errorf("%s %s is a %s %s, which has no Bucket in its runtime to reach bucket %s with", from.Kind, from.Name, from.Language, from.Kind, to.Name)
	}
	account := from.Name + ".account"
	member := ir.Output{Resource: account, Name: "member"}
	return registry.Connected{
		Resources: []*ir.Resource{
			{
				ID:   from.Name + ".storage." + to.Name,
				Type: TypeBucketIAMMember,
				Properties: map[string]any{
					"bucket": ir.Output{Resource: to.Name + ".bucket", Name: "name"},
					"role":   "roles/storage.objectUser",
					"member": member,
				},
			},
			{
				ID:   from.Name + ".sign-as-self",
				Type: TypeServiceAccountIAMMember,
				Properties: map[string]any{
					"serviceAccountId": ir.Output{Resource: account, Name: "name"},
					"role":             "roles/iam.serviceAccountTokenCreator",
					"member":           member,
				},
			},
		},
		Value: ir.BucketConnection{Name: to.Address},
	}, nil
}

// checkPrivateBuckets refuses a Bucket service's bucket that is not
// private (D54): one without uniform bucket-level access, whose object
// ACLs could admit a reader IAM does not, or without public access
// prevention enforced, and any grant to allUsers or allAuthenticatedUsers
// on such a bucket, on behalf of any deployable. A bucket is never exposed,
// and a browser reaches its objects through signed URLs, so unlike
// nothing-public-unless-exposed the rule admits no exception for an
// exposed server.
func checkPrivateBuckets(env *ir.ResolvedEnvironment) []string {
	buckets := map[string]bool{}
	var out []string
	for _, res := range env.Resources.Resources {
		if res.Type != TypeBucket || !slices.ContainsFunc(res.Owners, func(owner string) bool {
			d := env.Deployable(owner)
			return d != nil && d.Kind == ir.DeployableBucket
		}) {
			continue
		}
		buckets[res.ID] = true
		owners := strings.Join(res.Owners, ", ")
		if res.Properties["uniformBucketLevelAccess"] != true {
			out = append(out, fmt.Sprintf("bucket %s of %s does not set uniform bucket-level access, so object ACLs could admit readers IAM does not", res.ID, owners))
		}
		if res.Properties["publicAccessPrevention"] != publicAccessEnforced {
			out = append(out, fmt.Sprintf("bucket %s of %s does not enforce public access prevention", res.ID, owners))
		}
	}
	for _, res := range env.Resources.Resources {
		outputs, _ := ir.ValueRefs(res.Properties["bucket"])
		if !slices.ContainsFunc(outputs, func(o ir.Output) bool { return buckets[o.Resource] }) {
			continue
		}
		members := []any{res.Properties["member"]}
		list, _ := res.Properties["members"].([]any)
		for _, m := range append(members, list...) {
			if member, ok := m.(string); ok && slices.Contains(publicMembers, member) {
				out = append(out, fmt.Sprintf("%s grants %v to %s on bucket %s of a Bucket service, which is never public", res.ID, res.Properties["role"], member, outputs[0].Resource))
			}
		}
	}
	return out
}
