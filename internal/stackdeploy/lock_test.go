package stackdeploy_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	"github.com/parable-work/superschematic/registry"
)

// The deploy lock on the fake target (D46, amended).

// crashed is the lock a deploy whose process died left on a run an hour
// before fixedNow.
func crashed(run string) registry.DeployLock {
	return registry.DeployLock{
		ID: "0123456789abcdef", Run: run, Operation: "deploy",
		Holder: "ana", Host: "ana-laptop", PID: 4242,
		Time: fixedNow().Add(-time.Hour),
	}
}

// TestDeployHoldsTheLock: a deploy holds the run's lock from before it
// reads the manifest until it ends. A second deploy, a destroy and a plan
// of the run while it holds it: the deploy and the destroy fail at once,
// naming who holds the lock and since when, and change nothing; the plan
// says a deploy holds it. A deploy of another run of the environment is
// not kept out. Once the deploy ends the lock is gone.
func TestDeployHoldsTheLock(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	preview := f.env(t, "Preview")
	f.ready(t, env)
	ctx := context.Background()
	run := registry.Run{Environment: env}

	var during struct {
		deploy, destroy, member error
		plan                    *stackdeploy.PlanResult
		calls                   int
		manifests               int
	}
	f.ext.Provisioner.OnApply = func(step string) {
		if step != "infrastructure" || during.calls > 0 {
			return
		}
		f.ext.Provisioner.OnApply = nil
		during.calls = len(f.ext.Provisioner.Calls())
		_, during.deploy = stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(2)))
		during.destroy = stackdeploy.Destroy(ctx, f.options(t, env, nil))
		during.plan, _ = stackdeploy.Plan(ctx, stackdeploy.PlanOptions{Options: f.options(t, env, nil), Images: images(2), Planner: (&planner{to: 1}).plan})
		_, during.member = stackdeploy.Deploy(ctx, deployOptions(f, t, preview, map[string]string{"pr": "7"}, &planner{to: 1}, images(1)))
		during.calls = len(f.ext.Provisioner.Calls()) - during.calls
	}
	m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1)))
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != stackdeploy.StatusDeployed {
		t.Errorf("the deploy that held the lock: %s", m.Status)
	}
	for name, err := range map[string]error{"deploy": during.deploy, "destroy": during.destroy} {
		var held *registry.LockHeldError
		if !errors.As(err, &held) || !errors.Is(err, registry.ErrLockHeld) {
			t.Fatalf("a %s while a deploy runs: %v", name, err)
		}
		msg := err.Error()
		for _, want := range []string{
			"the deploy lock of Staging is held by a deploy by ",
			" since 2026-10-06T12:00:00Z, ",
			" ago; wait for it to end, or, when it no longer runs, remove its lock with `superschematic stack unlock Staging`",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("a %s while a deploy runs: %q lacks %q", name, msg, want)
			}
		}
		if held.Lock.Operation != "deploy" || held.Lock.Run != "Staging" || held.Lock.Host == "" || held.Lock.PID == 0 || held.Lock.ID == "" {
			t.Errorf("the lock a %s was refused with: %+v", name, held.Lock)
		}
	}
	if during.plan == nil || during.plan.Lock == nil || during.plan.Lock.Operation != "deploy" {
		t.Errorf("a plan while a deploy runs says of the lock: %+v", during.plan)
	}
	if during.member != nil {
		t.Errorf("a deploy of Preview.pr-7 while Staging deploys: %v", during.member)
	}
	// The refused deploy and destroy ran nothing: the calls between are
	// the plan's and the member's.
	if during.calls == 0 {
		t.Error("the member's deploy ran nothing")
	}
	if held, err := f.ext.State.ReadLock(ctx, run); err != nil || held != nil {
		t.Errorf("after the deploy, the lock is %+v, %v", held, err)
	}
	if f.ext.State.Locked != 2 || f.ext.State.Unlocked != 2 {
		t.Errorf("took %d locks and released %d, want two of each: Staging's and the member's", f.ext.State.Locked, f.ext.State.Unlocked)
	}
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(2))); err != nil {
		t.Errorf("the next deploy: %v", err)
	}
}

// TestDeployReleasesTheLockWhenItFails: a deploy that fails at a step, and
// one the gate refuses before its first step, release the lock, so the
// next deploy runs. So does a destroy.
func TestDeployReleasesTheLockWhenItFails(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	run := registry.Run{Environment: env}
	unlocked := func(what string) {
		t.Helper()
		if held, err := f.ext.State.ReadLock(ctx, run); err != nil || held != nil {
			t.Errorf("after %s, the lock is %+v, %v", what, held, err)
		}
	}

	f.ext.Provisioner.Fail = map[string]error{"rollout 1": errors.New("shop-api is not ready")}
	m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1)))
	if err == nil || !strings.Contains(err.Error(), "shop-api is not ready") || m.Status != stackdeploy.StatusFailed {
		t.Fatalf("a failed rollout: %v", err)
	}
	unlocked("a failed rollout")

	f.ext.Provisioner.Fail = nil
	gated := deployOptions(f, t, env, nil, &planner{to: 1}, images(1))
	gated.Gate = stackdeploy.Gate{}
	if _, err := stackdeploy.Deploy(ctx, gated); err != nil {
		t.Fatalf("a deploy with nothing destructive: %v", err)
	}
	gated.Planner = (&planner{to: 2}).plan
	var hazards *stackdeploy.HazardsError
	if _, err := stackdeploy.Deploy(ctx, gated); !errors.As(err, &hazards) {
		t.Fatalf("a deploy the gate refuses: %v", err)
	}
	unlocked("a deploy the gate refused")

	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 2}, images(2))); err != nil {
		t.Fatalf("the next deploy: %v", err)
	}
	if err := stackdeploy.Destroy(ctx, f.options(t, env, nil)); err != nil {
		t.Fatal(err)
	}
	unlocked("a destroy")

	// A lock that cannot be taken stops the deploy before it reads
	// anything; one that cannot be released fails a deploy that ran, and
	// says how to remove it.
	f.ext.State.LockFail = errors.New("the bucket is gone")
	n := len(f.ext.Provisioner.Calls())
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1))); err == nil || !strings.Contains(err.Error(), "take the deploy lock of Staging: the bucket is gone") {
		t.Errorf("a lock that cannot be taken: %v", err)
	}
	if calls := f.calls(n); len(calls) > 0 {
		t.Errorf("a deploy with no lock ran %v", calls)
	}
	f.ext.State.LockFail = nil
	f.ext.State.UnlockFail = errors.New("the connection dropped")
	m, err = stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1)))
	if err == nil || !strings.Contains(err.Error(), "release the deploy lock of Staging: the connection dropped; the next deploy is refused until `superschematic stack unlock Staging` removes it") {
		t.Errorf("a lock that cannot be released: %v", err)
	}
	if m == nil || m.Status != stackdeploy.StatusDeployed {
		t.Errorf("the deploy whose lock stayed: %+v", m)
	}
}

// TestUnlock: a lock a deploy that died left refuses every deploy of the
// run, naming who left it and how long ago. Unlock shows it, keeps it when
// the person declines, and removes it when they confirm; then a deploy
// runs. With no lock, Unlock removes nothing. A member's lock is its own,
// and the refusal names the member's parameters.
func TestUnlock(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	run := registry.Run{Environment: env}
	if err := f.ext.State.Lock(ctx, run, crashed("Staging")); err != nil {
		t.Fatal(err)
	}

	_, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1)))
	want := "the deploy lock of Staging is held by a deploy by ana on ana-laptop (pid 4242) since 2026-10-06T11:00:00Z, 1h0m0s ago; " +
		"wait for it to end, or, when it no longer runs, remove its lock with `superschematic stack unlock Staging`"
	if err == nil || err.Error() != want {
		t.Fatalf("a deploy of a run a crash left locked:\n%v\nwant:\n%s", err, want)
	}

	var shown []registry.DeployLock
	declined := stackdeploy.UnlockOptions{Options: f.options(t, env, nil), Confirm: func(l registry.DeployLock) (bool, error) {
		shown = append(shown, l)
		return false, nil
	}}
	if removed, err := stackdeploy.Unlock(ctx, declined); !errors.Is(err, stackdeploy.ErrUnlockDeclined) || removed != nil {
		t.Errorf("an unlock the person declines: %+v, %v", removed, err)
	}
	if held, _ := f.ext.State.ReadLock(ctx, run); held == nil {
		t.Error("a declined unlock removed the lock")
	}
	confirmed := declined
	confirmed.Confirm = func(l registry.DeployLock) (bool, error) {
		shown = append(shown, l)
		return true, nil
	}
	removed, err := stackdeploy.Unlock(ctx, confirmed)
	if err != nil || removed == nil || *removed != crashed("Staging") {
		t.Fatalf("unlock removed %+v, %v", removed, err)
	}
	if len(shown) != 2 || shown[0] != crashed("Staging") {
		t.Errorf("the person was shown %+v", shown)
	}
	if !strings.Contains(f.log.String(), "removed the deploy lock of Staging, which a deploy by ana on ana-laptop (pid 4242) since 2026-10-06T11:00:00Z held") {
		t.Errorf("the log:\n%s", f.log)
	}
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1))); err != nil {
		t.Fatalf("a deploy once the lock is gone: %v", err)
	}
	if removed, err := stackdeploy.Unlock(ctx, confirmed); err != nil || removed != nil {
		t.Errorf("an unlock of a run with no lock: %+v, %v", removed, err)
	}
	if len(shown) != 2 {
		t.Errorf("an unlock with no lock asked the person: %+v", shown[2:])
	}

	// A member's lock.
	preview := f.env(t, "Preview")
	member := registry.Run{Environment: preview, Parameters: map[string]string{"pr": "7"}}
	if err := f.ext.State.Lock(ctx, member, crashed("Preview.pr-7")); err != nil {
		t.Fatal(err)
	}
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1))); err != nil {
		t.Errorf("Staging while a member is locked: %v", err)
	}
	_, err = stackdeploy.Deploy(ctx, deployOptions(f, t, preview, map[string]string{"pr": "7"}, &planner{to: 1}, images(1)))
	if err == nil || !strings.Contains(err.Error(), "`superschematic stack unlock Preview --param pr=7`") {
		t.Errorf("a locked member: %v", err)
	}
	if removed, err := stackdeploy.Unlock(ctx, stackdeploy.UnlockOptions{Options: f.options(t, preview, map[string]string{"pr": "7"})}); err != nil || removed == nil || removed.Run != "Preview.pr-7" {
		t.Errorf("unlock of the member: %+v, %v", removed, err)
	}
}

// TestReleaseKeepsALockAnotherTook: a person removed a running deploy's
// lock and another deploy took it; when the first ends, it leaves the
// second's lock in place and says the two may have run at once. An unlock
// whose lock changed hands after it read it removes nothing.
func TestReleaseKeepsALockAnotherTook(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	run := registry.Run{Environment: env}
	other := crashed("Staging")
	other.ID, other.Holder = "fedcba9876543210", "ben"

	f.ext.Provisioner.OnApply = func(step string) {
		if step != "rollout 1" {
			return
		}
		f.ext.Provisioner.OnApply = nil
		if _, err := stackdeploy.Unlock(ctx, stackdeploy.UnlockOptions{Options: f.options(t, env, nil)}); err != nil {
			t.Fatal(err)
		}
		if err := f.ext.State.Lock(ctx, run, other); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1))); err != nil {
		t.Fatal(err)
	}
	if held, _ := f.ext.State.ReadLock(ctx, run); held == nil || *held != other {
		t.Errorf("the lock after the first deploy ended: %+v", held)
	}
	if !strings.Contains(f.log.String(), "the deploy lock of Staging was removed while this deploy ran, and a deploy by ben on ana-laptop (pid 4242) since 2026-10-06T11:00:00Z holds it now: the two may have run at once") {
		t.Errorf("the log:\n%s", f.log)
	}

	_, err := stackdeploy.Unlock(ctx, stackdeploy.UnlockOptions{Options: f.options(t, env, nil), Confirm: func(registry.DeployLock) (bool, error) {
		// The other deploy ends and a third takes the lock while the
		// person reads.
		if err := f.ext.State.Unlock(ctx, run, other.ID); err != nil {
			return false, err
		}
		third := other
		third.ID = "aaaaaaaaaaaaaaaa"
		return true, f.ext.State.Lock(ctx, run, third)
	}})
	if err == nil || !strings.Contains(err.Error(), "the deploy lock of Staging changed hands since it was read") {
		t.Errorf("an unlock of a lock that changed hands: %v", err)
	}
	if held, _ := f.ext.State.ReadLock(ctx, run); held == nil || held.ID != "aaaaaaaaaaaaaaaa" {
		t.Errorf("the third deploy's lock: %+v", held)
	}
}
