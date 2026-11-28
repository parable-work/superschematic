package tasks

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/registry"
)

// A schema input resolves to a static set that is wider than what one
// build of its service reads (resolveSchema): the service, each service
// it names, and the schemas root's shared files. How much wider decides
// what the input is worth: a set that every edit changes keys every task
// that reads schemas on every schema commit, and reuses nothing. Reach
// measures it on a repository's own history.

// ReachCommit is one commit's reach: of the services its schemas root
// holds, how many it edited, and how many schema inputs' digests it
// changed.
type ReachCommit struct {
	Commit string `json:"commit"`
	// Services is the number of services of the root at the commit.
	Services int `json:"services"`
	// Edited is the number of services whose own directory the commit
	// changed.
	Edited int `json:"edited"`
	// Changed is the number of services whose schema input's digest the
	// commit changed.
	Changed int `json:"changed"`
	// Shared reports that the commit changed a file of the root that
	// belongs to no service, which is in every service's set.
	Shared bool `json:"shared"`
}

// Reach is the reach of the commits that changed a schemas root.
type Reach struct {
	// Root is the schemas root, from the repository root.
	Root string `json:"root"`
	// Commits are the measured commits, newest first.
	Commits []ReachCommit `json:"commits"`
	// Skipped counts the commits left out: a commit with no parent, and
	// one that the root begins or ends at.
	Skipped int `json:"skipped"`
}

// All, Some and None count the commits that changed every schema
// input's digest, some of them, and none.
func (r *Reach) All() int {
	return r.count(func(c ReachCommit) bool { return c.Changed == c.Services })
}
func (r *Reach) None() int { return r.count(func(c ReachCommit) bool { return c.Changed == 0 }) }
func (r *Reach) Some() int {
	return r.count(func(c ReachCommit) bool { return c.Changed > 0 && c.Changed < c.Services })
}

func (r *Reach) count(keep func(ReachCommit) bool) int {
	n := 0
	for _, c := range r.Commits {
		if keep(c) {
			n++
		}
	}
	return n
}

// SchemaReach walks up to n commits that changed the schemas root at
// dir, back from revision along first parents, and measures each against
// its parent: the digest of every service's schema input, before and
// after.
func SchemaReach(repo *Repository, dir, revision string, n int) (*Reach, error) {
	history, err := repo.Log(revision, dir, n)
	if err != nil {
		return nil, err
	}
	repo.CacheBlobs()
	reach := &Reach{Root: dir, Commits: []ReachCommit{}}
	// Each commit's digests, kept for the walk: a commit is measured as
	// one commit's child and, most often, as the next one's parent.
	digests := map[string]*rootDigests{}
	at := func(commit string) (*rootDigests, error) {
		if d, ok := digests[commit]; ok {
			return d, nil
		}
		d, err := digestsOf(repo, commit, dir)
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", commit, err)
		}
		digests[commit] = d
		return d, nil
	}
	for _, entry := range history {
		if entry.Parent == "" {
			reach.Skipped++
			continue
		}
		after, err := at(entry.Commit)
		if err != nil {
			return nil, err
		}
		before, err := at(entry.Parent)
		if err != nil {
			return nil, err
		}
		if after == nil || before == nil {
			reach.Skipped++
			continue
		}
		measured := ReachCommit{Commit: entry.Commit, Services: len(after.inputs), Shared: after.shared != before.shared}
		for service, digest := range after.inputs {
			if before.inputs[service] != digest {
				measured.Changed++
			}
			if before.own[service] != after.own[service] {
				measured.Edited++
			}
		}
		reach.Commits = append(reach.Commits, measured)
		delete(digests, entry.Commit)
	}
	return reach, nil
}

// rootDigests is a schemas root at a commit: the digest of each
// service's schema input, of each service's own directory, and of the
// root's files that belong to no service.
type rootDigests struct {
	inputs map[string]string
	own    map[string]string
	shared string
}

// digestsOf measures the schemas root at dir in a commit. It is nil for
// a commit that holds no service under the root.
func digestsOf(repo *Repository, commit, dir string) (*rootDigests, error) {
	tree, err := repo.Tree(commit)
	if err != nil {
		return nil, err
	}
	var root *SchemaRoot
	for _, candidate := range SchemaRoots(tree) {
		if candidate.Dir == dir {
			root = &candidate
			break
		}
	}
	if root == nil {
		return nil, nil
	}
	out := &rootDigests{inputs: map[string]string{}, own: map[string]string{}}
	for _, service := range root.Services {
		set, err := SchemaInputSet(tree, *root, service)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", service, err)
		}
		if out.inputs[service], err = InputDigest(tree, set.Paths, set.Values...); err != nil {
			return nil, err
		}
		// The root's own files are the same in every service's set.
		if out.shared, err = InputDigest(tree, set.Shared, set.Values...); err != nil {
			return nil, err
		}
		if out.own[service], err = InputDigest(tree, ownPaths(tree, root.ServiceDir(service))); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ownPaths are the tracked files of a directory.
func ownPaths(tree registry.TaskTree, dir string) []string {
	entries := under(tree.Entries(), dir)
	paths := make([]string, len(entries))
	for i, entry := range entries {
		paths[i] = entry.Path
	}
	return paths
}
