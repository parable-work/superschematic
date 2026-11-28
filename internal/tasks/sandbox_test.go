package tasks

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sandboxHelperEnv names the repository TestSandboxHelper reads, and
// marks the process as the helper.
const sandboxHelperEnv = "SUPERSCHEMATIC_TASKS_SANDBOX_HELPER"

// TestSandboxHelper is no test of its own. TestSandboxHoldsAResolverToItsRules
// runs it in a process of its own, since a sandbox cannot be taken off:
// it opens a commit, takes the sandbox, and says what it can still do.
func TestSandboxHelper(t *testing.T) {
	dir := os.Getenv(sandboxHelperEnv)
	if dir == "" {
		t.Skip("run by TestSandboxHoldsAResolverToItsRules")
	}
	repo, err := OpenRepository(dir)
	require.NoError(t, err)
	commit, err := repo.Commit("HEAD")
	require.NoError(t, err)
	tree, err := repo.Tree(commit)
	require.NoError(t, err)
	require.NoError(t, repo.Start())

	if err := Sandbox(); err != nil {
		fmt.Printf("sandbox: %v\n", err)
		return
	}
	say := func(what string, err error) {
		switch {
		case err == nil:
			fmt.Printf("%s: allowed\n", what)
		case errors.Is(err, os.ErrPermission):
			fmt.Printf("%s: refused\n", what)
		default:
			fmt.Printf("%s: %v\n", what, err)
		}
	}
	// What a plan needs still works: a tracked file's bytes, through the
	// process started before the sandbox.
	data, err := tree.Read("go.mod")
	say("read a tracked file", err)
	fmt.Printf("bytes: %q\n", data)
	// What a resolver's rules forbid does not.
	say("start a program", exec.Command("git", "version").Run())
	_, err = os.ReadFile(filepath.Join(dir, "go.mod"))
	say("read the working tree", err)
	say("write a file", os.WriteFile(filepath.Join(dir, "written.txt"), []byte("x"), 0o644))
	say("remove a file", os.Remove(filepath.Join(dir, "go.mod")))
	say("make a directory", os.Mkdir(filepath.Join(dir, "made"), 0o755))
	_, err = net.Dial("tcp", "127.0.0.1:9")
	say("open a socket", err)
	_, err = net.Listen("tcp", "127.0.0.1:0")
	say("listen", err)
	// The process still ends its helper in order.
	say("close the repository", repo.Close())
}

// TestSandboxHoldsAResolverToItsRules: with the sandbox on, a process
// reads a commit's files and nothing else. It starts no program, opens no
// socket and touches no file.
func TestSandboxHoldsAResolverToItsRules(t *testing.T) {
	dir := writeRepo(t, FormatSHA1, map[string]MemoryFile{"go.mod": {Data: []byte("module example.com/shop\n")}})
	cmd := exec.Command(os.Args[0], "-test.run=^TestSandboxHelper$", "-test.v")
	cmd.Env = append(os.Environ(), sandboxHelperEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if strings.Contains(text, "sandbox: "+ErrSandboxUnsupported.Error()) {
		t.Skip(ErrSandboxUnsupported)
	}
	if strings.Contains(text, "sandbox: the plan's sandbox: seccomp:") {
		t.Skipf("this machine refuses a seccomp filter: %s", text)
	}
	require.NoError(t, err, text)
	for _, want := range []string{
		"read a tracked file: allowed",
		`bytes: "module example.com/shop\n"`,
		"start a program: refused",
		"read the working tree: refused",
		"write a file: refused",
		"remove a file: refused",
		"make a directory: refused",
		"open a socket: refused",
		"listen: refused",
		"close the repository: allowed",
	} {
		require.Contains(t, text, want)
	}
	// Nothing the helper tried reached the directory.
	require.NoFileExists(t, filepath.Join(dir, "written.txt"))
	require.NoDirExists(t, filepath.Join(dir, "made"))
	require.FileExists(t, filepath.Join(dir, "go.mod"))
}
