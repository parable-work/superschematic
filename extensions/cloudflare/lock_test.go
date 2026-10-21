package cloudflare_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The deploy lock on cloudflare (D46 and D64, amended): an object of the
// state bucket, superschematic/locks/<stack>/<run>.json, created with
// `If-None-Match: *` and deleted once a read shows it holds the ID asked
// for.

// storefrontLock is where storefront-stack's Staging lock sits in the
// state bucket.
const storefrontLock = "superschematic/locks/storefront-stack/Staging.json"

func lockOf(id, holder string) registry.DeployLock {
	return registry.DeployLock{
		ID: id, Run: "Staging", Operation: "deploy", Holder: holder, Host: "ci-runner", PID: 7,
		CI:   "https://github.com/acme/shop/actions/runs/99",
		Time: time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC),
	}
}

// TestStateLock takes, refuses, reads and releases a run's lock through
// the cloudflare state store over the fake Cloud: the lock is the object
// at its path, holding the lock's JSON; a second Lock is refused with the
// first's holder and leaves it; an Unlock of another ID leaves it too, and
// one of its ID deletes it. A member's lock is its own object. An object
// that holds no lock is refused with how to remove it.
func TestStateLock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	locker, ok := f.target(t).State.(registry.StateLocker)
	if !ok {
		t.Fatal("the cloudflare state store keeps no deploy lock")
	}
	run := storefrontRun(t, f)
	before := len(f.cloud.Changes())

	if held, err := locker.ReadLock(ctx, run); err != nil || held != nil {
		t.Fatalf("ReadLock before any deploy: %+v, %v", held, err)
	}
	first := lockOf("1111", "ana")
	if err := locker.Lock(ctx, run, first); err != nil {
		t.Fatal(err)
	}
	data, _, err := f.cloud.GetObject(ctx, accountID, cloudflare.StateBucket, storefrontLock)
	if err != nil {
		t.Fatalf("the lock is not at %s/%s: %v", cloudflare.StateBucket, storefrontLock, err)
	}
	var stored registry.DeployLock
	if err := json.Unmarshal(data, &stored); err != nil || stored != first {
		t.Errorf("the lock object holds %s (%v)", data, err)
	}
	for _, field := range []string{`"id": "1111"`, `"run": "Staging"`, `"operation": "deploy"`, `"holder": "ana"`, `"host": "ci-runner"`, `"pid": 7`, `"time": "2026-10-19T09:30:00Z"`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("the lock object lacks %s:\n%s", field, data)
		}
	}

	err = locker.Lock(ctx, run, lockOf("2222", "ben"))
	var held *registry.LockHeldError
	if !errors.As(err, &held) || held.Lock != first {
		t.Fatalf("a second Lock: %v", err)
	}
	if want := "the deploy lock of Staging is held by a deploy by ana on ci-runner (pid 7) in CI run https://github.com/acme/shop/actions/runs/99 since 2026-10-19T09:30:00Z"; err.Error() != want {
		t.Errorf("a second Lock: %q, want %q", err, want)
	}
	if got, _ := locker.ReadLock(ctx, run); got == nil || *got != first {
		t.Errorf("ReadLock = %+v", got)
	}
	if err := locker.Unlock(ctx, run, "2222"); !errors.As(err, &held) || held.Lock != first {
		t.Errorf("Unlock of another ID: %v", err)
	}
	if _, _, err := f.cloud.GetObject(ctx, accountID, cloudflare.StateBucket, storefrontLock); err != nil {
		t.Errorf("Unlock of another ID removed the lock: %v", err)
	}
	if err := locker.Unlock(ctx, run, "1111"); err != nil {
		t.Fatal(err)
	}
	if got, _ := locker.ReadLock(ctx, run); got != nil {
		t.Errorf("after Unlock, ReadLock = %+v", got)
	}
	if err := locker.Unlock(ctx, run, "1111"); err != nil {
		t.Errorf("Unlock with no lock: %v", err)
	}
	object := cloudflare.StateBucket + "/" + storefrontLock
	if changes, want := f.cloud.Changes()[before:], []string{"create " + object, "delete " + object}; !slices.Equal(changes, want) {
		t.Errorf("the bucket changed %q, want %q", changes, want)
	}

	// A member of a parameterized environment has a lock of its own.
	member := registry.Run{Environment: resolveCalls(t, f.reg, "Preview"), Parameters: map[string]string{"pr": "12"}}
	if err := locker.Lock(ctx, member, lockOf("3333", "ana")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.cloud.GetObject(ctx, accountID, cloudflare.StateBucket, "superschematic/locks/calls-stack/Preview.pr-12.json"); err != nil {
		t.Errorf("the member's lock: %v", err)
	}
	if err := locker.Lock(ctx, run, first); err != nil {
		t.Errorf("Staging's lock while a member holds its own: %v", err)
	}

	// An object that holds no lock.
	if err := f.cloud.PutObject(ctx, accountID, cloudflare.StateBucket, storefrontLock, []byte("not a lock"), nil); err != nil {
		t.Fatal(err)
	}
	_, err = locker.ReadLock(ctx, run)
	if err == nil || !strings.Contains(err.Error(), "holds no deploy lock superschematic reads") || !strings.Contains(err.Error(), "wrangler r2 object delete "+object+" --remote") {
		t.Errorf("ReadLock of an object that holds no lock: %v", err)
	}
	if err := locker.Lock(ctx, run, first); err == nil || errors.As(err, &held) {
		t.Errorf("Lock over an object that holds no lock: %v", err)
	}
	if err := locker.Unlock(ctx, run, "1111"); err == nil || errors.As(err, &held) {
		t.Errorf("Unlock of an object that holds no lock: %v", err)
	}
}

// inFlightCloud answers the first conditional write of a key as R2
// answers one while another conditional write to the key is in flight
// (409), with nothing there yet: the other write did not land.
type inFlightCloud struct {
	*fakeCloud
	refused int
}

func (c *inFlightCloud) CreateObject(ctx context.Context, account, bucket, key string, data []byte) (bool, error) {
	if c.refused == 0 {
		c.refused++
		return false, nil
	}
	return c.fakeCloud.CreateObject(ctx, account, bucket, key, data)
}

// TestStateLockAfterAWriteInFlight: a Lock whose conditional write lost to
// one in flight, which then left nothing, finds no lock and writes again.
func TestStateLockAfterAWriteInFlight(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	run := storefrontRun(t, f)
	cloud := &inFlightCloud{fakeCloud: f.cloud}
	reg, err := registry.Assemble(registry.DefaultNaming(), cloudflare.Extension{Cloud: cloud, Getenv: f.shell.getenv}, pulumiStub{})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := reg.Target(cloudflare.Target)
	if err := target.State.(registry.StateLocker).Lock(ctx, run, lockOf("1111", "ana")); err != nil {
		t.Fatal(err)
	}
	if cloud.refused != 1 {
		t.Errorf("the first write was refused %d times", cloud.refused)
	}
	if got, err := target.State.(registry.StateLocker).ReadLock(ctx, run); err != nil || got == nil || got.ID != "1111" {
		t.Errorf("ReadLock = %+v, %v", got, err)
	}
}

// TestDeployTakesTheLockOnCloudflare deploys storefront-stack's Staging
// through the cloudflare target over the fake Cloud: while the first step
// applies, the lock object is in the state bucket and a second deploy is
// refused with its holder and the unlock command; once the deploy ends the
// object is gone. A lock a crash left refuses the next deploy, naming its
// holder, until stack.Unlock removes it.
func TestDeployTakesTheLockOnCloudflare(t *testing.T) {
	f := newFixture(t)
	f.shell[cloudflare.PassphraseEnv] = "the-passphrase"
	ctx := context.Background()
	if _, err := f.cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	run := storefrontRun(t, f)
	root := t.TempDir()
	pkg := filepath.Join(root, "dist", "server", "storefront-stack", "shop-storefront")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "bundle.ts"), []byte("// written by the server generator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	build := &bundleBuild{version: "v1"}
	log := &bytes.Buffer{}
	options := func() stack.DeployOptions {
		return stack.DeployOptions{
			Options: stack.Options{Registry: f.reg, Run: run, Dir: t.TempDir(), Log: log},
			Sources: &stack.Sources{OutputRoot: filepath.Join(root, "dist"), RepositoryRoot: root, ImplementationRoot: root, Run: build.run},
		}
	}
	var during struct {
		lock   []byte
		second error
	}
	f.prov.OnApply = func(string) {
		f.prov.OnApply = nil
		during.lock, _, _ = f.cloud.GetObject(ctx, accountID, cloudflare.StateBucket, storefrontLock)
		_, during.second = stack.Deploy(ctx, options())
	}
	if _, err := stack.Deploy(ctx, options()); err != nil {
		t.Fatalf("deploy: %v\n%s", err, log)
	}
	var lock registry.DeployLock
	if err := json.Unmarshal(during.lock, &lock); err != nil || lock.Run != "Staging" || lock.Operation != "deploy" || lock.ID == "" || lock.Host == "" {
		t.Errorf("while the deploy ran, the lock object held %s (%v)", during.lock, err)
	}
	if !errors.Is(during.second, registry.ErrLockHeld) || !strings.Contains(during.second.Error(), "superschematic stack unlock Staging") {
		t.Errorf("a second deploy while the first ran: %v", during.second)
	}
	if _, _, err := f.cloud.GetObject(ctx, accountID, cloudflare.StateBucket, storefrontLock); err == nil {
		t.Error("the lock object is still there after the deploy")
	}

	// A crash's lock.
	if err := f.target(t).State.(registry.StateLocker).Lock(ctx, run, lockOf("4444", "ana")); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Deploy(ctx, options()); !errors.Is(err, registry.ErrLockHeld) || !strings.Contains(err.Error(), "held by a deploy by ana on ci-runner") {
		t.Errorf("a deploy a crash's lock refuses: %v", err)
	}
	removed, err := stack.Unlock(ctx, stack.UnlockOptions{Options: stack.Options{Registry: f.reg, Run: run}})
	if err != nil || removed == nil || removed.ID != "4444" {
		t.Fatalf("Unlock removed %+v, %v", removed, err)
	}
	if _, err := stack.Deploy(ctx, options()); err != nil {
		t.Errorf("a deploy once the lock is gone: %v\n%s", err, log)
	}
}
