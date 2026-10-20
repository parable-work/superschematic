package stackdeploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"sort"
	"strings"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
)

// The deploy lock (D46, amended). A deploy and a destroy take the run's lock
// before they read its manifest, so no other deploy plans from a manifest
// they are about to replace, and release it when they end, whether they
// succeed or fail. A second deploy of the run while one holds it fails at
// once and says who holds it and since when. The lock is held, not leased:
// a deploy whose process died leaves it, and `stack unlock` removes it after
// a person checks that deploy no longer runs. A lease that expired while
// its holder was paused, its machine asleep or its connection gone, would
// let a second deploy start beside the first, which then goes on writing
// the provisioner's state, the migrations and the manifest; nothing the
// deploy writes could check a fencing token, Pulumi's state least of all.
//
// The lock is the target's: its state store keeps it beside the manifests
// when it is a registry.StateLocker. With a store that is not, the deploy
// runs without one and says so.

// The operations that hold the lock.
const (
	opDeploy  = "deploy"
	opDestroy = "destroy"
)

// releaseTimeout bounds the release of a lock, which runs on a context of
// its own, so a deploy that was interrupted still releases it.
const releaseTimeout = 30 * time.Second

// lock takes the run's deploy lock for op, when the target's state store
// keeps one, and returns what releases it. A lock another holds is refused
// with what holds it, how long ago it was taken, and the command that
// removes one a crash left.
func (s *session) lock(ctx context.Context, op string, now func() time.Time) (release func() error, err error) {
	locker, ok := s.target.State.(registry.StateLocker)
	if !ok {
		s.logf("target %s keeps no deploy lock: nothing keeps another deploy of %s from running beside this one", s.target.Name, s.run.Name())
		return func() error { return nil }, nil
	}
	held := newLock(op, s.run.Name(), now)
	if err := locker.Lock(ctx, s.run, held); err != nil {
		var refused *registry.LockHeldError
		if errors.As(err, &refused) {
			return nil, fmt.Errorf("%w, %s ago; wait for it to end, or, when it no longer runs, remove its lock with `%s`",
				err, since(refused.Lock.Time, now), unlockCommand(s.run))
		}
		return nil, fmt.Errorf("take the deploy lock of %s: %w", s.run.Name(), err)
	}
	return func() error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancel()
		err := locker.Unlock(ctx, s.run, held.ID)
		var taken *registry.LockHeldError
		if errors.As(err, &taken) {
			// A person removed this deploy's lock while it ran, and another
			// took it: that one keeps it.
			s.logf("the deploy lock of %s was removed while this %s ran, and %s holds it now: the two may have run at once", s.run.Name(), op, taken.Lock)
			return nil
		}
		if err != nil {
			return fmt.Errorf("release the deploy lock of %s: %w; the next deploy is refused until `%s` removes it", s.run.Name(), err, unlockCommand(s.run))
		}
		return nil
	}, nil
}

// newLock is a new hold of a run's lock for op: a random ID, who runs it,
// where, and now.
func newLock(op, run string, now func() time.Time) registry.DeployLock {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	host, _ := os.Hostname()
	return registry.DeployLock{
		ID:        hex.EncodeToString(id),
		Run:       run,
		Operation: op,
		Holder:    holder(),
		Host:      host,
		PID:       os.Getpid(),
		CI:        ciRun(),
		Time:      now().UTC(),
	}
}

// holder names who runs the deploy: the actor of a GitHub Actions run, else
// the user.
func holder() string {
	if os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("GITHUB_ACTOR") != "" {
		return os.Getenv("GITHUB_ACTOR")
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, name := range []string{"USER", "USERNAME", "LOGNAME"} {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// ciRun is the URL of the GitHub Actions run the deploy runs in, empty
// outside one.
func ciRun() string {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return ""
	}
	server, repo, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID")
	if server == "" || repo == "" || id == "" {
		return ""
	}
	url := server + "/" + repo + "/actions/runs/" + id
	if attempt := os.Getenv("GITHUB_RUN_ATTEMPT"); attempt != "" && attempt != "1" {
		url += "/attempts/" + attempt
	}
	return url
}

// since says how long ago t was, to the second.
func since(t time.Time, now func() time.Time) string {
	d := now().Sub(t).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}

// unlockCommand is the command that removes the run's lock, with its
// parameters.
func unlockCommand(run registry.Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "superschematic stack unlock %s", run.Environment.Environment)
	params := make([]string, 0, len(run.Parameters))
	for name := range run.Parameters {
		params = append(params, name)
	}
	sort.Strings(params)
	for _, name := range params {
		fmt.Fprintf(&b, " --param %s=%s", name, run.Parameters[name])
	}
	return b.String()
}

// UnlockOptions is one `stack unlock`.
type UnlockOptions struct {
	Options

	// Confirm is shown the lock the run holds and says whether to remove
	// it. Nil removes it without asking.
	Confirm func(lock registry.DeployLock) (bool, error)
}

// ErrUnlockDeclined is what Unlock returns when Confirm declines.
var ErrUnlockDeclined = errors.New("the lock stays: nothing was removed")

// Unlock removes a run's deploy lock, which a deploy or a destroy leaves
// when its process dies before it ends (D46, amended). It reads the lock,
// has Confirm decide, and removes that lock only: a lock another deploy
// took meanwhile stays, and Unlock says so. It returns the lock it
// removed, or nil when the run holds none. It refuses a target whose state
// store keeps no lock.
func Unlock(ctx context.Context, o UnlockOptions) (*registry.DeployLock, error) {
	s, err := open(o.Options)
	if err != nil {
		return nil, err
	}
	if s.target.State == nil {
		return nil, fmt.Errorf("target %s keeps no deploy state, so environment %s holds no deploy lock", s.target.Name, s.env.Environment)
	}
	locker, ok := s.target.State.(registry.StateLocker)
	if !ok {
		return nil, fmt.Errorf("target %s keeps no deploy lock, so %s holds none", s.target.Name, s.run.Name())
	}
	held, err := locker.ReadLock(ctx, s.run)
	if err != nil {
		return nil, fmt.Errorf("read the deploy lock of %s: %w", s.run.Name(), err)
	}
	if held == nil {
		s.logf("no deploy holds the lock of %s", s.run.Name())
		return nil, nil
	}
	if o.Confirm != nil {
		ok, err := o.Confirm(*held)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrUnlockDeclined
		}
	}
	if err := locker.Unlock(ctx, s.run, held.ID); err != nil {
		var taken *registry.LockHeldError
		if errors.As(err, &taken) {
			return nil, fmt.Errorf("the deploy lock of %s changed hands since it was read, and %s holds it now; it stays", s.run.Name(), taken.Lock)
		}
		return nil, fmt.Errorf("remove the deploy lock of %s: %w", s.run.Name(), err)
	}
	s.logf("removed the deploy lock of %s, which %s held", s.run.Name(), held)
	return held, nil
}

// readLock returns the run's lock when the target's state store keeps one
// and a deploy holds it, for a plan to say so.
func (s *session) readLock(ctx context.Context) (*registry.DeployLock, error) {
	locker, ok := s.target.State.(registry.StateLocker)
	if !ok {
		return nil, nil
	}
	held, err := locker.ReadLock(ctx, s.run)
	if err != nil {
		return nil, fmt.Errorf("read the deploy lock of %s: %w", s.run.Name(), err)
	}
	return held, nil
}
