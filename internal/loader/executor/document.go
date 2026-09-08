package executor

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// documentHarnessSource is the standalone harness bun runs to execute a
// sidecar document module (a registered DocumentSpec's File, such as
// deploy.values.ts) and capture its default export. Written to a temp file
// per invocation, like harness.ts.
//
//go:embed harness_document.ts
var documentHarnessSource []byte

// documentOutput is the JSON document the harness prints: the
// default-exported document, the module graph the bundler resolved for it,
// plus any diagnostics collected while importing.
type documentOutput struct {
	Document         json.RawMessage `json:"document"`
	AuthoringImports []string        `json:"authoringImports"`
	Diagnostics      []Diagnostic    `json:"diagnostics"`
}

// Document is one executed sidecar document module: its default export as
// raw JSON plus the transitive module graph (absolute file paths) the
// harness crawled for it.
type Document struct {
	Document         json.RawMessage
	AuthoringImports []string
}

// RunDocument executes one sidecar document module through the document
// harness and returns its default export. file is the service-relative
// path. The harness runs with the service directory as its working
// directory so bun picks up the service tsconfig (@superschematic/* path mappings).
func RunDocument(servicePath, file string, opts ...Option) (result *Document, err error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	var bunPath string
	if err := o.profile.Measure("executor.lookup-bun", func() error {
		var err error
		bunPath, err = exec.LookPath("bun")
		return err
	}); err != nil {
		return nil, fmt.Errorf("%s executes with bun, which was not found on PATH (see the schemas root package.json for the pinned version): %w", file, err)
	}

	var harnessPath string
	if err := o.profile.Measure("executor.write-harness", func() error {
		var err error
		harnessPath, err = writeHarness(documentHarnessSource)
		return err
	}); err != nil {
		return nil, err
	}
	defer func() {
		removeErr := os.Remove(harnessPath)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("removing execution harness %s: %w", harnessPath, removeErr)
		}
	}()

	// The harness dynamic-imports the path as given; only an absolute path
	// resolves independently of the harness's temp location.
	abs, err := filepath.Abs(filepath.Join(servicePath, filepath.FromSlash(file)))
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", file, err)
	}

	cmd := exec.Command(bunPath, "run", harnessPath, abs)
	cmd.Dir = servicePath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	done := o.profile.Start("executor.bun-run")
	runErr := cmd.Run()
	done()

	var out documentOutput
	if decodeErr := json.Unmarshal(stdout.Bytes(), &out); decodeErr != nil {
		if runErr != nil {
			return nil, fmt.Errorf("%s execution failed: %w\n%s", file, runErr, tail(stderr.String(), 20))
		}
		return nil, fmt.Errorf("%s execution produced unreadable output: %w", file, decodeErr)
	}

	if len(out.Diagnostics) > 0 {
		errs := make([]error, len(out.Diagnostics))
		for i, d := range out.Diagnostics {
			if d.File == "" {
				errs[i] = errors.New(d.Message)
				continue
			}
			errs[i] = fmt.Errorf("%s: %s", d.File, d.Message)
		}
		return nil, errors.Join(errs...)
	}
	if runErr != nil {
		return nil, fmt.Errorf("%s execution failed: %w\n%s", file, runErr, tail(stderr.String(), 20))
	}
	return &Document{Document: out.Document, AuthoringImports: out.AuthoringImports}, nil
}
