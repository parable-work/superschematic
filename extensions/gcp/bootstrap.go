package gcp

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// Bootstrap (docs/stack-model.md, section 7.3) runs once per project with
// an owner's application default credentials, and is safe to run again:
//
//  1. it enables the APIs the environment's graph and the deploy use;
//  2. it creates the state bucket and the KMS key directly, since Pulumi
//     needs them before it can run;
//  3. it applies a bootstrap graph through the provisioner, in a Pulumi
//     project of its own (`<stack>-bootstrap`, a stack per GCP project):
//     the Artifact Registry repository named after the stack, the
//     `deployer` and read-only `planner` service accounts with their
//     roles, their use of the state bucket and key, the `builder` account
//     image builds run as and the `migrator` account the migration job
//     runs as, and Workload Identity Federation for the GitHub repository
//     the git remote names;
//  4. it creates each platform credential's secret directly, readable by
//     `deployer` and `planner` only. Environments may share a credential,
//     and the bootstrap graph of one would delete another's, so the secret
//     is not in it.
//
// The network a calling server's Direct VPC egress needs is not here: it
// is in the environment's graph, which only lowers it when an edge needs
// it (section 7.2). Bootstrap enables the Compute Engine API it needs.

// apiServices are the APIs every environment's deploy uses: the
// bootstrap's own (Service Usage, Resource Manager, IAM and its
// credentials and token exchange for Workload Identity Federation,
// Storage and KMS for the state, Artifact Registry for the images), Cloud
// Build and Cloud Logging for the image builds, and Secret Manager.
var apiServices = []string{
	"artifactregistry.googleapis.com",
	"cloudbuild.googleapis.com",
	"cloudkms.googleapis.com",
	"cloudresourcemanager.googleapis.com",
	"iam.googleapis.com",
	"iamcredentials.googleapis.com",
	"logging.googleapis.com",
	"secretmanager.googleapis.com",
	"serviceusage.googleapis.com",
	"storage.googleapis.com",
	"sts.googleapis.com",
}

// graphServices are the APIs a resource type's module needs. A database
// needs Cloud Run too, for its migration job.
var graphServices = map[string][]string{
	"gcp:cloudrunv2/":         {"run.googleapis.com", "cloudtrace.googleapis.com"},
	"gcp:sql/":                {"sqladmin.googleapis.com", "run.googleapis.com"},
	"gcp:compute/":            {"compute.googleapis.com"},
	"gcp:certificatemanager/": {"certificatemanager.googleapis.com"},
	"gcp:dns/":                {"dns.googleapis.com"},
}

// servicesFor returns the APIs a bootstrap of env enables, sorted.
func servicesFor(env *ir.ResolvedEnvironment) []string {
	out := slices.Clone(apiServices)
	if env.Resources != nil {
		for _, res := range env.Resources.Resources {
			for prefix, services := range graphServices {
				if strings.HasPrefix(res.Type, prefix) {
					out = append(out, services...)
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// The roles of the two accounts that run the generated CI (section 11.3).
var (
	// deployerRoles let `deployer` apply every resource the gcp target
	// emits, push images, run image builds and migration jobs, and enable
	// APIs.
	deployerRoles = []string{
		"roles/artifactregistry.writer",
		"roles/certificatemanager.owner",
		"roles/cloudbuild.builds.editor",
		"roles/cloudsql.admin",
		"roles/compute.loadBalancerAdmin",
		"roles/compute.networkAdmin",
		"roles/dns.admin",
		"roles/iam.serviceAccountAdmin",
		"roles/iam.serviceAccountUser",
		"roles/resourcemanager.projectIamAdmin",
		"roles/run.admin",
		"roles/secretmanager.admin",
		"roles/serviceusage.serviceUsageConsumer",
	}

	// plannerRoles let `planner` read every resource and IAM policy a
	// preview refreshes, and see whether a secret has a value, without
	// reading one.
	plannerRoles = []string{
		"roles/iam.securityReviewer",
		"roles/secretmanager.viewer",
		"roles/serviceusage.serviceUsageConsumer",
		"roles/viewer",
	}

	// stateRoles are each account's roles on the state bucket and key.
	// A preview takes the stack's lock, an object it writes, so
	// `planner` writes objects too.
	stateRoles = map[string][2]string{
		"deployer": {"roles/storage.objectAdmin", "roles/cloudkms.cryptoKeyEncrypterDecrypter"},
		"planner":  {"roles/storage.objectUser", "roles/cloudkms.cryptoKeyEncrypterDecrypter"},
	}

	// builderRoles let `builder`, which a Cloud Build build runs as, write
	// its logs. It pushes to the stack's repository only, and reads only
	// the build contexts in the state bucket (stateReaders).
	builderRoles = []string{"roles/logging.logWriter"}

	// migratorRoles let `migrator`, which the migration job runs as, reach
	// a Cloud SQL instance through the connector and log in to it with
	// IAM. It reads only the job documents and plans in the state bucket.
	migratorRoles = []string{"roles/cloudsql.client", "roles/cloudsql.instanceUser"}

	// stateReaders are the prefixes of the state bucket the accounts that
	// run builds and jobs read, and nothing else of it: not Pulumi's
	// state, nor the manifests.
	stateReaders = map[string]string{
		"builder":  buildPrefix,
		"migrator": migrationPrefix,
	}
)

// accountRoles are the bootstrap's accounts, in the order the graph names
// them.
var accountRoles = []string{"deployer", "planner", "builder", "migrator"}

// githubIssuer is the OIDC issuer of GitHub Actions' tokens.
const githubIssuer = "https://token.actions.githubusercontent.com"

// accountID is the id of one of the stack's CI accounts:
// `<stack>-deployer`, `<stack>-planner`.
func accountID(stack, role string) string { return kebab(stack) + "-" + role }

// accountMember is the IAM member of one of the stack's CI accounts.
func accountMember(v values, stack, role string) string {
	return fmt.Sprintf("serviceAccount:%s@%s.iam.gserviceaccount.com", accountID(stack, role), v.project)
}

// roleKey shortens a role for a node ID: roles/run.admin is run-admin.
func roleKey(role string) string {
	return strings.ReplaceAll(strings.TrimPrefix(role, "roles/"), ".", "-")
}

// BootstrapEnvironment returns the bootstrap graph of env's project as an
// environment the provisioner applies in one step: stack
// `<Stack>Bootstrap`, environment the project id. repository is the
// GitHub repository (`acme/shop`); empty leaves Workload Identity
// Federation out.
func BootstrapEnvironment(env *ir.ResolvedEnvironment, repository string) (*ir.ResolvedEnvironment, error) {
	v, err := envValues(env)
	if err != nil {
		return nil, err
	}
	stack := kebab(env.Stack)
	for _, role := range accountRoles {
		if id := accountID(env.Stack, role); len(id) > 30 {
			return nil, fmt.Errorf("gcp: the service account id %s is %d characters, and GCP allows 30; name the stack with at most %d", id, len(id), 30-len(role)-1)
		}
	}
	if len(stack)+len("-github") > 32 {
		return nil, fmt.Errorf("gcp: the workload identity pool id %s-github is longer than GCP's 32 characters; name the stack with at most 25", stack)
	}
	var nodes []*ir.Resource
	add := func(id, typ string, props map[string]any) {
		res := &ir.Resource{ID: id, Type: typ, Properties: props, Phase: ir.PhaseInfrastructure, Owners: []string{"bootstrap"}}
		outs, _ := ir.ValueRefs(map[string]any(props))
		for _, o := range outs {
			if !slices.Contains(res.DependsOn, o.Resource) {
				res.DependsOn = append(res.DependsOn, o.Resource)
			}
		}
		sort.Strings(res.DependsOn)
		nodes = append(nodes, res)
	}
	add("repository", TypeRepository, map[string]any{
		"project":      v.project,
		"location":     v.region,
		"repositoryId": stack,
		"format":       "DOCKER",
		"description":  fmt.Sprintf("The images of stack %s", env.Stack),
	})
	key := stateKeyName(v)
	for _, role := range []string{"deployer", "planner"} {
		member := ir.Output{Resource: role, Name: "member"}
		add(role, TypeAccount, map[string]any{
			"project":     v.project,
			"accountId":   accountID(env.Stack, role),
			"displayName": fmt.Sprintf("%s %s", env.Stack, role),
		})
		roles := deployerRoles
		if role == "planner" {
			roles = plannerRoles
		}
		for _, r := range roles {
			add(role+".role."+roleKey(r), TypeProjectIAMMember, map[string]any{"project": v.project, "role": r, "member": member})
		}
		add(role+".state", TypeBucketIAMMember, map[string]any{"bucket": stateBucket(v.project), "role": stateRoles[role][0], "member": member})
		add(role+".state-key", TypeCryptoKeyIAMMember, map[string]any{"cryptoKeyId": key, "role": stateRoles[role][1], "member": member})
		if repository != "" {
			add(role+".github", TypeServiceAccountIAMMember, map[string]any{
				"serviceAccountId": ir.Output{Resource: role, Name: "name"},
				"role":             "roles/iam.workloadIdentityUser",
				"member":           ir.Concat{"principalSet://iam.googleapis.com/", ir.Output{Resource: "github", Name: "name"}, "/attribute.repository/" + repository},
			})
		}
	}
	for _, role := range []string{"builder", "migrator"} {
		member := ir.Output{Resource: role, Name: "member"}
		add(role, TypeAccount, map[string]any{
			"project":     v.project,
			"accountId":   accountID(env.Stack, role),
			"displayName": fmt.Sprintf("%s %s", env.Stack, role),
		})
		roles := builderRoles
		if role == "migrator" {
			roles = migratorRoles
		}
		for _, r := range roles {
			add(role+".role."+roleKey(r), TypeProjectIAMMember, map[string]any{"project": v.project, "role": r, "member": member})
		}
		prefix := stateReaders[role]
		add(role+".state", TypeBucketIAMMember, map[string]any{
			"bucket": stateBucket(v.project),
			"role":   "roles/storage.objectViewer",
			"member": member,
			"condition": map[string]any{
				"title":      "Objects under " + prefix,
				"expression": fmt.Sprintf(`resource.name.startsWith("projects/_/buckets/%s/objects/%s")`, stateBucket(v.project), prefix),
			},
		})
	}
	add("builder.repository", TypeRepositoryIAMMember, map[string]any{
		"project":    v.project,
		"location":   v.region,
		"repository": ir.Output{Resource: "repository", Name: "name"},
		"role":       "roles/artifactregistry.writer",
		"member":     ir.Output{Resource: "builder", Name: "member"},
	})
	if repository != "" {
		add("github", TypeWorkloadIdentityPool, map[string]any{
			"project":                v.project,
			"workloadIdentityPoolId": stack + "-github",
			"displayName":            "GitHub Actions",
			"description":            fmt.Sprintf("The CI of %s", repository),
		})
		add("github.provider", TypeWorkloadIdentityProvider, map[string]any{
			"project":                        v.project,
			"workloadIdentityPoolId":         ir.Output{Resource: "github", Name: "workloadIdentityPoolId"},
			"workloadIdentityPoolProviderId": "github",
			"displayName":                    "GitHub",
			"attributeMapping": map[string]any{
				"google.subject":       "assertion.sub",
				"attribute.repository": "assertion.repository",
				"attribute.ref":        "assertion.ref",
			},
			"attributeCondition": fmt.Sprintf("assertion.repository == '%s'", repository),
			"oidc":               map[string]any{"issuerUri": githubIssuer},
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	var ids []string
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return &ir.ResolvedEnvironment{
		Version:     ir.ResolvedEnvironmentVersion,
		Stack:       env.Stack + "Bootstrap",
		Environment: v.project,
		Target:      Target,
		Provisioner: Provisioner,
		Values:      map[string]any{"project": v.project, "region": v.region},
		Deployables: []*ir.ResolvedDeployable{},
		Resources:   &ir.ResourceGraph{Resources: nodes},
		DeployOrder: []*ir.DeployStep{{Step: ir.StepInfrastructure, Resources: ids}},
	}, nil
}

// bootstrapper is the gcp target's Bootstrapper.
type bootstrapper struct{ ext Extension }

var _ registry.Bootstrapper = bootstrapper{}

// Bootstrap runs the four steps above for req's environment.
func (b bootstrapper) Bootstrap(ctx context.Context, req registry.BootstrapRequest) error {
	env := req.Environment
	v, err := envValues(env)
	if err != nil {
		return err
	}
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	cloud := b.ext.cloud()
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(log, "  "+format+"\n", args...) }

	services := servicesFor(env)
	logf("enable %d APIs on %s", len(services), v.project)
	if err := cloud.EnableServices(ctx, v.project, services); err != nil {
		return err
	}

	// Every call from here on may meet an API enabled moments ago.
	retry := func(what string, do func() error) error { return untilEnabled(ctx, logf, what, do) }

	bucket := stateBucket(v.project)
	var created bool
	err = retry("the state bucket", func() (err error) {
		created, err = cloud.EnsureBucket(ctx, v.project, bucket, v.region)
		return err
	})
	if err != nil {
		return err
	}
	logf("state bucket gs://%s: %s", bucket, createdOrKept(created))
	err = retry("the state key", func() (err error) {
		created, err = cloud.EnsureKey(ctx, v.project, v.region, stateKeyRing, stateKey)
		return err
	})
	if err != nil {
		return err
	}
	logf("state key %s: %s", stateKeyName(v), createdOrKept(created))

	graph, err := BootstrapEnvironment(env, req.Repository)
	if err != nil {
		return err
	}
	if req.Repository == "" {
		logf("no GitHub repository in the git remote: Workload Identity Federation is left out")
	}
	if req.Provisioner == nil || req.Dir == "" {
		return fmt.Errorf("gcp: bootstrap needs the provisioner and a directory for its program")
	}
	dir := filepath.Join(req.Dir, "bootstrap")
	if err := req.Provisioner.Render(graph, dir); err != nil {
		return err
	}
	backend, err := stateStore(b).Backend(ctx, env)
	if err != nil {
		return err
	}
	logf("apply the bootstrap graph: %d nodes", len(graph.Resources.Resources))
	err = retry("the bootstrap graph", func() error {
		return req.Provisioner.Apply(ctx, registry.ProvisionRequest{Environment: graph, Dir: dir, Backend: backend}, *graph.DeployOrder[0])
	})
	if err != nil {
		return err
	}
	logf("bootstrap graph applied")

	members := []string{accountMember(v, env.Stack, "deployer"), accountMember(v, env.Stack, "planner")}
	seen := map[string]bool{}
	for _, c := range req.Credentials {
		if seen[c.Secret] {
			continue
		}
		seen[c.Secret] = true
		err := retry("credential secret "+c.Secret, func() (err error) {
			if created, err = cloud.EnsureSecret(ctx, v.project, c.Secret); err != nil {
				return err
			}
			return cloud.GrantSecretAccess(ctx, v.project, c.Secret, members)
		})
		if err != nil {
			return err
		}
		logf("credential secret %s: %s, readable by deployer and planner", c.Secret, createdOrKept(created))
	}
	return nil
}

// apiPropagation bounds how long bootstrap retries a call that an API it
// has just enabled refuses, and apiRetry is the wait between tries.
var apiPropagation, apiRetry = 5 * time.Minute, 15 * time.Second

// untilEnabled runs do until it succeeds, fails for another reason than an
// API that is not enabled, or apiPropagation passes. Enabling an API
// finishes before every Google server sees it, and for some minutes the
// API may refuse a call as one the project has not enabled: the first
// bootstrap of a fresh project met it from Cloud KMS, both on creating
// the key ring and, through the provisioner, on reading the key's IAM
// policy.
func untilEnabled(ctx context.Context, logf func(string, ...any), what string, do func() error) error {
	deadline := time.Now().Add(apiPropagation)
	for {
		err := do()
		if err == nil || !serviceDisabled(err) || time.Now().After(deadline) {
			return err
		}
		logf("%s: an API enabled moments ago refuses calls yet; retry in %s", what, apiRetry)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(apiRetry):
		}
	}
}

// serviceDisabled reports whether err is Google's refusal of a call to an
// API the project has not enabled, by the text both the client libraries'
// and the provisioner's errors carry: the 403's message, or its
// SERVICE_DISABLED reason.
func serviceDisabled(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "SERVICE_DISABLED") || strings.Contains(msg, "before or it is disabled")
}

func createdOrKept(created bool) string {
	if created {
		return "created"
	}
	return "exists"
}

var (
	defaultCloudOnce sync.Once
	defaultCloudOf   Cloud
)

// defaultCloud is the process's Cloud over the client libraries.
func defaultCloud() Cloud {
	defaultCloudOnce.Do(func() { defaultCloudOf = NewCloud() })
	return defaultCloudOf
}
