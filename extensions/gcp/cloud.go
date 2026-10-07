package gcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	cloudbuild "cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/iam/apiv1/iampb"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	serviceusage "cloud.google.com/go/serviceusage/apiv1"
	"cloud.google.com/go/serviceusage/apiv1/serviceusagepb"
	"cloud.google.com/go/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/parable-work/superschematic/registry"
)

// Cloud is what the gcp target calls on Google Cloud directly, outside the
// provisioner: what bootstrap makes before Pulumi can run (section 7.3),
// the secret values `stack secrets set` and bootstrap write, and the deploy
// manifests. NewCloud returns the one over Google Cloud's client
// libraries, which authenticate with application default credentials;
// tests pass a fake. Every call is idempotent.
type Cloud interface {
	// EnableServices enables APIs (`run.googleapis.com`) on a project.
	EnableServices(ctx context.Context, project string, services []string) error

	// ProjectNumber returns a project's number, from the name Resource
	// Manager gives the project, `projects/<number>`. The provider of the
	// generated CI's Workload Identity Federation is named by it (D47).
	ProjectNumber(ctx context.Context, project string) (string, error)

	// EnsureBucket creates a bucket with uniform access, public access
	// prevention and object versioning, unless it exists.
	EnsureBucket(ctx context.Context, project, bucket, location string) (created bool, err error)

	// EnsureKey creates a key ring and a symmetric encryption key in it,
	// unless they exist.
	EnsureKey(ctx context.Context, project, location, keyRing, key string) (created bool, err error)

	// ReadObject returns an object's content, or an error that wraps
	// fs.ErrNotExist.
	ReadObject(ctx context.Context, bucket, object string) ([]byte, error)

	// WriteObject replaces an object.
	WriteObject(ctx context.Context, bucket, object string, data []byte) error

	// DeleteObject deletes an object; a missing one is not an error.
	DeleteObject(ctx context.Context, bucket, object string) error

	// EnsureSecret creates a Secret Manager secret with automatic
	// replication, unless it exists.
	EnsureSecret(ctx context.Context, project, secret string) (created bool, err error)

	// GrantSecretAccess adds members to the accessor role of a secret,
	// leaving its other members as they are.
	GrantSecretAccess(ctx context.Context, project, secret string, members []string) error

	// SecretHasValue reports whether a secret exists with an enabled
	// latest version.
	SecretHasValue(ctx context.Context, project, secret string) (bool, error)

	// AddSecretVersion adds a version holding value. It returns an error
	// that wraps registry.ErrSecretNotCreated when the secret does not
	// exist.
	AddSecretVersion(ctx context.Context, project, secret string, value []byte) error

	// AccessSecret returns the value of a secret's latest version.
	AccessSecret(ctx context.Context, project, secret string) ([]byte, error)

	// ImageDigest returns the digest, `sha256:<hex>`, of the image an
	// Artifact Registry tag names (`<repository>:<tag>`), or an error that
	// wraps fs.ErrNotExist when the tag does not exist.
	ImageDigest(ctx context.Context, image string) (string, error)

	// RunBuild runs a Cloud Build build of a Docker image in the
	// project's region, and returns once it finished: with the digest of
	// the image it pushed, or an error that names the build's logs.
	RunBuild(ctx context.Context, project, region string, spec BuildSpec) (*BuildResult, error)

	// EnsureJob creates a Cloud Run job, or updates it when it differs
	// from spec, and reports whether it changed anything.
	EnsureJob(ctx context.Context, project, region string, spec JobSpec) (changed bool, err error)

	// RunJob runs a job once, with args in place of its container's, and
	// returns once the execution finished, whether it succeeded or not.
	RunJob(ctx context.Context, project, region, job string, args []string) (*JobRun, error)
}

// BuildSpec is a Cloud Build build of a Docker image.
type BuildSpec struct {
	// Bucket and Object hold the build context: a gzipped tarball.
	Bucket string
	Object string

	// Dockerfile is the Dockerfile's path in the context.
	Dockerfile string

	// Image is the repository and tag the build pushes,
	// `<repository>:<tag>`.
	Image string

	// ServiceAccount is the email of the account the build runs as.
	ServiceAccount string

	// Timeout bounds the build.
	Timeout time.Duration
}

// BuildResult is a finished build.
type BuildResult struct {
	// ID names the build, and LogURL is where its logs are.
	ID     string
	LogURL string

	// Digest is the digest of the image it pushed, `sha256:<hex>`.
	Digest string
}

// JobSpec is a Cloud Run job that runs one container to its end, once,
// with no retry.
type JobSpec struct {
	// Name is the job's name in its project and region.
	Name string

	// Container names the job's one container, and Image is its image, by
	// digest.
	Container string
	Image     string

	// ServiceAccount is the email of the account the job runs as.
	ServiceAccount string

	// Timeout bounds one run.
	Timeout time.Duration

	// Labels are the job's labels.
	Labels map[string]string
}

// JobRun is one finished execution of a job.
type JobRun struct {
	// Name is the execution's resource name, and LogURI where its logs
	// are.
	Name   string
	LogURI string

	// Succeeded is whether its one task succeeded; Message says why it
	// did not.
	Succeeded bool
	Message   string
}

// NewCloud returns the Cloud over Google Cloud's client libraries. Each
// client is made on first use, so a command that never reaches Google
// Cloud, such as resolution, needs no credentials.
func NewCloud() Cloud { return &googleCloud{} }

// googleCloud is Cloud over the client libraries.
type googleCloud struct {
	mu       sync.Mutex
	usage    *serviceusage.Client
	storage  *storage.Client
	kms      *kms.KeyManagementClient
	secrets  *secretmanager.Client
	registry *artifactregistry.Client
	projects *resourcemanager.ProjectsClient
	builds   *cloudbuild.Client
	jobs     *run.JobsClient
	clientOK bool
}

// clients makes the clients once.
func (c *googleCloud) clients(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clientOK {
		return nil
	}
	var err error
	if c.usage, err = serviceusage.NewClient(ctx); err != nil {
		return fmt.Errorf("gcp: the Service Usage client (application default credentials: gcloud auth application-default login): %w", err)
	}
	if c.storage, err = storage.NewClient(ctx); err != nil {
		return fmt.Errorf("gcp: the Cloud Storage client: %w", err)
	}
	if c.kms, err = kms.NewKeyManagementClient(ctx); err != nil {
		return fmt.Errorf("gcp: the Cloud KMS client: %w", err)
	}
	if c.secrets, err = secretmanager.NewClient(ctx); err != nil {
		return fmt.Errorf("gcp: the Secret Manager client: %w", err)
	}
	if c.registry, err = artifactregistry.NewClient(ctx); err != nil {
		return fmt.Errorf("gcp: the Artifact Registry client: %w", err)
	}
	if c.projects, err = resourcemanager.NewProjectsClient(ctx); err != nil {
		return fmt.Errorf("gcp: the Resource Manager client: %w", err)
	}
	if c.builds, err = cloudbuild.NewClient(ctx); err != nil {
		return fmt.Errorf("gcp: the Cloud Build client: %w", err)
	}
	if c.jobs, err = run.NewJobsClient(ctx); err != nil {
		return fmt.Errorf("gcp: the Cloud Run jobs client: %w", err)
	}
	c.clientOK = true
	return nil
}

// enableBatch is the most services one BatchEnableServices call takes.
const enableBatch = 20

func (c *googleCloud) EnableServices(ctx context.Context, project string, services []string) error {
	if err := c.clients(ctx); err != nil {
		return err
	}
	for batch := range slices.Chunk(services, enableBatch) {
		op, err := c.usage.BatchEnableServices(ctx, &serviceusagepb.BatchEnableServicesRequest{
			Parent:     "projects/" + project,
			ServiceIds: batch,
		})
		if err != nil {
			return fmt.Errorf("gcp: enable %v on %s: %w", batch, project, err)
		}
		if _, err := op.Wait(ctx); err != nil {
			return fmt.Errorf("gcp: enable %v on %s: %w", batch, project, err)
		}
	}
	return nil
}

func (c *googleCloud) ProjectNumber(ctx context.Context, project string) (string, error) {
	if err := c.clients(ctx); err != nil {
		return "", err
	}
	p, err := c.projects.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{Name: "projects/" + project})
	if err != nil {
		return "", fmt.Errorf("gcp: project %s: %w", project, err)
	}
	number, ok := strings.CutPrefix(p.GetName(), "projects/")
	if !ok || number == "" || strings.Trim(number, "0123456789") != "" {
		return "", fmt.Errorf("gcp: project %s is named %q, not projects/<number>", project, p.GetName())
	}
	return number, nil
}

func (c *googleCloud) EnsureBucket(ctx context.Context, project, bucket, location string) (bool, error) {
	if err := c.clients(ctx); err != nil {
		return false, err
	}
	handle := c.storage.Bucket(bucket)
	_, err := handle.Attrs(ctx)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, storage.ErrBucketNotExist) {
		return false, fmt.Errorf("gcp: bucket %s: %w", bucket, err)
	}
	err = handle.Create(ctx, project, &storage.BucketAttrs{
		Location:                 location,
		UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
		PublicAccessPrevention:   storage.PublicAccessPreventionEnforced,
		VersioningEnabled:        true,
	})
	if status.Code(err) == codes.AlreadyExists || isConflict(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("gcp: create bucket %s in %s: %w", bucket, project, err)
	}
	return true, nil
}

// isConflict reports whether err is the JSON API's 409, which a bucket
// another run created in between returns.
func isConflict(err error) bool {
	var coded interface{ HTTPCode() int }
	return errors.As(err, &coded) && coded.HTTPCode() == 409
}

func (c *googleCloud) EnsureKey(ctx context.Context, project, location, keyRing, key string) (bool, error) {
	if err := c.clients(ctx); err != nil {
		return false, err
	}
	parent := fmt.Sprintf("projects/%s/locations/%s", project, location)
	ring := parent + "/keyRings/" + keyRing
	created := false
	if _, err := c.kms.GetKeyRing(ctx, &kmspb.GetKeyRingRequest{Name: ring}); status.Code(err) == codes.NotFound {
		_, err := c.kms.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{Parent: parent, KeyRingId: keyRing, KeyRing: &kmspb.KeyRing{}})
		if err != nil && status.Code(err) != codes.AlreadyExists {
			return false, fmt.Errorf("gcp: create key ring %s: %w", ring, err)
		}
		created = true
	} else if err != nil {
		return false, fmt.Errorf("gcp: key ring %s: %w", ring, err)
	}
	name := ring + "/cryptoKeys/" + key
	if _, err := c.kms.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: name}); status.Code(err) == codes.NotFound {
		_, err := c.kms.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
			Parent:      ring,
			CryptoKeyId: key,
			CryptoKey:   &kmspb.CryptoKey{Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT},
		})
		if err != nil && status.Code(err) != codes.AlreadyExists {
			return false, fmt.Errorf("gcp: create key %s: %w", name, err)
		}
		created = true
	} else if err != nil {
		return false, fmt.Errorf("gcp: key %s: %w", name, err)
	}
	return created, nil
}

func (c *googleCloud) ReadObject(ctx context.Context, bucket, object string) ([]byte, error) {
	if err := c.clients(ctx); err != nil {
		return nil, err
	}
	r, err := c.storage.Bucket(bucket).Object(object).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, fmt.Errorf("gs://%s/%s: %w", bucket, object, fs.ErrNotExist)
	}
	if err != nil {
		return nil, fmt.Errorf("gcp: read gs://%s/%s: %w", bucket, object, err)
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

func (c *googleCloud) WriteObject(ctx context.Context, bucket, object string, data []byte) error {
	if err := c.clients(ctx); err != nil {
		return err
	}
	w := c.storage.Bucket(bucket).Object(object).NewWriter(ctx)
	w.ContentType = contentType(object)
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return fmt.Errorf("gcp: write gs://%s/%s: %w", bucket, object, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("gcp: write gs://%s/%s: %w", bucket, object, err)
	}
	return nil
}

// contentType is the content type of an object the target writes: a
// build context's tarball, or a JSON document.
func contentType(object string) string {
	if strings.HasSuffix(object, ".tar.gz") {
		return "application/gzip"
	}
	return "application/json"
}

func (c *googleCloud) DeleteObject(ctx context.Context, bucket, object string) error {
	if err := c.clients(ctx); err != nil {
		return err
	}
	err := c.storage.Bucket(bucket).Object(object).Delete(ctx)
	if err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("gcp: delete gs://%s/%s: %w", bucket, object, err)
	}
	return nil
}

func (c *googleCloud) EnsureSecret(ctx context.Context, project, secret string) (bool, error) {
	if err := c.clients(ctx); err != nil {
		return false, err
	}
	_, err := c.secrets.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/" + project,
		SecretId: secret,
		Secret: &secretmanagerpb.Secret{Replication: &secretmanagerpb.Replication{
			Replication: &secretmanagerpb.Replication_Automatic_{Automatic: &secretmanagerpb.Replication_Automatic{}},
		}},
	})
	if status.Code(err) == codes.AlreadyExists {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("gcp: create secret %s in %s: %w", secret, project, err)
	}
	return true, nil
}

// secretAccessor is the role that reads a secret's versions.
const secretAccessor = "roles/secretmanager.secretAccessor"

func (c *googleCloud) GrantSecretAccess(ctx context.Context, project, secret string, members []string) error {
	if err := c.clients(ctx); err != nil {
		return err
	}
	resource := secretResource(project, secret)
	policy, err := c.secrets.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: resource})
	if err != nil {
		return fmt.Errorf("gcp: the IAM policy of %s: %w", resource, err)
	}
	if !addMembers(policy, secretAccessor, members) {
		return nil
	}
	if _, err := c.secrets.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: resource, Policy: policy}); err != nil {
		return fmt.Errorf("gcp: grant %s on %s: %w", secretAccessor, resource, err)
	}
	return nil
}

// addMembers adds members to role's binding in policy and reports whether
// it changed anything.
func addMembers(policy *iampb.Policy, role string, members []string) bool {
	var binding *iampb.Binding
	for _, b := range policy.Bindings {
		if b.Role == role && b.Condition == nil {
			binding = b
			break
		}
	}
	if binding == nil {
		binding = &iampb.Binding{Role: role}
		policy.Bindings = append(policy.Bindings, binding)
	}
	changed := false
	for _, m := range members {
		if !slices.Contains(binding.Members, m) {
			binding.Members = append(binding.Members, m)
			changed = true
		}
	}
	return changed
}

func secretResource(project, secret string) string {
	return fmt.Sprintf("projects/%s/secrets/%s", project, secret)
}

func (c *googleCloud) SecretHasValue(ctx context.Context, project, secret string) (bool, error) {
	if err := c.clients(ctx); err != nil {
		return false, err
	}
	v, err := c.secrets.GetSecretVersion(ctx, &secretmanagerpb.GetSecretVersionRequest{Name: secretResource(project, secret) + "/versions/latest"})
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("gcp: secret %s in %s: %w", secret, project, err)
	}
	return v.State == secretmanagerpb.SecretVersion_ENABLED, nil
}

func (c *googleCloud) AddSecretVersion(ctx context.Context, project, secret string, value []byte) error {
	if err := c.clients(ctx); err != nil {
		return err
	}
	_, err := c.secrets.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secretResource(project, secret),
		Payload: &secretmanagerpb.SecretPayload{Data: value},
	})
	if status.Code(err) == codes.NotFound {
		return fmt.Errorf("gcp: secret %s in %s: %w", secret, project, registry.ErrSecretNotCreated)
	}
	if err != nil {
		// The error names the secret, never the value.
		return fmt.Errorf("gcp: add a version of secret %s in %s: %w", secret, project, err)
	}
	return nil
}

func (c *googleCloud) AccessSecret(ctx context.Context, project, secret string) ([]byte, error) {
	if err := c.clients(ctx); err != nil {
		return nil, err
	}
	v, err := c.secrets.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{Name: secretResource(project, secret) + "/versions/latest"})
	if err != nil {
		return nil, fmt.Errorf("gcp: read secret %s in %s: %w", secret, project, err)
	}
	return v.Payload.Data, nil
}

// tagResource returns the Artifact Registry resource name of an image's
// tag: `<region>-docker.pkg.dev/<project>/<repository>/<image>:<tag>` is
// projects/<project>/locations/<region>/repositories/<repository>/packages/<image>/tags/<tag>,
// with each slash of the image's name escaped.
func tagResource(image string) (string, error) {
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon < slash || colon < 0 {
		return "", fmt.Errorf("gcp: image %q names no tag", image)
	}
	ref, tag := image[:colon], image[colon+1:]
	host, rest, _ := strings.Cut(ref, "/")
	region, ok := strings.CutSuffix(host, "-docker.pkg.dev")
	parts := strings.SplitN(rest, "/", 3)
	if !ok || region == "" || len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || tag == "" {
		return "", fmt.Errorf("gcp: image %q is not <region>-docker.pkg.dev/<project>/<repository>/<image>:<tag>", image)
	}
	return fmt.Sprintf("projects/%s/locations/%s/repositories/%s/packages/%s/tags/%s",
		parts[0], region, parts[1], url.PathEscape(parts[2]), tag), nil
}

func (c *googleCloud) ImageDigest(ctx context.Context, image string) (string, error) {
	name, err := tagResource(image)
	if err != nil {
		return "", err
	}
	if err := c.clients(ctx); err != nil {
		return "", err
	}
	tag, err := c.registry.GetTag(ctx, &artifactregistrypb.GetTagRequest{Name: name})
	if status.Code(err) == codes.NotFound {
		return "", fmt.Errorf("gcp: %s: %w", image, fs.ErrNotExist)
	}
	if err != nil {
		return "", fmt.Errorf("gcp: the tag of %s: %w", image, err)
	}
	_, digest, ok := strings.Cut(tag.Version, "/versions/")
	if !ok || !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("gcp: the tag of %s names version %q, not a digest", image, tag.Version)
	}
	return digest, nil
}

// dockerStep is the Cloud Build step that builds and pushes an image. BuildKit
// reads the Dockerfile's cache mounts and its Dockerfile.dockerignore.
const dockerStep = "gcr.io/cloud-builders/docker"

func (c *googleCloud) RunBuild(ctx context.Context, project, region string, spec BuildSpec) (*BuildResult, error) {
	if err := c.clients(ctx); err != nil {
		return nil, err
	}
	build := &cloudbuildpb.Build{
		Source: &cloudbuildpb.Source{Source: &cloudbuildpb.Source_StorageSource{
			StorageSource: &cloudbuildpb.StorageSource{Bucket: spec.Bucket, Object: spec.Object},
		}},
		Steps: []*cloudbuildpb.BuildStep{{
			Name: dockerStep,
			Env:  []string{"DOCKER_BUILDKIT=1"},
			Args: []string{"build", "-f", spec.Dockerfile, "-t", spec.Image, "."},
		}},
		Images:         []string{spec.Image},
		ServiceAccount: "projects/" + project + "/serviceAccounts/" + spec.ServiceAccount,
		Timeout:        durationpb.New(spec.Timeout),
		Options: &cloudbuildpb.BuildOptions{
			// A build that names its account sends its logs to Cloud
			// Logging only, and the server images compile Rust and Go.
			Logging:     cloudbuildpb.BuildOptions_CLOUD_LOGGING_ONLY,
			MachineType: cloudbuildpb.BuildOptions_E2_HIGHCPU_8,
		},
	}
	op, err := c.builds.CreateBuild(ctx, &cloudbuildpb.CreateBuildRequest{
		Parent:    fmt.Sprintf("projects/%s/locations/%s", project, region),
		ProjectId: project,
		Build:     build,
	})
	if err != nil {
		return nil, fmt.Errorf("gcp: start the build of %s: %w", spec.Image, err)
	}
	logs := ""
	if meta, merr := op.Metadata(); merr == nil && meta.GetBuild() != nil {
		logs = meta.GetBuild().GetLogUrl()
	}
	done, err := op.Wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcp: the build of %s failed (logs: %s): %w", spec.Image, logs, err)
	}
	if done.GetStatus() != cloudbuildpb.Build_SUCCESS {
		return nil, fmt.Errorf("gcp: the build of %s ended %s: %s (logs: %s)", spec.Image, done.GetStatus(), done.GetStatusDetail(), done.GetLogUrl())
	}
	for _, image := range done.GetResults().GetImages() {
		if image.GetName() == spec.Image {
			return &BuildResult{ID: done.GetId(), LogURL: done.GetLogUrl(), Digest: image.GetDigest()}, nil
		}
	}
	return nil, fmt.Errorf("gcp: the build of %s pushed no image of that name (logs: %s)", spec.Image, done.GetLogUrl())
}

// jobResource is a job's resource name.
func jobResource(project, region, job string) string {
	return fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, region, job)
}

// jobOf is the job spec describes.
func jobOf(spec JobSpec) *runpb.Job {
	return &runpb.Job{
		Labels: spec.Labels,
		Template: &runpb.ExecutionTemplate{
			TaskCount:   1,
			Parallelism: 1,
			Template: &runpb.TaskTemplate{
				Containers:     []*runpb.Container{{Name: spec.Container, Image: spec.Image}},
				Retries:        &runpb.TaskTemplate_MaxRetries{MaxRetries: 0},
				Timeout:        durationpb.New(spec.Timeout),
				ServiceAccount: spec.ServiceAccount,
			},
		},
	}
}

// jobMatches reports whether a job runs what spec describes.
func jobMatches(job *runpb.Job, spec JobSpec) bool {
	task := job.GetTemplate().GetTemplate()
	containers := task.GetContainers()
	return len(containers) == 1 && containers[0].GetName() == spec.Container && containers[0].GetImage() == spec.Image &&
		task.GetServiceAccount() == spec.ServiceAccount && task.GetMaxRetries() == 0 &&
		task.GetTimeout().AsDuration() == spec.Timeout && job.GetTemplate().GetTaskCount() == 1
}

func (c *googleCloud) EnsureJob(ctx context.Context, project, region string, spec JobSpec) (bool, error) {
	if err := c.clients(ctx); err != nil {
		return false, err
	}
	name := jobResource(project, region, spec.Name)
	current, err := c.jobs.GetJob(ctx, &runpb.GetJobRequest{Name: name})
	switch {
	case status.Code(err) == codes.NotFound:
		op, err := c.jobs.CreateJob(ctx, &runpb.CreateJobRequest{
			Parent: fmt.Sprintf("projects/%s/locations/%s", project, region),
			JobId:  spec.Name,
			Job:    jobOf(spec),
		})
		if err != nil {
			return false, fmt.Errorf("gcp: create job %s: %w", name, err)
		}
		if _, err := op.Wait(ctx); err != nil {
			return false, fmt.Errorf("gcp: create job %s: %w", name, err)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("gcp: job %s: %w", name, err)
	case jobMatches(current, spec):
		return false, nil
	}
	job := jobOf(spec)
	job.Name = name
	op, err := c.jobs.UpdateJob(ctx, &runpb.UpdateJobRequest{Job: job})
	if err != nil {
		return false, fmt.Errorf("gcp: update job %s: %w", name, err)
	}
	if _, err := op.Wait(ctx); err != nil {
		return false, fmt.Errorf("gcp: update job %s: %w", name, err)
	}
	return true, nil
}

func (c *googleCloud) RunJob(ctx context.Context, project, region, job string, args []string) (*JobRun, error) {
	if err := c.clients(ctx); err != nil {
		return nil, err
	}
	name := jobResource(project, region, job)
	current, err := c.jobs.GetJob(ctx, &runpb.GetJobRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("gcp: job %s: %w", name, err)
	}
	container := ""
	if containers := current.GetTemplate().GetTemplate().GetContainers(); len(containers) == 1 {
		container = containers[0].GetName()
	}
	op, err := c.jobs.RunJob(ctx, &runpb.RunJobRequest{
		Name: name,
		Overrides: &runpb.RunJobRequest_Overrides{
			ContainerOverrides: []*runpb.RunJobRequest_Overrides_ContainerOverride{{Name: container, Args: args}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("gcp: run job %s: %w", name, err)
	}
	execution, err := op.Wait(ctx)
	if err != nil {
		// The execution failed: its metadata says where its logs are.
		out := &JobRun{Message: err.Error()}
		if meta, merr := op.Metadata(); merr == nil && meta != nil {
			out.Name, out.LogURI = meta.GetName(), meta.GetLogUri()
		}
		return out, nil
	}
	out := &JobRun{Name: execution.GetName(), LogURI: execution.GetLogUri()}
	out.Succeeded = execution.GetSucceededCount() == 1 && execution.GetFailedCount() == 0
	if !out.Succeeded {
		for _, cond := range execution.GetConditions() {
			if cond.GetMessage() != "" {
				out.Message = cond.GetMessage()
				break
			}
		}
	}
	return out, nil
}
