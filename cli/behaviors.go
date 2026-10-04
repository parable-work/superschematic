package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

// behaviorFileSuffix ends the name of every file the behaviors command
// writes: <behavior name>.behavior.json.
const behaviorFileSuffix = ".behavior.json"

// behaviorsFlags holds one behaviors command's flag values.
type behaviorsFlags struct {
	out        string
	check      bool
	extension  string
	pkg        string
	namingPath string
}

// newBehaviorsCmd writes the declaration of every behavior the binary
// registers into the npm package that implements them (D16). It is a
// command of the binary, not a tool of the core module, because an
// extension's declarations are registered only in its own binary.
func newBehaviorsCmd(a *app) *cobra.Command {
	flags := &behaviorsFlags{}
	cmd := &cobra.Command{
		Use:   "behaviors --out <dir> [--package <npm package>] [--extension <name>]",
		Short: "Write each registered behavior's declaration for the package that implements it",
		Long: `behaviors writes the declaration of every behavior this binary registers,
the JSON file its Go package embeds, into a directory of the npm package
that implements it: one <name>.behavior.json per behavior. An engine
implementation carries that copy, so the engine and the compiler read one
declaration.

A copy is canonical: the declaration's keys in their documented order,
each JSON Schema's object keys sorted, two-space indents and a final
newline, whatever the source file's layout. Other *.behavior.json files in
the directory are removed. With --package, only the behaviors that npm
package implements are written: the core's behaviors are implemented by
@superschematic/engine and @superschematic/engine-workqueue, and each
package carries only its own. With --extension, only the behaviors that
extension registered are written. Given both, a behavior must match both.

With --check nothing is written: the command fails, naming each file, when
a copy differs, is missing, or has no registered behavior. CI runs it so a
changed declaration cannot reach the compiler without its implementation's
copy.

Examples:
  superschematic behaviors --package @superschematic/engine --out runtime/engine/typescript/src/behaviors/core/declarations
  superschematic behaviors --package @superschematic/engine-workqueue --out runtime/engine-workqueue/typescript/src/declarations --check
  acme-schematic behaviors --extension acme --out packages/behaviors/declarations
  acme-schematic behaviors --extension acme --out packages/behaviors/declarations --check`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBehaviors(cmd, a, flags)
		},
	}
	cmd.Flags().StringVar(&flags.out, "out", "", "directory the <name>.behavior.json files go in (required)")
	cmd.Flags().BoolVar(&flags.check, "check", false, "write nothing; fail when the directory's copies are not what would be written")
	cmd.Flags().StringVar(&flags.extension, "extension", "", "only the behaviors this extension registered (its Name())")
	cmd.Flags().StringVar(&flags.pkg, "package", "", "only the behaviors this npm package implements (@superschematic/engine, @superschematic/engine-workqueue)")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default: built-in names; behaviors has no service directory to find superschematic.toml from)")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func runBehaviors(cmd *cobra.Command, a *app, flags *behaviorsFlags) error {
	names := naming.Default()
	if flags.namingPath != "" {
		var err error
		if names, err = resolveNaming(flags.namingPath, ""); err != nil {
			return err
		}
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return err
	}
	want := map[string][]byte{}
	extensionHas := false
	for _, behavior := range reg.Behaviors() {
		if flags.extension != "" && behavior.Extension != flags.extension {
			continue
		}
		extensionHas = true
		if flags.pkg != "" && behavior.Package != flags.pkg {
			continue
		}
		data, err := behaviorDeclarationFile(behavior.BehaviorDeclaration)
		if err != nil {
			return fmt.Errorf("behavior %s: %w", behavior.Name, err)
		}
		want[behavior.Name+behaviorFileSuffix] = data
	}
	if flags.extension != "" && !extensionHas {
		return fmt.Errorf("behaviors: extension %q registers no behavior in this binary (registered: %s)", flags.extension, registeredList(reg))
	}
	if flags.pkg != "" && len(want) == 0 {
		return fmt.Errorf("behaviors: no behavior this binary registers%s is implemented by package %q (packages: %s)", extensionClause(flags.extension), flags.pkg, packageList(reg))
	}
	have, err := behaviorFiles(flags.out)
	if err != nil {
		return err
	}
	if flags.check {
		return checkBehaviorFiles(cmd, flags, want, have)
	}
	if err := os.MkdirAll(flags.out, 0o755); err != nil {
		return err
	}
	for _, name := range sortedKeys(want) {
		if bytes.Equal(have[name], want[name]) {
			continue
		}
		if err := os.WriteFile(filepath.Join(flags.out, name), want[name], 0o644); err != nil {
			return err
		}
	}
	for name := range have {
		if _, keep := want[name]; !keep {
			if err := os.Remove(filepath.Join(flags.out, name)); err != nil {
				return err
			}
		}
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "behaviors: %d declaration(s) in %s\n", len(want), flags.out)
	return nil
}

func checkBehaviorFiles(cmd *cobra.Command, flags *behaviorsFlags, want, have map[string][]byte) error {
	var problems []string
	for _, name := range sortedKeys(want) {
		existing, ok := have[name]
		switch {
		case !ok:
			problems = append(problems, name+" is missing")
		case !bytes.Equal(existing, want[name]):
			problems = append(problems, name+" differs from the registered declaration")
		}
	}
	for _, name := range sortedKeys(have) {
		if _, ok := want[name]; !ok {
			problems = append(problems, name+" is no registered behavior's declaration")
		}
	}
	if len(problems) == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "behaviors: %d declaration(s) in %s are current\n", len(want), flags.out)
		return nil
	}
	rerun := cmd.Root().Name() + " behaviors --out " + flags.out
	if flags.pkg != "" {
		rerun += " --package " + flags.pkg
	}
	if flags.extension != "" {
		rerun += " --extension " + flags.extension
	}
	return fmt.Errorf("behaviors: %s: %s; run: %s", flags.out, strings.Join(problems, "; "), rerun)
}

// behaviorDeclarationFile is the canonical copy of a declaration: its keys
// in BehaviorDeclaration's order, each JSON Schema's object keys sorted and
// number literals as written, two-space indents, no HTML escaping and a
// final newline.
func behaviorDeclarationFile(declaration registry.BehaviorDeclaration) ([]byte, error) {
	var err error
	if declaration.ConfigSchema, err = canonicalSchema(declaration.ConfigSchema); err != nil {
		return nil, fmt.Errorf("configSchema: %w", err)
	}
	if declaration.CreateParamsSchema, err = canonicalSchema(declaration.CreateParamsSchema); err != nil {
		return nil, fmt.Errorf("createParamsSchema: %w", err)
	}
	operations := make([]registry.BehaviorOperation, len(declaration.Operations))
	for i, operation := range declaration.Operations {
		if operation.ParamsSchema, err = canonicalSchema(operation.ParamsSchema); err != nil {
			return nil, fmt.Errorf("operation %s paramsSchema: %w", operation.Name, err)
		}
		if operation.ResultSchema, err = canonicalSchema(operation.ResultSchema); err != nil {
			return nil, fmt.Errorf("operation %s resultSchema: %w", operation.Name, err)
		}
		operations[i] = operation
	}
	declaration.Operations = operations
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(declaration); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// canonicalSchema re-encodes a JSON Schema with its object keys sorted and
// its number literals kept, without HTML escaping. An absent schema stays
// absent.
func canonicalSchema(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// behaviorFiles reads every *.behavior.json in dir; a missing dir holds none.
func behaviorFiles(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), behaviorFileSuffix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = data
	}
	return files, nil
}

func registeredList(reg *registry.Registry) string {
	var owned []string
	for _, behavior := range reg.Behaviors() {
		owner := "core"
		if behavior.Extension != "" {
			owner = "extension " + behavior.Extension
		}
		owned = append(owned, behavior.Name+" ("+owner+")")
	}
	if len(owned) == 0 {
		return "none"
	}
	return strings.Join(owned, ", ")
}

// packageList names the npm packages the registered behaviors name, sorted.
func packageList(reg *registry.Registry) string {
	seen := map[string]bool{}
	var packages []string
	for _, behavior := range reg.Behaviors() {
		if behavior.Package != "" && !seen[behavior.Package] {
			seen[behavior.Package] = true
			packages = append(packages, behavior.Package)
		}
	}
	if len(packages) == 0 {
		return "no behavior names one"
	}
	sort.Strings(packages)
	return strings.Join(packages, ", ")
}

func extensionClause(extension string) string {
	if extension == "" {
		return ""
	}
	return " for extension " + extension
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
