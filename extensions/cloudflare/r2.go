package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// Buckets on R2 (docs/stack-model.md, sections 18.2 and 18.7, D54, D64): a
// Bucket service is a private R2 bucket in the environment's account, named
// after the stack, the environment and the bucket (names.go). R2 makes a bucket public only through an
// r2.dev managed domain or a custom domain, which no platform emits and
// buckets-never-public refuses. A Worker reaches a bucket through an
// r2_bucket binding, and presigns S3 URLs for its objects with an account
// token the edge mints, scoped to that bucket's objects: a Worker has no
// identity to sign with.
//
// A member of a parameterized environment gets a bucket of its own, named
// with its parameters' values, which the target's PreDestroy empties
// before the member's destroy, since the provider deletes only an empty
// bucket. Any other bucket keeps its objects: a destroy fails on a bucket
// that holds some, rather than delete what an environment kept, as on gcp.

// R2 keeps a Bucket service's objects in an R2 bucket.
const R2 = "cloudflare.r2"

// WorkersR2Connector realizes a bucket edge from a Worker to an R2 bucket.
const WorkersR2Connector = "cloudflare.workers-r2"

// The resource types of a bucket and of its edges. TypeR2Bucket and
// TypeR2BucketLifecycle are pinned in schemas/ at ProviderVersion; the
// bucket edge's token is TypeAccountToken, which bootstrap pinned.
// TypeR2ManagedDomain and TypeR2CustomDomain, which would make a bucket
// public, no platform emits: buckets-never-public names them.
const (
	TypeR2Bucket          = "cloudflare:index/r2Bucket:R2Bucket"
	TypeR2BucketLifecycle = "cloudflare:index/r2BucketLifecycle:R2BucketLifecycle"
	TypeR2ManagedDomain   = "cloudflare:index/r2ManagedDomain:R2ManagedDomain"
	TypeR2CustomDomain    = "cloudflare:index/r2CustomDomain:R2CustomDomain"
)

// PolicyBucketsNeverPublic refuses a public domain on a Bucket service's
// bucket (section 18.7).
const PolicyBucketsNeverPublic = "buckets-never-public"

// The permission group a bucket edge's token holds: Workers R2 Storage
// Bucket Item Write, read and write of one bucket's objects and nothing of
// the bucket itself, of scope R2BucketScope. Cloudflare publishes the id,
// the same in every account, beside the name in R2's documentation; a
// platform cannot list the groups as bootstrap does, since resolution
// calls nothing, so bootstrap checks the id against the account's list.
const (
	R2ObjectsWriteGroup     = "2efd5506f9c8494dacb1fa10a3e7d5b6"
	R2ObjectsWriteGroupName = "Workers R2 Storage Bucket Item Write"
	R2BucketScope           = "com.cloudflare.edge.r2.bucket"
)

// r2Jurisdiction is the jurisdiction a bucket's token resource names: every
// bucket the platform makes is in the default one (r2Settings).
const r2Jurisdiction = "default"

// r2Settings is the schema of a bucket's settings on R2 (section 18.1):
//
//   - locationHint, the region R2 places the bucket in when it is created
//     (`location`), a hint it may ignore;
//   - storageClass, the class a new object is stored in unless its upload
//     names another;
//   - deleteAfterDays, the age at which a lifecycle rule deletes an object,
//     as gcp's lifecycle rule does.
//
// There is no jurisdiction: a jurisdiction's bucket is reached at another
// S3 host, `<account>.<jurisdiction>.r2.cloudflarestorage.com`, which the
// R2 credential the runtime signs with does not carry (section 18.15). R2
// has no object versioning, so gcp's versioning has no place here.
const r2Settings = `{
  "type": "object",
  "properties": {
    "locationHint": {"enum": ["wnam", "enam", "weur", "eeur", "apac", "oc"]},
    "storageClass": {"enum": ["Standard", "InfrequentAccess"]},
    "deleteAfterDays": {"type": "integer", "minimum": 1, "maximum": 36500}
  },
  "additionalProperties": false
}`

// r2Platform is the R2 platform's registration.
func r2Platform() registry.PlatformSpec {
	return registry.PlatformSpec{
		Name:      R2,
		Kind:      ir.DeployableBucket,
		Settings:  json.RawMessage(r2Settings),
		NameOf:    bucketName,
		AddressOf: bucketAddress,
		Lower:     lowerBucket,
	}
}

// R2BucketName names a Bucket service's bucket of an environment of a
// stack, `<stack>-<env>-<bucket>` in kebab case
// (`acme-shop-staging-shop-media`), since a bucket's name is unique in its
// account, which may hold several stacks and environments. A member of a
// parameterized environment appends each parameter's name and value
// (bucketName).
func R2BucketName(stack, environment, bucket string) string {
	return AccountName(stack, environment, bucket)
}

// bucketName is R2BucketName with each parameter's name and value
// appended, so each member of a preview gets a bucket of its own:
// `acme-shop-preview-shop-media-pr123` when pr is 123.
func bucketName(ctx registry.PlatformContext) any {
	return accountName(ctx.Environment, ctx.Deployable.Name)
}

// bucketID and lifecycleID are the IDs of a bucket deployable's nodes.
func bucketID(bucket string) string    { return bucket + ".bucket" }
func lifecycleID(bucket string) string { return bucket + ".lifecycle" }

// bucketAddress is the bucket's name, an output of its node, so each
// workload whose edge names it waits for it.
func bucketAddress(ctx registry.PlatformContext) any {
	return ir.Output{Resource: bucketID(ctx.Deployable.Name), Name: "name"}
}

// The length of an R2 bucket's name, and what it holds: lowercase letters,
// digits and hyphens, beginning and ending with a letter or a digit.
const (
	minBucketName = 3
	maxBucketName = 63
)

var bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// lowerBucket lowers a bucket deployable to an r2Bucket in the
// environment's account, with the location hint and the storage class its
// settings give, and with deleteAfterDays an r2BucketLifecycle whose one
// rule deletes each object that many days after it was written. It refuses
// as unrealizable a name R2 does not take, and the state bucket's name.
func lowerBucket(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	v := targetValuesOf(ctx.Environment.Values)
	if err := checkBucketName(d.Name, d.ResourceName, partsOf(ctx.Environment, "the Bucket service", d.Name)); err != nil {
		return registry.Lowered{}, err
	}
	props := map[string]any{
		"accountId": v.accountID,
		"name":      d.ResourceName,
	}
	if hint, ok := d.Settings["locationHint"].(string); ok {
		props["location"] = hint
	}
	if class, ok := d.Settings["storageClass"].(string); ok {
		props["storageClass"] = class
	}
	out := registry.Lowered{Resources: []*ir.Resource{{
		ID:         bucketID(d.Name),
		Type:       TypeR2Bucket,
		Phase:      ir.PhaseInfrastructure,
		Properties: props,
	}}}
	if days, ok := wholeNumber(d.Settings["deleteAfterDays"]); ok {
		out.Resources = append(out.Resources, &ir.Resource{
			ID:    lifecycleID(d.Name),
			Type:  TypeR2BucketLifecycle,
			Phase: ir.PhaseInfrastructure,
			Properties: map[string]any{
				"accountId":  v.accountID,
				"bucketName": bucketAddress(ctx),
				"rules": []any{map[string]any{
					"id":         fmt.Sprintf("delete-after-%d-days", days),
					"enabled":    true,
					"conditions": map[string]any{"prefix": ""},
					"deleteObjectsTransition": map[string]any{
						"condition": map[string]any{"type": "Age", "maxAge": float64(days * 24 * 60 * 60)},
					},
				}},
			},
		})
	}
	return out, nil
}

// wholeNumber reads a setting that the schema holds to an integer.
func wholeNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

// checkBucketName refuses, as unrealizable, a bucket name R2 does not
// take: outside 3 to 63 characters, or under a parameter one that leaves no
// room for the value; with a character other than a lowercase letter, a
// digit or a hyphen, or a hyphen at an end; or the name of a bucket
// bootstrap makes in every account. parts are what the name joins.
func checkBucketName(bucket string, name any, parts []namePart) error {
	n, refs := literalLength(name)
	switch {
	case !refs && n > maxBucketName:
		return registry.Unrealizable("the R2 bucket of %s would be named %q, %d characters, and R2 takes a name of %d to %d: %s", bucket, name, n, minBucketName, maxBucketName, shorten(parts))
	case !refs && n < minBucketName:
		return registry.Unrealizable("the R2 bucket of %s would be named %q, %d characters, and R2 takes a name of %d to %d; give the Bucket service a longer name", bucket, name, n, minBucketName, maxBucketName)
	case refs && n >= maxBucketName:
		return registry.Unrealizable("the R2 bucket of %s would be named with %d characters before its parameter values, and R2 takes a name of at most %d: %s", bucket, n, maxBucketName, shorten(parts))
	}
	literal := literalText(name)
	if !bucketNamePattern.MatchString(literal) {
		return registry.Unrealizable("the R2 bucket of %s would be named %q, and R2 takes lowercase letters, digits and hyphens, a letter or a digit at each end; give the stack, the environment or the Bucket service another name", bucket, literal)
	}
	if literal == StateBucket {
		return registry.Unrealizable("the R2 bucket of %s would be named %s, the bucket bootstrap keeps the account's state in; give the stack, the environment or the Bucket service another name", bucket, literal)
	}
	return nil
}

// literalText is a name's characters outside its references, a parameter's
// value standing as a letter, so the pattern checks what the name holds.
func literalText(name any) string {
	switch name := name.(type) {
	case string:
		return name
	case ir.Concat:
		var b strings.Builder
		for _, part := range name {
			if s, ok := part.(string); ok {
				b.WriteString(s)
			} else {
				b.WriteString("x")
			}
		}
		return b.String()
	}
	return ""
}

// bucketTokenID is the ID of the account token a deployable's edge to a
// bucket mints.
func bucketTokenID(from, bucket string) string { return from + ".r2." + bucket + ".token" }

// connectR2 realizes a bucket edge from a Worker to an R2 bucket. The
// value is the bucket's name, an r2_bucket binding named after the derived
// field, which the platform writes (resourceBinding), and the credential
// with which the runtime presigns S3 URLs: the account, and the id and the
// value of an account token the edge mints, flagged secret, which reaches
// the Worker as a secret_text binding. The token allows Workers R2 Storage
// Bucket Item Write on that bucket alone, under its name,
// `<stack>-<env>-<caller>-<bucket>` with the member's parameters: read and
// write of the bucket's objects, and nothing of the bucket or any other.
func connectR2(ctx registry.ConnectorContext) (registry.Connected, error) {
	from, to := ctx.From, ctx.To
	v := targetValuesOf(ctx.Environment.Values)
	token := bucketTokenID(from.Name, to.Name)
	resources := join(`{"`+R2BucketScope+".", v.accountID, "_", r2Jurisdiction, "_", to.Address, `":"*"}`)
	return registry.Connected{
		Resources: []*ir.Resource{{
			ID:    token,
			Type:  TypeAccountToken,
			Phase: ir.PhaseInfrastructure,
			Properties: map[string]any{
				"accountId": v.accountID,
				"name":      accountName(ctx.Environment, from.Name, to.Name),
				"policies": []any{map[string]any{
					"effect":           "allow",
					"permissionGroups": []any{map[string]any{"id": R2ObjectsWriteGroup}},
					"resources":        resources,
				}},
			},
		}},
		Value: ir.BucketConnection{
			Name:    to.Address,
			Binding: ctx.Edge.Field,
			R2: &ir.R2Credential{
				Account:     v.accountID,
				AccessKeyID: ir.Output{Resource: token, Name: "id"},
				Token:       ir.Output{Resource: token, Name: "value"},
			},
		},
	}, nil
}

// checkBucketTokenGroup refuses an environment with an edge to an R2
// bucket, from a server or any other Worker, on an account that does not
// list R2ObjectsWriteGroup under its name. A bucket edge's token names the
// group by the id Cloudflare publishes, since resolution lists nothing;
// bootstrap, which lists the account's groups, checks it, so a changed id
// fails there rather than at a deploy.
func checkBucketTokenGroup(env *ir.ResolvedEnvironment, groups []PermissionGroup) error {
	if !slices.ContainsFunc(env.Edges, func(e *ir.Edge) bool {
		d := env.Deployable(e.To)
		return e.Kind == ir.EdgeBucket && d != nil && d.Platform == R2
	}) {
		return nil
	}
	i := slices.IndexFunc(groups, func(g PermissionGroup) bool { return g.ID == R2ObjectsWriteGroup })
	switch {
	case i < 0:
		return fmt.Errorf("cloudflare: account permission groups list no %s, %s, which the token of each bucket edge holds; Cloudflare may have changed its id, and the extension needs a release that names the new one", R2ObjectsWriteGroup, R2ObjectsWriteGroupName)
	case groups[i].Name != R2ObjectsWriteGroupName:
		return fmt.Errorf("cloudflare: account permission group %s is %s, and the token of each bucket edge holds it as %s; Cloudflare may have changed it, and the extension needs a release that names the new one", R2ObjectsWriteGroup, groups[i].Name, R2ObjectsWriteGroupName)
	}
	return nil
}

// checkBucketsNeverPublic refuses an r2.dev managed domain or a custom
// domain on a Bucket service's bucket (section 18.7): a bucket is never
// exposed, and a browser reaches its objects through URLs a Worker signs,
// so unlike nothing-public-unless-exposed the rule admits no exception. A
// domain names its bucket by the bucket node's output or by the bucket's
// name.
func checkBucketsNeverPublic(env *ir.ResolvedEnvironment) []string {
	byNode := map[string]string{}
	byName := map[string]string{}
	for _, d := range env.Deployables {
		if d.Kind != ir.DeployableBucket {
			continue
		}
		if name, ok := d.ResourceName.(string); ok {
			byName[name] = d.Name
		}
		for _, res := range env.Resources.Resources {
			if res.Type == TypeR2Bucket && slices.Contains(res.Owners, d.Name) {
				byNode[res.ID] = d.Name
			}
		}
	}
	var out []string
	for _, res := range env.Resources.Resources {
		var what string
		switch res.Type {
		case TypeR2ManagedDomain:
			what = "the r2.dev managed domain"
		case TypeR2CustomDomain:
			what = "custom domain"
		default:
			continue
		}
		bucket := ""
		if name, ok := res.Properties["bucketName"].(string); ok {
			bucket = byName[name]
		}
		outputs, _ := ir.ValueRefs(res.Properties["bucketName"])
		for _, o := range outputs {
			if b, ok := byNode[o.Resource]; ok {
				bucket = b
			}
		}
		if bucket != "" {
			out = append(out, fmt.Sprintf("%s %s makes the bucket of %s public, and a Bucket service's bucket is never public: a browser reaches its objects through signed URLs", what, res.ID, bucket))
		}
	}
	return out
}

// destroyPreparer is the target's DestroyPreparer: it empties each bucket
// of a member of a parameterized environment before the member's destroy,
// since the provider deletes only an empty bucket (section 18.2). A plain
// environment's buckets keep their objects, and its destroy fails on one
// that holds some.
type destroyPreparer struct{ ext Extension }

var _ registry.DestroyPreparer = destroyPreparer{}

// PrepareDestroy deletes every object of each of the run's own buckets,
// through R2's S3 API with the run's token. A bucket that is gone already
// is empty.
func (p destroyPreparer) PrepareDestroy(ctx context.Context, run registry.Run, log io.Writer) error {
	env := run.Environment
	if env == nil || env.Resources == nil || len(env.Parameters) == 0 {
		return nil
	}
	var buckets []string
	for _, res := range env.Resources.Resources {
		if res.Type != TypeR2Bucket || res.Inherited {
			continue
		}
		name, err := runString(res.Properties["name"], run.Parameters)
		if err != nil {
			return fmt.Errorf("cloudflare: bucket %s: %w", res.ID, err)
		}
		buckets = append(buckets, name)
	}
	if len(buckets) == 0 {
		return nil
	}
	v, err := envValues(env)
	if err != nil {
		return err
	}
	cloud, err := p.ext.cloud()
	if err != nil {
		return err
	}
	if log == nil {
		log = io.Discard
	}
	for _, bucket := range buckets {
		keys, err := cloud.ListObjects(ctx, v.accountID, bucket, "")
		if errors.Is(err, fs.ErrNotExist) {
			_, _ = fmt.Fprintf(log, "  bucket %s: gone\n", bucket)
			continue
		}
		if err != nil {
			return err
		}
		for _, key := range keys {
			if err := cloud.DeleteObject(ctx, v.accountID, bucket, key); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(log, "  bucket %s: emptied, %d objects deleted\n", bucket, len(keys))
	}
	return nil
}

// runString is a name in a run: a string, or a concatenation with each
// parameter's value in its place.
func runString(v any, params map[string]string) (string, error) {
	switch v := v.(type) {
	case string:
		return v, nil
	case ir.Parameter:
		value, ok := params[string(v)]
		if !ok {
			return "", fmt.Errorf("the run gives parameter %s no value", v)
		}
		return value, nil
	case ir.Concat:
		var b strings.Builder
		for _, part := range v {
			s, err := runString(part, params)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("the name is %T, not a string or a parameter's concatenation", v)
}
