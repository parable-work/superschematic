package gcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"sync"

	"cloud.google.com/go/iam/apiv1/iampb"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	serviceusage "cloud.google.com/go/serviceusage/apiv1"
	"cloud.google.com/go/serviceusage/apiv1/serviceusagepb"
	"cloud.google.com/go/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	w.ContentType = "application/json"
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return fmt.Errorf("gcp: write gs://%s/%s: %w", bucket, object, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("gcp: write gs://%s/%s: %w", bucket, object, err)
	}
	return nil
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
