package gcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// The deploy lock on gcp (D46, amended): an object of the state bucket,
// superschematic/locks/<stack>/<run>.json, created with
// ifGenerationMatch=0 and deleted at the generation that holds the lock's
// ID.

// stagingLock is where Staging's lock sits in its state bucket.
const stagingLock = "superschematic/locks/shop/Staging.json"

func lockOf(id, holder string) registry.DeployLock {
	return registry.DeployLock{
		ID: id, Run: "Staging", Operation: "deploy", Holder: holder, Host: "ci-runner", PID: 7,
		CI:   "https://github.com/acme/shop/actions/runs/99",
		Time: time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC),
	}
}

// TestStateLock takes, refuses, reads and releases a run's lock through
// the gcp state store over the fake Cloud: the lock is the object at its
// path, holding the lock's JSON; a second Lock is refused with the first's
// holder and leaves it; an Unlock of another ID leaves it too, and one of
// its ID deletes it. A member's lock is its own object. An object that
// holds no lock is refused with how to remove it.
func TestStateLock(t *testing.T) {
	f := newDeployFixture(t, false)
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
	target, _ := f.reg.Target(gcp.Target)
	locker, ok := target.State.(registry.StateLocker)
	if !ok {
		t.Fatal("the gcp state store keeps no deploy lock")
	}
	ctx := context.Background()
	run := registry.Run{Environment: env}

	if held, err := locker.ReadLock(ctx, run); err != nil || held != nil {
		t.Fatalf("ReadLock before any deploy: %+v, %v", held, err)
	}
	first := lockOf("1111", "ana")
	if err := locker.Lock(ctx, run, first); err != nil {
		t.Fatal(err)
	}
	data, err := f.cloud.ReadObject(ctx, stateBucket, stagingLock)
	if err != nil {
		t.Fatalf("the lock is not at gs://%s/%s: %v", stateBucket, stagingLock, err)
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
	if _, err := f.cloud.ReadObject(ctx, stateBucket, stagingLock); err != nil {
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
	changes := f.cloud.Changes()
	if want := []string{"create gs://" + stateBucket + "/" + stagingLock, "delete gs://" + stateBucket + "/" + stagingLock}; !slices.Equal(changes, want) {
		t.Errorf("the bucket changed %q, want %q", changes, want)
	}

	// A member of Preview, in Staging's project, has a lock of its own.
	preview := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Preview")
	member := registry.Run{Environment: preview, Parameters: map[string]string{"pr": "12"}}
	if err := locker.Lock(ctx, member, lockOf("3333", "ana")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cloud.ReadObject(ctx, stateBucket, "superschematic/locks/shop/Preview.pr-12.json"); err != nil {
		t.Errorf("the member's lock: %v", err)
	}
	if err := locker.Lock(ctx, run, first); err != nil {
		t.Errorf("Staging's lock while a member holds its own: %v", err)
	}

	// An object that holds no lock.
	if err := f.cloud.WriteObject(ctx, stateBucket, stagingLock, []byte("not a lock")); err != nil {
		t.Fatal(err)
	}
	_, err = locker.ReadLock(ctx, run)
	if err == nil || !strings.Contains(err.Error(), "holds no deploy lock superschematic reads") || !strings.Contains(err.Error(), "gcloud storage rm gs://"+stateBucket+"/"+stagingLock) {
		t.Errorf("ReadLock of an object that holds no lock: %v", err)
	}
	if err := locker.Lock(ctx, run, first); err == nil || errors.As(err, &held) {
		t.Errorf("Lock over an object that holds no lock: %v", err)
	}
}

// TestDeployTakesTheLockOnGCP deploys Staging through the gcp target over
// the fake Cloud: while the infrastructure step applies, the lock object
// is in the state bucket and a second deploy is refused with its holder;
// once the deploy ends the object is gone. A lock a crash left refuses the
// next deploy until stack.Unlock removes it.
func TestDeployTakesTheLockOnGCP(t *testing.T) {
	f := newDeployFixture(t, true)
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
	ctx := context.Background()
	if _, err := f.cloud.EnsureSecret(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := f.cloud.AddSecretVersion(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY", []byte("sk")); err != nil {
		t.Fatal(err)
	}
	options := func() stack.DeployOptions {
		return stack.DeployOptions{
			Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()},
			Images:  shopImages("acme-staging", 1),
			Planner: shopPlanner,
		}
	}
	var during struct {
		lock   []byte
		second error
	}
	f.prov.OnApply = func(step string) {
		if step != "infrastructure" {
			return
		}
		f.prov.OnApply = nil
		during.lock, _ = f.cloud.ReadObject(ctx, stateBucket, stagingLock)
		_, during.second = stack.Deploy(ctx, options())
	}
	if _, err := stack.Deploy(ctx, options()); err != nil {
		t.Fatal(err)
	}
	var lock registry.DeployLock
	if err := json.Unmarshal(during.lock, &lock); err != nil || lock.Run != "Staging" || lock.Operation != "deploy" || lock.ID == "" || lock.Host == "" {
		t.Errorf("while the deploy ran, the lock object held %s (%v)", during.lock, err)
	}
	if !errors.Is(during.second, registry.ErrLockHeld) || !strings.Contains(during.second.Error(), "superschematic stack unlock Staging") {
		t.Errorf("a second deploy while the first ran: %v", during.second)
	}
	if _, err := f.cloud.ReadObject(ctx, stateBucket, stagingLock); err == nil {
		t.Error("the lock object is still there after the deploy")
	}

	// A crash's lock.
	target, _ := f.reg.Target(gcp.Target)
	if err := target.State.(registry.StateLocker).Lock(ctx, registry.Run{Environment: env}, lockOf("4444", "ana")); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Deploy(ctx, options()); !errors.Is(err, registry.ErrLockHeld) || !strings.Contains(err.Error(), "held by a deploy by ana on ci-runner") {
		t.Errorf("a deploy a crash's lock refuses: %v", err)
	}
	removed, err := stack.Unlock(ctx, stack.UnlockOptions{Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}}})
	if err != nil || removed == nil || removed.ID != "4444" {
		t.Fatalf("Unlock removed %+v, %v", removed, err)
	}
	if _, err := stack.Deploy(ctx, options()); err != nil {
		t.Errorf("a deploy once the lock is gone: %v", err)
	}
}
