package enginehttp_test

// The HTTP corpus (runtime/engine/testdata/http, whose README is the
// format) replayed through enginehttp, as a Go server serves the engine:
// each case's apps are httptest servers whose mounts are enginehttp
// handlers over the engine the case opened through the binding, each
// exchange is sent over HTTP with the caller the recording established,
// and each event stream is read frame by frame from the response body. A
// case's engine steps go through Engine.Do. TestReplayScenarioCalls
// replays the engine's own scenario files (runtime/engine/testdata/scenarios)
// through Engine.Do alone. Both run on SQLite, and on Postgres when
// SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL names one.
//
// Most cases need what only a recording says: the test kit's behaviors
// and a policy that answers from the recorded table. The test kit's
// archive has them (runtime/engine/rust/testkit-ffi); link it in place of
// the core's with
//
//	scripts/engine-archive.sh --crate runtime/engine/rust/testkit-ffi --out <dir>
//	CGO_LDFLAGS=-L<dir> go test -run Replay ./enginehttp/
//
// (make engine-go does). Over the core's archive the replay runs only the
// cases that need neither, and ENGINE_REPLAY_ARCHIVE=1 makes it fail
// instead. A case is skipped, as the Rust test kit's replay skips it, when
// only the TypeScript engine can run it (js), when it composes a core
// behavior not ported yet, and on Postgres when it is SQLite's; KNOWN lists
// the steps whose answers differ, with why. ENGINE_HTTP_CORPUS_DIR names
// another corpus and ENGINE_HTTP_ONLY one file of it.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	engine "github.com/parable-work/superschematic/runtime/engine/go"
	"github.com/parable-work/superschematic/runtime/engine/go/enginehttp"
)

const (
	corpusVariable   = "ENGINE_HTTP_CORPUS_DIR"
	onlyVariable     = "ENGINE_HTTP_ONLY"
	databaseVariable = "SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL"
	archiveVariable  = "ENGINE_REPLAY_ARCHIVE"
	testkitProject   = "superschematic-engine-testkit-ffi"
	scenarioSeed     = "superschematic-engine-scenario/"
	requestSeed      = "superschematic-engine-replay-request/"
	callerHeader     = "X-Replay-Caller"
	frameWait        = 10 * time.Second
)

// httpFiles are the files of the HTTP corpus.
var httpFiles = []string{"http", "operations", "stream", "mcp"}

// coreBehaviors are the core's behaviors, which the TypeScript engine
// registers when it opens; a case that composes one the archive does not
// register is skipped.
var coreBehaviors = []string{
	"Workflow", "Comments", "Revisions", "Dependencies", "Links", "Rollups", "Search", "Reactions",
	"Constants", "Variants", "Branches", "StateRules", "Approvals", "Timers", "Numbering",
}

// responseHeaders are the response headers an exchange compares.
var responseHeaders = []string{
	"allow", "cache-control", "content-type", "etag", "location", "mcp-session-id",
	"retry-after", "www-authenticate", "x-request-id",
}

// unbuiltWrites change the engine and are not built: a case that makes one
// is skipped.
var unbuiltWrites = []string{"storage.transaction", "events.watch", "search"}

// knownMismatch is a step whose answer differs for a reason outside the
// engine's routes: its file, its case and its step (from 1).
type knownMismatch struct {
	file     string
	scenario string
	step     int
	reason   string
	// timing marks a mismatch that depends on when frames are read: it is
	// reported when the step differs, and a step that gives the recorded
	// answer does not fail the test.
	timing bool
}

// matches is whether a failure is this mismatch's: its case's, at its
// step.
func (k knownMismatch) matches(failure string) bool {
	return strings.HasPrefix(failure, fmt.Sprintf("%s > step %d ", k.scenario, k.step))
}

// known are the steps whose answers differ, with why. The test reports
// them and passes; once one gives the recorded answer, it fails until it
// leaves this list.
var known = []knownMismatch{
	{
		"http",
		"refusals > the runtime answers an unknown path, bad percent-encoding and a large body as problems",
		11,
		"net/http answers a request target that is not percent-encoding (%ZZ) with its own 400 in text/plain before any handler sees it, where Hono hands it to the engine, which answers a problem document",
		false,
	},
	{
		"stream",
		"event stream > a resume from before the floor is 410 cursor_expired, a stream from the start replays from the floor, and a stream retention passes ends",
		15,
		"enginehttp writes each live event as it commits, so the stream may send o2's and o3's before the prune ends it; the recording's client pulled the stream only after the prune, when the engine ended it at once",
		true,
	},
}

// testkitHas is whether the test kit's archive implements a test behavior
// for a corpus file (superschematic_engine_testkit::behaviors::test_behavior).
func testkitHas(file, name string) bool {
	switch name {
	case "test.Counter", "test.Flag", "test.Tally", "test.Tag", "test.Mailer", "test.Slugger",
		"test.Ledger", "test.Pacer", "test.Holder", "test.Reader", "test.Broken":
		return true
	case "test.Hold":
		return !strings.HasPrefix(file, "core-") || file == "core-timers" || file == "core-revisions"
	}
	switch file + "/" + name {
	case "core-workflow/test.Dispatch", "core-staterules/test.Recorder", "core-timers/test.Bounce",
		"core-dependencies/test.Finisher", "core-numbering/test.Refuser":
		return true
	}
	return false
}

func repository(t *testing.T) string {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// database is where a replay opens its engines.
type database struct {
	name string
	url  string
}

func (d database) postgres() bool { return d.url != "" }

// archive is what the linked archive registers.
type archive struct {
	testkit   bool
	behaviors map[string]bool
}

func probe(t *testing.T) archive {
	e, err := engine.Open(engine.Config{Path: filepath.Join(t.TempDir(), "probe.db"), Policy: engine.AllowAll()})
	if err != nil {
		t.Fatalf("open a probe engine: %v", err)
	}
	defer e.Close()
	info := e.Info()
	registered := map[string]bool{}
	for _, name := range info.Behaviors {
		registered[name] = true
	}
	return archive{testkit: info.Project == testkitProject, behaviors: registered}
}

func TestReplayHTTPCorpus(t *testing.T) {
	root := repository(t)
	dir := os.Getenv(corpusVariable)
	if dir == "" {
		dir = filepath.Join(root, "runtime", "engine", "testdata", "http")
	}
	replayCorpus(t, root, dir, httpFiles, os.Getenv(onlyVariable), known, func(file *corpusFile) {
		if len(file.scenarios) <= 5 {
			t.Fatalf("%s holds %d cases", file.name, len(file.scenarios))
		}
	})
}

// scenarioFiles are the scenario corpus's files a replay through Call
// runs: the engine's own test files, whose calls need no behavior or the
// test kit's.
var scenarioFiles = []string{
	"access", "compat", "events", "instances", "list-filters", "namespaces", "normalize", "registry", "unique",
}

// knownScenarios are the scenario steps whose answers differ through the
// binding, with why.
var knownScenarios = []knownMismatch{}

// TestReplayScenarioCalls replays the engine's own scenario files
// (runtime/engine/testdata/scenarios) through Engine.Do, as the Rust test
// kit replays them through the engine's calls: the binding's calls, their
// arguments and their errors, which the HTTP corpus's engine steps cover
// only in part. ENGINE_CORPUS_DIR names another corpus and
// ENGINE_SCENARIOS_ONLY one file of it.
func TestReplayScenarioCalls(t *testing.T) {
	root := repository(t)
	dir := os.Getenv("ENGINE_CORPUS_DIR")
	if dir == "" {
		dir = filepath.Join(root, "runtime", "engine", "testdata", "scenarios")
	}
	replayCorpus(t, root, dir, scenarioFiles, os.Getenv("ENGINE_SCENARIOS_ONLY"), knownScenarios, func(*corpusFile) {})
}

// replayCorpus replays files of a corpus on SQLite, and on Postgres when
// the variable names one, and fails on a step that differs unless known
// lists it.
func replayCorpus(t *testing.T, root, dir string, files []string, only string, known []knownMismatch, check func(*corpusFile)) {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("no corpus at %s (the tree holds runtime/engine/testdata)", dir)
	}
	linked := probe(t)
	if !linked.testkit {
		if os.Getenv(archiveVariable) == "1" {
			t.Fatalf("%s=1 and the linked archive is not the test kit's: build it with scripts/engine-archive.sh --crate runtime/engine/rust/testkit-ffi --out <dir> and put -L<dir> first in CGO_LDFLAGS", archiveVariable)
		}
		t.Logf("the linked archive is the core's: the cases that need the test kit's behaviors or a recorded policy are skipped (scripts/engine-archive.sh --crate runtime/engine/rust/testkit-ffi)")
	}
	databases := []database{{name: "SQLite"}}
	if url := os.Getenv(databaseVariable); url != "" {
		databases = append(databases, database{name: "Postgres", url: url})
	} else {
		t.Logf("replay: Postgres pass skipped; set %s to run it", databaseVariable)
	}
	var failures []string
	for _, db := range databases {
		var reports []*report
		for _, name := range files {
			if only != "" && only != name {
				continue
			}
			file, err := load(filepath.Join(dir, name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			check(file)
			reports = append(reports, replayFile(t, file, db, linked, root))
		}
		t.Logf("replay through the Go binding on %s:", db.name)
		for _, rep := range reports {
			t.Logf("  %s", rep.summary())
			reasons := map[string]int{}
			for _, skipped := range rep.skipped {
				reasons[skipped[1]]++
			}
			keys := make([]string, 0, len(reasons))
			for reason := range reasons {
				keys = append(keys, reason)
			}
			sort.Strings(keys)
			for _, reason := range keys {
				t.Logf("      skipped %d: %s", reasons[reason], reason)
			}
			for _, k := range known {
				if k.file != rep.file {
					continue
				}
				failed := slices.ContainsFunc(rep.failures, k.matches)
				ran := !slices.ContainsFunc(rep.skipped, func(s [2]string) bool { return s[0] == k.scenario })
				switch {
				case failed:
					t.Logf("      known mismatch: %s (step %d): %s", k.scenario, k.step, k.reason)
				case ran && !k.timing:
					failures = append(failures, fmt.Sprintf("[%s] %s: %s (step %d) gives the recorded answer now; take it out of known", db.name, k.file, k.scenario, k.step))
				}
			}
			for _, failure := range rep.failures {
				isKnown := slices.ContainsFunc(known, func(k knownMismatch) bool {
					return k.file == rep.file && k.matches(failure)
				})
				if !isKnown {
					failures = append(failures, fmt.Sprintf("[%s] %s: %s", db.name, rep.file, failure))
				} else if testing.Verbose() {
					t.Logf("      %s", failure)
				}
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("%d steps gave other answers:\n%s", len(failures), strings.Join(failures, "\n"))
	}
}

type corpusFile struct {
	name      string
	scenarios []*object
}

func load(path string) (*corpusFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	value, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	shared, _ := member(value, "shared").(*object)
	cases := list(member(value, "cases"))
	if cases == nil {
		cases = list(member(value, "scenarios"))
	}
	file := &corpusFile{name: textOf(member(value, "name"))}
	for _, each := range cases {
		resolved, err := resolveShared(each, shared)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		scenario, ok := resolved.(*object)
		if !ok {
			return nil, fmt.Errorf("%s: a case that is no object", path)
		}
		file.scenarios = append(file.scenarios, scenario)
	}
	return file, nil
}

type report struct {
	file         string
	scenarios    int
	replayed     int
	skipped      [][2]string
	stepsPassed  int
	stepsSkipped int
	failures     []string
}

func (r *report) summary() string {
	return fmt.Sprintf("%s: %d of %d scenarios replayed (%d skipped), %d steps passed, %d skipped, %d failed",
		r.file, r.replayed, r.scenarios, len(r.skipped), r.stepsPassed, r.stepsSkipped, len(r.failures))
}

func replayFile(t *testing.T, file *corpusFile, db database, linked archive, root string) *report {
	rep := &report{file: file.name, scenarios: len(file.scenarios)}
	for _, scenario := range file.scenarios {
		name := textOf(member(scenario, "name"))
		var steps []*object
		for _, step := range list(member(scenario, "steps")) {
			if o, ok := step.(*object); ok {
				steps = append(steps, o)
			}
		}
		if reason := skipReason(file.name, scenario, steps, db, linked); reason != "" {
			rep.stepsSkipped += len(steps)
			rep.skipped = append(rep.skipped, [2]string{name, reason})
			continue
		}
		rep.replayed++
		r := newRun(t, file.name, scenario, db, linked, root)
		for index, step := range steps {
			r.tail = true
			for _, later := range steps[index+1:] {
				if _, isStream := later.get("stream"); !isStream || member(later, "end") == nil {
					r.tail = false
				}
			}
			passed, err := r.step(step)
			switch {
			case err != nil:
				call := textOf(member(step, "call"))
				if call == "" {
					call = "?"
				}
				rep.failures = append(rep.failures, fmt.Sprintf("%s > step %d (%s): %v", name, index+1, call, err))
			case passed:
				rep.stepsPassed++
			default:
				rep.stepsSkipped++
			}
		}
		r.finish()
	}
	return rep
}

func names(scenario *object, key string) []string {
	var out []string
	for _, each := range list(member(scenario, key)) {
		if s, ok := text(each); ok {
			out = append(out, s)
		}
	}
	return out
}

// skipReason says why a case is not replayed, or "".
func skipReason(file string, scenario *object, steps []*object, db database, linked archive) string {
	if js := names(scenario, "js"); len(js) > 0 {
		return "js: " + strings.Join(js, ", ")
	}
	if tests := names(scenario, "testBehaviors"); len(tests) > 0 {
		if !linked.testkit {
			return "the test kit's behaviors, in its archive alone"
		}
		var missing []string
		for _, name := range tests {
			if !testkitHas(file, name) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return "test behaviors not ported: " + strings.Join(missing, ", ")
		}
	}
	// A schema an exchange defines over HTTP composes behaviors the
	// recorder never saw, so its body is read for them too.
	composed := names(scenario, "behaviors")
	for _, step := range steps {
		body := member(member(step, "request"), "body")
		types, _ := member(body, "types").(*object)
		if types == nil {
			continue
		}
		for _, key := range types.keys {
			for _, behavior := range list(member(types.members[key], "behaviors")) {
				if name, ok := text(member(behavior, "name")); ok && !slices.Contains(composed, name) {
					composed = append(composed, name)
				}
			}
		}
	}
	sort.Strings(composed)
	var unported []string
	for _, name := range composed {
		if slices.Contains(coreBehaviors, name) && !linked.behaviors[name] {
			unported = append(unported, name)
		}
	}
	if len(unported) > 0 {
		return "core behaviors not ported yet (milestones 10 and 11): " + strings.Join(unported, ", ")
	}
	if textOf(member(scenario, "dialect")) == "sqlite" && db.postgres() {
		return "dialect: sqlite"
	}
	for _, step := range steps {
		call := textOf(member(step, "call"))
		if slices.Contains(unbuiltWrites, call) {
			return "needs " + call
		}
		if call == "storage.run" || call == "storage.exec" {
			return "writes the file with SQL of its own: the C ABI has no storage calls"
		}
		if call == "behaviors.register" {
			return "registers a behavior after the engine opened: the C ABI registers an archive's behaviors when an engine opens"
		}
		if _, scripted := step.get("clock"); scripted {
			return "a fixed clock that read several times in a step: the C ABI sets a manual clock and scripts none"
		}
		if call != "open" {
			continue
		}
		given, _ := firstArg(step).(*object)
		if textOf(member(given, "ids")) == "fixed" {
			return "ids the test gave: the C ABI takes no id queue"
		}
		if !linked.testkit {
			if _, table := member(given, "policy").(*object); table {
				return "a recorded policy, in the test kit's archive alone"
			}
			if textOf(member(given, "metaSchema")) == "open" {
				return "the open meta-schema, in the test kit's archive alone"
			}
			if len(list(member(given, "behaviors"))) > 0 {
				return "the test kit's behaviors, in its archive alone"
			}
		}
	}
	return ""
}

func firstArg(step *object) any {
	args := list(member(step, "args"))
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

// opened is an engine of a case.
type opened struct {
	name       string
	e          *engine.Engine
	fixedClock bool
}

// app is what a case serves: its mounts, over one engine, behind one
// httptest server.
type app struct {
	name   string
	engine string
	mounts []*mount
	routes []engine.Route
	server *httptest.Server
}

type mount struct {
	prefix  string
	mcp     bool
	handler http.Handler
}

// openStream is a response body of server-sent events, read frame by
// frame as they arrive.
type openStream struct {
	label  string
	body   io.ReadCloser
	frames chan any
	held   []any
	ended  bool
}

type bucket struct {
	tokens float64
	at     int64
}

// run is one case's replay.
type run struct {
	t          *testing.T
	file       string
	db         database
	linked     archive
	root       string
	principals *object
	engines    []*opened
	places     map[string]string
	schemas    []string
	symbols    *symbols
	apps       []*app
	streams    []*openStream
	buckets    map[string]*bucket
	bucketsMu  sync.Mutex
	requests   atomic.Uint64
	client     *http.Client
	tail       bool
}

var nextPlace atomic.Uint64

func unique(prefix string) string {
	return fmt.Sprintf("%s_%d_%d_%d", prefix, time.Now().UnixNano(), os.Getpid(), nextPlace.Add(1))
}

func newRun(t *testing.T, file string, scenario *object, db database, linked archive, root string) *run {
	principals, _ := member(scenario, "principals").(*object)
	return &run{
		t:          t,
		file:       file,
		db:         db,
		linked:     linked,
		root:       root,
		principals: principals,
		places:     map[string]string{},
		symbols:    newSymbols(db.postgres()),
		buckets:    map[string]*bucket{},
		client:     &http.Client{Transport: &http.Transport{DisableCompression: true}},
	}
}

// step replays one step: true when it passed, false when it was skipped.
func (r *run) step(step *object) (bool, error) {
	if member(step, "js") == true {
		return false, nil
	}
	_, isApp := step.get("app")
	_, isExchange := step.get("request")
	_, isStream := step.get("stream")
	switch {
	case isApp && !isExchange:
		return true, r.app(step)
	case isExchange:
		return true, r.exchange(step)
	case isStream:
		return true, r.stream(step)
	}
	call := textOf(member(step, "call"))
	switch call {
	case "behaviors.names", "tools.listBehaviors":
		for _, name := range coreBehaviors {
			if !r.linked.behaviors[name] {
				return false, nil
			}
		}
	case "behaviors.has", "behaviors.declaration", "tools.describeBehavior":
		if name, ok := text(firstArg(step)); ok && slices.Contains(coreBehaviors, name) && !r.linked.behaviors[name] {
			return false, nil
		}
	case "storage.get", "storage.all", "storage.run", "storage.exec":
		// The file's own SQL: the C ABI has no storage calls.
		return false, nil
	}
	var args []any
	for _, arg := range list(member(step, "args")) {
		written, err := r.symbols.substitute(arg)
		if err != nil {
			return false, err
		}
		args = append(args, written)
	}
	if strings.HasPrefix(call, "events.") && r.db.postgres() {
		for _, arg := range args {
			if o, ok := arg.(*object); ok {
				if after, ok := integer(member(o, "after")); ok {
					o.set("after", number(r.symbols.cursorArgument(after)))
				}
			}
		}
	}
	compare := !(textOf(member(step, "dialect")) == "sqlite" && r.db.postgres())
	switch call {
	case "open":
		return r.compare(step, nil, false, r.open(step, args), compare)
	case "close":
		e, err := r.engineOf(step)
		if err != nil {
			return false, err
		}
		if e.e != nil {
			_ = e.e.Close()
			e.e = nil
		}
		return r.compare(step, nil, false, nil, compare)
	}
	request := engine.Request{Op: call}
	if given, ok := step.get("principal"); ok {
		name, isName := text(given)
		if !isName {
			return false, fmt.Errorf("a principal the replayer cannot pass: %s", show(given))
		}
		principal, ok := r.principals.get(name)
		if !ok {
			return false, fmt.Errorf("no principal %s", name)
		}
		request.RawPrincipal = encode(principal)
	}
	for _, arg := range args {
		request.Args = append(request.Args, json.RawMessage(encode(arg)))
	}
	e, err := r.engineOf(step)
	if err != nil {
		return false, err
	}
	if e.e == nil {
		return false, errors.New("the engine is closed")
	}
	if now, ok := integer(member(step, "now")); ok && e.fixedClock {
		request.Now = &now
	}
	if !e.fixedClock {
		r.pastLatest()
	}
	out, failed := e.e.Do(context.Background(), request)
	if failed != nil || out == nil {
		return r.compare(step, nil, false, failed, compare)
	}
	result, err := decode(out)
	if err != nil {
		return false, fmt.Errorf("the answer is no JSON: %w", err)
	}
	return r.compare(step, result, true, nil, compare)
}

// compare holds a call's answer to the recorded one: its result (has is
// whether it returned one), or its error.
func (r *run) compare(step *object, result any, has bool, failed error, compare bool) (bool, error) {
	if !compare {
		return true, nil
	}
	expected, expects := step.get("expect")
	recorded, errs := step.get("error")
	switch {
	case expects && failed != nil:
		return false, fmt.Errorf("expected %s, got the error %s", show(expected), show(errorJSON(failed)))
	case expects && !has:
		return false, fmt.Errorf("expected %s, got nothing", show(expected))
	case expects:
		return true, r.symbols.matches(expected, result, "")
	case errs && failed != nil:
		return true, r.symbols.matches(recorded, errorJSON(failed), "")
	case errs:
		return false, fmt.Errorf("expected the error %s, got %s", show(recorded), show(result))
	case failed != nil:
		return false, fmt.Errorf("expected nothing, got the error %s", show(errorJSON(failed)))
	case has:
		return false, fmt.Errorf("expected nothing, got %s", show(result))
	}
	return true, nil
}

// errorJSON is an error as the corpus records it.
func errorJSON(err error) any {
	out := newObject()
	var refused *engine.Error
	if errors.As(err, &refused) {
		out.set("class", refused.Class)
		switch {
		case refused.Code != "":
			out.set("code", string(refused.Code))
		case refused.Class == "SqliteError":
			out.set("code", number(int64(refused.SQLiteCode)))
		}
		out.set("message", refused.Message)
		if len(refused.Details) > 0 {
			out.set("details", fromGo(refused.Details))
		}
		return out
	}
	out.set("class", "Error")
	out.set("message", err.Error())
	return out
}

func (r *run) engineOf(step *object) (*opened, error) {
	name, named := text(member(step, "engine"))
	for i := len(r.engines) - 1; i >= 0; i-- {
		if !named || r.engines[i].name == name {
			return r.engines[i], nil
		}
	}
	return nil, errors.New("no engine is open")
}

// open opens an engine through the binding as the recording says: its
// storage a place of the replay's own, its seeded ids and the wall clock
// strict, and what only a recording says (the answer table, the test
// kit's behaviors, the open meta-schema) the test kit's project config.
func (r *run) open(step *object, args []any) error {
	given := newObject()
	if len(args) > 0 {
		if o, ok := args[0].(*object); ok {
			given = o
		}
	}
	name := textOf(member(step, "as"))
	if name == "" {
		name = "e1"
	}
	config := newObject()
	path := textOf(member(given, "path"))
	place, ok := r.places[path]
	if !ok {
		if r.db.postgres() {
			place = unique("replay")
			r.schemas = append(r.schemas, place)
		} else {
			place = filepath.Join(r.t.TempDir(), "engine.db")
		}
		r.places[path] = place
	}
	if r.db.postgres() {
		postgres := newObject()
		postgres.set("url", r.db.url)
		postgres.set("schema", place)
		postgres.set("listen", false)
		config.set("postgres", postgres)
	} else {
		config.set("path", place)
	}
	busy := member(given, "busyTimeoutMs")
	if busy == nil {
		busy = number(5000)
	}
	config.set("busyTimeoutMs", busy)
	project := newObject()
	project.set("file", r.file)
	switch policy := member(given, "policy").(type) {
	case nil:
	case string:
		config.set("policy", policy)
	case *object:
		project.set("answers", member(policy, "answers"))
		if r.principals != nil {
			project.set("principals", r.principals)
		}
	default:
		return fmt.Errorf("a policy the replayer cannot pass: %s", show(policy))
	}
	for _, key := range []string{"namespaces", "runner", "retention", "tools", "values"} {
		if value, ok := given.get(key); ok {
			config.set(key, value)
		}
	}
	seed := scenarioSeed
	if name != "e1" {
		seed += name + "/"
	}
	ids := newObject()
	ids.set("seeded", seed)
	config.set("ids", ids)
	fixed := textOf(member(given, "clock")) == "fixed"
	if fixed {
		now, _ := integer(member(step, "now"))
		manual := newObject()
		manual.set("manual", number(now))
		config.set("clock", manual)
	} else {
		config.set("clock", "strict")
	}
	switch meta := member(given, "metaSchema").(type) {
	case nil:
	case string:
		if meta != "open" {
			return fmt.Errorf("a meta-schema the replayer cannot pass: %s", meta)
		}
		project.set("openMetaSchema", r.root)
	default:
		config.set("metaSchema", meta)
	}
	if behaviors := list(member(given, "behaviors")); len(behaviors) > 0 {
		project.set("behaviors", behaviors)
	}
	if r.linked.testkit {
		config.set("project", project)
	}
	e, err := engine.OpenJSON(encode(config), 0)
	if err != nil {
		return err
	}
	r.engines = append(r.engines, &opened{name: name, e: e, fixedClock: fixed})
	return nil
}

// pastLatest waits until the wall clock is past every instant a time
// symbol is bound to. The recorder's wall clock is one strict clock for
// every engine of a test, never one millisecond twice; each engine here
// has a strict clock of its own, so a step on one engine starts past what
// the others read.
func (r *run) pastLatest() {
	for latest := r.symbols.latest(); time.Now().UnixMilli() <= latest; {
		time.Sleep(200 * time.Microsecond)
	}
}

// app builds the mounts a case serves, behind a server of their own.
func (r *run) app(step *object) error {
	name := textOf(member(step, "app"))
	if name == "" {
		name = "a1"
	}
	a := &app{name: name, engine: textOf(member(step, "engine"))}
	var e *opened
	for i := len(r.engines) - 1; i >= 0; i-- {
		if a.engine == "" || r.engines[i].name == a.engine {
			e = r.engines[i]
			break
		}
	}
	if e == nil || e.e == nil {
		return errors.New("no engine is open")
	}
	routes, err := e.e.Routes(context.Background())
	if err != nil {
		return err
	}
	a.routes = routes
	kind := textOf(member(step, "kind"))
	if kind == "mount" {
		for _, each := range list(member(step, "mounts")) {
			options, _ := member(each, "options").(*object)
			a.mounts = append(a.mounts, r.mount(e.e, textOf(member(each, "prefix")), textOf(member(each, "kind")), options))
		}
	} else {
		options, _ := member(step, "options").(*object)
		a.mounts = append(a.mounts, r.mount(e.e, "", kind, options))
	}
	a.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.dispatch(a, w, req)
	}))
	r.apps = append(r.apps, a)
	return nil
}

// mount is one enginehttp handler, with the options the case built it
// with; the host's authentication is the replay's, which reads each
// exchange's recorded caller.
func (r *run) mount(e *engine.Engine, prefix, kind string, options *object) *mount {
	m := &mount{prefix: prefix, mcp: kind == "mcp"}
	handlerOptions := enginehttp.Options{
		Prefix:    prefix,
		Caller:    replayCaller,
		RequestID: r.requestID,
		Serve:     enginehttp.ServeAPI,
	}
	if m.mcp {
		handlerOptions.Serve = enginehttp.ServeMCP
	}
	if stream, ok := member(options, "stream").(*object); ok {
		if size, ok := integer(member(stream, "pageSize")); ok {
			handlerOptions.Stream.PageSize = int(size)
		}
		if heartbeat, ok := integer(member(stream, "heartbeatMs")); ok {
			handlerOptions.Stream.HeartbeatMs = int(heartbeat)
		}
	}
	if server, ok := member(options, "serverInfo").(*object); ok {
		handlerOptions.MCP.ServerInfo = &engine.ServerInfo{Name: textOf(member(server, "name")), Version: textOf(member(server, "version"))}
	}
	handlerOptions.MCP.Instructions = textOf(member(options, "instructions"))
	if limit, ok := integer(member(options, "bodyLimitBytes")); ok {
		handlerOptions.BodyLimitBytes = limit
	}
	if limit, ok := member(options, "rateLimitPerMinute").(json.Number); ok {
		if perMinute, err := limit.Float64(); err == nil && perMinute > 0 {
			handlerOptions.Middleware = append(handlerOptions.Middleware, r.rateLimit(perMinute))
		}
	}
	m.handler = enginehttp.Handler(e, handlerOptions)
	return m
}

// dispatch hands a request to the mount under whose prefix it falls and
// whose kind serves its route; no mount serving it is the app's 404.
func (r *run) dispatch(a *app, w http.ResponseWriter, req *http.Request) {
	path := req.URL.RawPath
	if path == "" {
		path = req.URL.Path
	}
	for _, m := range a.mounts {
		rest, ok := strings.CutPrefix(path, strings.TrimSuffix(m.prefix, "/"))
		if !ok {
			continue
		}
		route, found := routeOf(a.routes, req.Method, rest)
		if found && route.MCP == m.mcp {
			m.handler.ServeHTTP(w, req)
			return
		}
	}
	enginehttp.WriteProblem(w, http.StatusNotFound, "Not Found", "not_found", r.requestID(req), nil)
}

// routeOf is the route a method and a path after the prefix name, a
// parameter matching one non-empty segment.
func routeOf(routes []engine.Route, method, path string) (engine.Route, bool) {
	segments := strings.Split(path, "/")
	if len(segments) > 0 {
		segments = segments[1:]
	}
	for _, route := range routes {
		if route.Method != strings.ToUpper(method) {
			continue
		}
		pattern := strings.Split(route.Path, "/")[1:]
		if len(pattern) != len(segments) {
			continue
		}
		matched := true
		for i, want := range pattern {
			if strings.HasPrefix(want, "{") && strings.HasSuffix(want, "}") {
				matched = matched && segments[i] != ""
			} else {
				matched = matched && want == segments[i]
			}
		}
		if matched {
			return route, true
		}
	}
	return engine.Route{}, false
}

// requestID is the host's: the client's X-Request-Id when it is sane, else
// the next of the replay's seeded ids.
func (r *run) requestID(req *http.Request) string {
	if id := strings.TrimSpace(req.Header.Get("X-Request-Id")); id != "" && len(id) <= 128 {
		return id
	}
	return seededUUID(requestSeed, r.requests.Add(1))
}

// seededUUID is the n-th seeded id: a version 4 UUID of SHA-256(<prefix><n>).
func seededUUID(prefix string, n uint64) string {
	digest := sha256.Sum256([]byte(prefix + strconv.FormatUint(n, 10)))
	b := digest[:16]
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// rateLimit is the HTTP runtime's MemoryRateLimitStore: a token bucket per
// route.
func (r *run) rateLimit(perMinute float64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			key := req.Method + " " + chi.RouteContext(req.Context()).RoutePattern() + ":unknown"
			if retry, limited := r.admit(key, perMinute); limited {
				w.Header().Set("Retry-After", strconv.FormatInt(retry, 10))
				enginehttp.WriteProblem(w, http.StatusTooManyRequests, "Too Many Requests", "too_many_requests", r.requestID(req), map[string]int64{"retryAfterSeconds": retry})
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

func (r *run) admit(key string, limit float64) (int64, bool) {
	r.bucketsMu.Lock()
	defer r.bucketsMu.Unlock()
	now := time.Now().UnixMilli()
	perMs := limit / 60_000
	b, ok := r.buckets[key]
	if !ok {
		b = &bucket{tokens: limit, at: now}
		r.buckets[key] = b
	}
	if now > b.at {
		b.tokens = math.Min(limit, b.tokens+float64(now-b.at)*perMs)
		b.at = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0, false
	}
	return int64(math.Max(1, math.Ceil((1-b.tokens)/perMs/1000))), true
}

// replayCaller is the host's authentication: the caller the recording
// established, which the replay sends beside the request.
func replayCaller(req *http.Request) engine.Caller {
	encoded := req.Header.Get(callerHeader)
	req.Header.Del(callerHeader)
	if encoded == "" {
		return engine.Caller{}
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return engine.Caller{}
	}
	var recorded struct {
		Principal      *engine.Principal     `json:"principal"`
		Service        *engine.ServiceCaller `json:"service"`
		ServiceRefused *struct {
			Status int    `json:"status"`
			Code   string `json:"code"`
		} `json:"serviceRefused"`
	}
	if err := json.Unmarshal(data, &recorded); err != nil {
		return engine.Caller{}
	}
	caller := engine.Caller{Principal: recorded.Principal, Service: recorded.Service}
	if refused := recorded.ServiceRefused; refused != nil {
		status := refused.Status
		if status == 0 {
			status = http.StatusUnauthorized
		}
		caller.Refused = &engine.Refusal{Status: status, Code: refused.Code}
	}
	return caller
}

// bodyOf is a recorded body's bytes: a JSON body as JSON, any other as its
// text.
func bodyOf(body any, contentType string) []byte {
	isJSON := strings.Contains(contentType, "json")
	if o, ok := body.(*object); ok && o.len() == 1 {
		if raw, ok := text(member(o, "text")); ok {
			if _, err := decode([]byte(raw)); !isJSON || err != nil {
				return []byte(raw)
			}
		}
	}
	return encode(body)
}

// exchange sends a request through the app and compares its response.
func (r *run) exchange(step *object) error {
	recorded, err := r.symbols.substitute(member(step, "request"))
	if err != nil {
		return err
	}
	method := textOf(member(recorded, "method"))
	if method == "" {
		method = http.MethodGet
	}
	path := textOf(member(recorded, "path"))
	if path == "" {
		path = "/"
	}
	var a *app
	appName, named := text(member(step, "app"))
	for i := len(r.apps) - 1; i >= 0; i-- {
		if !named || r.apps[i].name == appName {
			a = r.apps[i]
			break
		}
	}
	if a == nil {
		return errors.New("no app is served")
	}
	for i := len(r.engines) - 1; i >= 0; i-- {
		e := r.engines[i]
		if (a.engine == "" || e.name == a.engine) && e.e != nil {
			if now, ok := integer(member(step, "now")); ok && e.fixedClock {
				if _, err := e.e.Do(context.Background(), engine.Request{Op: "abi", Now: &now}); err != nil {
					return err
				}
			}
			if !e.fixedClock {
				r.pastLatest()
			}
			break
		}
	}
	headers, _ := member(recorded, "headers").(*object)
	contentType := textOf(member(headers, "content-type"))
	var body io.Reader
	if given, ok := recorded.(*object).get("body"); ok {
		body = strings.NewReader(string(bodyOf(given, contentType)))
	}
	target := &url.URL{Scheme: "http", Host: a.server.Listener.Addr().String(), Opaque: path, RawQuery: textOf(member(recorded, "query"))}
	req, err := http.NewRequest(method, a.server.URL, body)
	if err != nil {
		return err
	}
	req.URL = target
	for _, key := range headers.keysOrNone() {
		req.Header.Set(key, textOf(headers.members[key]))
	}
	if caller, ok := recorded.(*object).get("caller"); ok {
		req.Header.Set(callerHeader, base64.RawURLEncoding.EncodeToString(encode(caller)))
	}
	response, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	expected := member(step, "response")
	status, _ := integer(member(expected, "status"))
	gotContentType := response.Header.Get("Content-Type")
	isStream := strings.HasPrefix(gotContentType, "text/event-stream")
	if !isStream {
		defer response.Body.Close()
	}
	if int64(response.StatusCode) != status {
		data, _ := io.ReadAll(response.Body)
		if isStream {
			response.Body.Close()
		}
		return fmt.Errorf("%s %s: expected status %d, got %d: %s", method, path, status, response.StatusCode, data)
	}
	expectedHeaders, _ := member(expected, "headers").(*object)
	for _, name := range responseHeaders {
		want, wanted := expectedHeaders.get(name)
		values := response.Header.Values(name)
		got := strings.Join(values, ", ")
		switch {
		case wanted && len(values) > 0:
			if err := r.symbols.matches(want, got, "/headers/"+name); err != nil {
				return err
			}
		case wanted:
			return fmt.Errorf("%s %s: expected header %s: %s, got none", method, path, name, show(want))
		case len(values) > 0:
			return fmt.Errorf("%s %s: expected no header %s, got %s", method, path, name, got)
		}
	}
	wantBody, wantsBody := expected.(*object).get("body")
	if isStream {
		label := textOf(member(step, "stream"))
		if label == "" {
			response.Body.Close()
			return errors.New("a stream the recording does not name")
		}
		r.streams = append(r.streams, readStream(label, response.Body))
		if wantsBody {
			return fmt.Errorf("%s %s: expected the body %s, got a stream", method, path, show(wantBody))
		}
		return nil
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		if wantsBody {
			return fmt.Errorf("%s %s: expected the body %s, got none", method, path, show(wantBody))
		}
		return nil
	}
	var actual any
	if strings.Contains(gotContentType, "json") {
		if parsed, err := decode(data); err == nil {
			actual = parsed
		}
	}
	if actual == nil {
		o := newObject()
		o.set("text", string(data))
		actual = o
	}
	if !wantsBody {
		return fmt.Errorf("%s %s: expected no body, got %s", method, path, data)
	}
	return r.symbols.matches(wantBody, actual, "/body")
}

func (o *object) keysOrNone() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// readStream reads a stream's frames as the recorder does, leaving out the
// heartbeat's comments, until the body ends.
func readStream(label string, body io.ReadCloser) *openStream {
	s := &openStream{label: label, body: body, frames: make(chan any, 1024)}
	go func() {
		defer close(s.frames)
		reader := bufio.NewReader(body)
		var block []string
		for {
			line, err := reader.ReadString('\n')
			if line != "" && strings.HasSuffix(line, "\n") {
				line = strings.TrimSuffix(line, "\n")
				if line == "" {
					if frame := frameOf(block); frame != nil {
						s.frames <- frame
					}
					block = nil
				} else {
					block = append(block, line)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

func frameOf(lines []string) any {
	frame := newObject()
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, ":"):
			frame.set("comment", strings.TrimSpace(line[1:]))
		case strings.HasPrefix(line, "event: "):
			frame.set("event", line[len("event: "):])
		case strings.HasPrefix(line, "id: "):
			frame.set("id", line[len("id: "):])
		case strings.HasPrefix(line, "data: "):
			data := line[len("data: "):]
			if parsed, err := decode([]byte(data)); err == nil {
				frame.set("data", parsed)
			} else {
				frame.set("text", data)
			}
		}
	}
	if frame.len() == 1 && member(frame, "comment") == "keepalive" {
		return nil
	}
	return frame
}

// take waits until the stream holds count frames or ends.
func (s *openStream) take(count int) {
	deadline := time.After(frameWait)
	for len(s.held) < count && !s.ended {
		select {
		case frame, ok := <-s.frames:
			if !ok {
				s.ended = true
				break
			}
			s.held = append(s.held, frame)
		case <-deadline:
			return
		}
	}
}

// stream compares the frames a stream sent, or its end.
func (r *run) stream(step *object) error {
	label := textOf(member(step, "stream"))
	var s *openStream
	for _, each := range r.streams {
		if each.label == label {
			s = each
		}
	}
	if s == nil {
		return fmt.Errorf("no stream %s is open", label)
	}
	if member(step, "end") == true {
		if r.tail {
			// The test was over: its cleanup closed the engines, which
			// ends every stream.
			for _, e := range r.engines {
				if e.e != nil {
					_ = e.e.Close()
					e.e = nil
				}
			}
		}
		s.take(math.MaxInt)
		if len(s.held) > 0 {
			return fmt.Errorf("stream %s: expected its end, got frames %s", label, show(s.held))
		}
		if !s.ended {
			return fmt.Errorf("stream %s: expected its end, and it went on", label)
		}
		return nil
	}
	expected := list(member(step, "frames"))
	s.take(len(expected))
	count := min(len(expected), len(s.held))
	got := s.held[:count]
	s.held = s.held[count:]
	if len(got) < len(expected) {
		return fmt.Errorf("stream %s: expected %d frames, got %d: %s", label, len(expected), len(got), show(got))
	}
	for i, want := range expected {
		if err := r.symbols.matches(want, got[i], fmt.Sprintf("/frames/%d", i)); err != nil {
			return err
		}
	}
	return nil
}

// finish closes the case's streams, engines and servers, and drops its
// Postgres schemas.
func (r *run) finish() {
	for _, e := range r.engines {
		if e.e != nil {
			_ = e.e.Close()
		}
	}
	for _, s := range r.streams {
		_ = s.body.Close()
	}
	for _, a := range r.apps {
		a.server.CloseClientConnections()
		a.server.Close()
	}
	r.client.CloseIdleConnections()
	if len(r.schemas) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, r.db.url)
	if err != nil {
		r.t.Logf("drop the replay's schemas: %v", err)
		return
	}
	defer conn.Close(ctx)
	for _, schema := range r.schemas {
		if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			r.t.Logf("drop schema %s: %v", schema, err)
		}
	}
}
