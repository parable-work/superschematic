package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/cli"
)

// TestEmbeddedNotesIsOneBinary is step 7 of D116 on examples/embedded-notes:
// an environment on the embedded target built as one binary and run.
//
// `superschematic stack build Desktop` builds the stack, runs the site's build, writes the plan a release recorded
// (plans/Desktop/notes-db/0001.json, which the example commits) and the
// site's files into the environment's crate, builds it with cargo, frozen
// to the committed lockfile, and prints the binary's path. The test starts
// the binary on a free port against a fresh data directory, which it
// migrates from the plan it carries; writes a note and reads it back
// through notes-api under /api/notes-api; reads the digest notes-digest
// counts by calling notes-api in the process, whose service clause holds;
// reads the site's page, which
// the binary carries with its digest as its ETag, the page's config, and
// the fallback for a path that names no file; stops it with SIGTERM; and
// starts it again on the same files, which need no plan and hold the
// note. It listens on every interface only when told to in those words.
func TestEmbeddedNotesIsOneBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the cargo build of examples/embedded-notes in -short mode")
	}
	for _, tool := range []string{"cargo", "bun"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available; skipping the build of examples/embedded-notes", tool)
		}
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// The example builds in place, as examples/acme-shop's does: its
	// lockfiles, which it commits, pin the workspaces of its own output
	// root, and everything else the build writes there git ignores.
	example := filepath.Join(repoRoot, "examples", "embedded-notes")
	schemasRoot := filepath.Join(example, "schemas")
	t.Chdir(example)
	stackDir := filepath.Join(schemasRoot, "services", "notes-stack")

	// A deploy has nothing to roll out, and says what builds the binary.
	if out, err := runErr("stack", "deploy", "Desktop", "--stack", stackDir); err == nil || !strings.Contains(err.Error(), "superschematic stack build Desktop") {
		t.Fatalf("stack deploy Desktop = %v, want a refusal that names stack build\n%s", err, out)
	}

	out := run(t, "stack", "build", "Desktop", "--stack", stackDir)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	binary := lines[len(lines)-1]
	if info, err := os.Stat(binary); err != nil || info.IsDir() {
		t.Fatalf("stack build printed %q last, which is no binary: %v\n%s", binary, err, out)
	}
	if filepath.Base(binary) != "notes-stack-desktop" {
		t.Errorf("the binary is %s, want notes-stack-desktop", binary)
	}
	// The build is no release: it carries the plan the example's release
	// recorded, and records nothing.
	recorded, err := os.ReadFile(filepath.Join(stackDir, "plans", "Desktop", "notes-db", "0001.json"))
	if err != nil {
		t.Fatal(err)
	}
	crate := filepath.Join(schemasRoot, "dist", "embedded", "notes-stack", "Desktop")
	carried, err := os.ReadFile(filepath.Join(crate, "carried", "plans", "notes-db", "0001.json"))
	if err != nil || !bytes.Equal(carried, recorded) {
		t.Errorf("the binary does not carry the recorded plan as it is: %v", err)
	}
	if plans, _ := filepath.Glob(filepath.Join(stackDir, "plans", "Desktop", "notes-db", "*.json")); len(plans) != 1 {
		t.Errorf("a build that is no release recorded a plan: %v", plans)
	}
	if !strings.Contains(out, "the binary carries 1 plan(s) of notes-db") {
		t.Errorf("stack build does not say what the binary carries:\n%s", out)
	}

	data := filepath.Join(t.TempDir(), "data")
	port := freePort(t)
	origin := fmt.Sprintf("http://127.0.0.1:%d", port)

	// It listens on every interface only when told to in those words.
	refused := exec.Command(binary, "--data", data, "--listen", "0.0.0.0")
	refused.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))
	if output, err := refused.CombinedOutput(); err == nil || !strings.Contains(string(output), "--every-interface") {
		t.Fatalf("--listen 0.0.0.0 = %v, want a refusal that names --every-interface\n%s", err, output)
	}

	first := startBinary(t, binary, data, port)
	if !strings.Contains(first.wait(t, origin), "from an empty database") {
		t.Errorf("the first start applied no plan to the empty file:\n%s", first.log.String())
	}
	if _, err := os.Stat(filepath.Join(data, "databases", "notes_db.sqlite")); err != nil {
		t.Errorf("the database's file is not under the data directory as stack dev lays it out: %v", err)
	}

	var created struct {
		Data struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"data"`
	}
	status, _, body := request(t, http.MethodPost, origin+"/api/notes-api/api/notes", `{"title":"milk","body":"two litres"}`, nil)
	if err := json.Unmarshal(body, &created); status != http.StatusOK || err != nil || created.Data.Title != "milk" {
		t.Fatalf("POST /api/notes-api/api/notes = %d %s (%v)", status, body, err)
	}
	if status, _, body := request(t, http.MethodGet, origin+"/api/notes-api/api/notes/"+created.Data.ID, "", nil); status != http.StatusOK || !strings.Contains(string(body), "two litres") {
		t.Errorf("GET the note = %d %s", status, body)
	}
	// notes-digest, an API of another server of the binary, counts the
	// notes by calling notes-api through its SDK, in the process, with its
	// server's loopback credential, which notes-api's service clause
	// admits; the same route refuses a caller with no service credential,
	// as it does between two servers.
	if status, _, body := request(t, http.MethodGet, origin+"/api/notes-digest/api/digest", "", nil); status != http.StatusOK || !strings.Contains(string(body), `"notes":1`) {
		t.Errorf("GET the digest = %d %s, want one note counted through notes-api", status, body)
	}
	if status, _, body := request(t, http.MethodGet, origin+"/api/notes-api/api/internal/notes", "", nil); status != http.StatusUnauthorized || !strings.Contains(string(body), "service_unauthorized") {
		t.Errorf("GET notes-api's route for services with no credential = %d %s, want 401 service_unauthorized", status, body)
	}
	if status, _, _ := request(t, http.MethodGet, origin+"/api/no-such-api/notes", "", nil); status != http.StatusNotFound {
		t.Errorf("a path under /api that is no API's = %d, want 404, never the site's page", status)
	}

	status, header, page := request(t, http.MethodGet, origin+"/", "", nil)
	etag := header.Get("ETag")
	if status != http.StatusOK || !strings.HasPrefix(header.Get("Content-Type"), "text/html") || len(etag) != 66 || !bytes.Contains(page, []byte("<html")) {
		t.Fatalf("GET / = %d %s, ETag %q:\n%s", status, header.Get("Content-Type"), etag, page)
	}
	if status, _, body := request(t, http.MethodGet, origin+"/", "", map[string]string{"If-None-Match": etag}); status != http.StatusNotModified || len(body) != 0 {
		t.Errorf("GET / with the page's ETag = %d with %d bytes, want 304 with none", status, len(body))
	}
	if status, _, body := request(t, http.MethodGet, origin+"/notes/"+created.Data.ID, "", nil); status != http.StatusOK || !bytes.Equal(body, page) {
		t.Errorf("a path that names no file = %d, want the site's fallback, its page", status)
	}
	var config struct {
		APIs map[string]struct {
			URL string `json:"url"`
		} `json:"apis"`
	}
	status, _, body = request(t, http.MethodGet, origin+"/__superschematic/config.json", "", nil)
	if err := json.Unmarshal(body, &config); status != http.StatusOK || err != nil || config.APIs["notes-api"].URL != origin+"/api/notes-api" {
		t.Errorf("the site's config = %d %s (%v), want notes-api on the page's own origin", status, body, err)
	}
	first.stop(t)

	// The same files again: nothing to migrate, and the note is there.
	second := startBinary(t, binary, data, port)
	if log := second.wait(t, origin); strings.Contains(log, "applying plan") {
		t.Errorf("the second start applied a plan to a file at the last plan's model:\n%s", log)
	}
	status, _, body = request(t, http.MethodGet, origin+"/api/notes-api/api/notes", "", nil)
	if status != http.StatusOK || !strings.Contains(string(body), created.Data.ID) {
		t.Errorf("the notes after a restart = %d %s, want the note the first run wrote", status, body)
	}
	second.stop(t)
}

// runErr runs the installed binary's command line with args, and returns
// what it printed and its error.
func runErr(args ...string) (string, error) {
	buf := new(bytes.Buffer)
	root := cli.New(cli.Config{Name: "superschematic"}, extensions()...)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// freePort returns a port nothing listens on.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

// running is the example's binary, started.
type running struct {
	cmd  *exec.Cmd
	log  *bytes.Buffer
	done chan error
}

// startBinary starts the binary on data, listening on loopback at port.
func startBinary(t *testing.T, binary, data string, port int) *running {
	t.Helper()
	r := &running{cmd: exec.Command(binary, "--data", data), log: new(bytes.Buffer), done: make(chan error, 1)}
	r.cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))
	r.cmd.Stdout, r.cmd.Stderr = r.log, r.log
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { r.done <- r.cmd.Wait() }()
	t.Cleanup(func() {
		if r.cmd.ProcessState == nil {
			_ = r.cmd.Process.Kill()
		}
	})
	return r
}

// wait waits until the binary answers at origin, and returns its log so
// far.
func (r *running) wait(t *testing.T, origin string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-r.done:
			t.Fatalf("the binary stopped before it served: %v\n%s", err, r.log.String())
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/__superschematic/config.json", nil)
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err == nil {
			_ = resp.Body.Close()
			return r.log.String()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the binary did not serve at %s:\n%s", origin, r.log.String())
	return ""
}

// stop stops the binary as a person's Ctrl-C or a supervisor does, and
// holds it to a clean exit.
func (r *running) stop(t *testing.T) {
	t.Helper()
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-r.done:
		var exit *exec.ExitError
		if errors.As(err, &exit) || err != nil {
			t.Fatalf("the binary did not stop cleanly on SIGTERM: %v\n%s", err, r.log.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the binary did not stop on SIGTERM:\n%s", r.log.String())
	}
}

// request makes one request and returns its status, headers and body.
func request(t *testing.T, method, url, body string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, data
}

// TestStackDevRunsEmbeddedNotesAsOneProcess: `superschematic stack dev`
// runs the example's environment as the one process it is (D116). It
// migrates notes-db's file under the environment's state directory as the
// local target does, with superschematic-migrate, from the model the file
// recorded to the schema's; builds the binary as a build that is no
// release; and runs it at the environment's port until it is interrupted,
// when the binary stops with it. The binary's own start finds the file at
// the model its carried plans end at, and applies none.
func TestStackDevRunsEmbeddedNotesAsOneProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the cargo build of examples/embedded-notes in -short mode")
	}
	for _, tool := range []string{"cargo", "bun", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available; skipping stack dev of examples/embedded-notes", tool)
		}
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// The migration runner, which holds the SQLite driver the compiler's
	// module leaves out.
	runner := filepath.Join(t.TempDir(), "superschematic-migrate")
	build := exec.Command("go", "build", "-o", runner, "./cmd/superschematic-migrate")
	build.Dir = filepath.Join(repoRoot, "runtime", "migrate", "go")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build superschematic-migrate: %v\n%s", err, output)
	}
	t.Setenv("SUPERSCHEMATIC_MIGRATE", runner)
	example := filepath.Join(repoRoot, "examples", "embedded-notes")
	t.Chdir(example)
	state := filepath.Join(example, "schemas", ".superschematic")
	t.Cleanup(func() { _ = os.RemoveAll(state) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := new(syncBuffer)
	root := cli.New(cli.Config{Name: "superschematic"}, extensions()...)
	root.SetOut(log)
	root.SetErr(log)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"stack", "dev", "--remove-data"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	// The environment's values name its port.
	const origin = "http://127.0.0.1:4170"
	deadline := time.Now().Add(20 * time.Minute)
	for {
		select {
		case err := <-done:
			t.Fatalf("stack dev stopped before it served: %v\n%s", err, log.String())
		default:
		}
		if resp, err := http.Get(origin + "/__superschematic/config.json"); err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stack dev did not serve at %s:\n%s", origin, log.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status, _, body := request(t, http.MethodPost, origin+"/api/notes-api/api/notes", `{"title":"dev"}`, nil); status != http.StatusOK {
		t.Errorf("POST a note under stack dev = %d %s", status, body)
	}
	file := filepath.Join(state, "local", "notes-stack", "Desktop", "databases", "notes_db.sqlite")
	if _, err := os.Stat(file); err != nil {
		t.Errorf("the database's file is not under the state directory: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("stack dev stopped with %v\n%s", err, log.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("stack dev did not stop:\n%s", log.String())
	}
	got := log.String()
	for _, want := range []string{"migrate notes-db: 1 steps to model", "is one binary", `"plans":0`} {
		if !strings.Contains(got, want) {
			t.Errorf("stack dev's output lacks %q:\n%s", want, got)
		}
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("--remove-data left the database's file: %v", err)
	}
}

// syncBuffer is a buffer a command writes while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
