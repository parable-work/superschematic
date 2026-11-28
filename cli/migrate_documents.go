package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/migrateplan"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// documentsRefusedFlags are the flags a plan from documents does not take:
// there is no schemas root beside the documents to read a previous version,
// a reader or a query from (D117).
var documentsRefusedFlags = []string{"from-ref", "from-database", "drop", "reader", "database-url"}

// requestRefusedFlags are the flags --request does not take: the request
// holds the documents, the model and the plan's options, and the response
// is the one output. It takes --naming.
var requestRefusedFlags = []string{
	"from", "from-ref", "from-database", "drop", "rename", "reader", "dialect", "out", "format",
	"fail-on", "allow", "print-model", "database-url", "documents", "service",
}

// checkDocumentsFlags refuses, before anything loads, a flag --documents
// does not take.
func (f *migratePlanFlags) checkDocumentsFlags(cmd *cobra.Command) error {
	if !f.documents {
		if f.service != "" {
			return fmt.Errorf("--service names the DB service development mode's General documents lower into; give --documents with it")
		}
		return nil
	}
	var given []string
	for _, name := range documentsRefusedFlags {
		if cmd.Flags().Changed(name) {
			given = append(given, "--"+name)
		}
	}
	if len(given) > 0 {
		return fmt.Errorf("--documents plans from the model a database recorded (--from <model.json>) or an empty database, with no schemas root beside the documents; drop %s", strings.Join(given, ", "))
	}
	return nil
}

// runMigratePlanDocuments plans from one DB service's schema documents,
// the files the arguments name and the stream - reads from stdin, to the
// model --from names, or from an empty database (D117).
func runMigratePlanDocuments(cmd *cobra.Command, a *app, flags *migratePlanFlags, paths []string) error {
	in, err := flags.parse(cmd)
	if err != nil {
		return err
	}
	sources, err := readDocumentSources(cmd.InOrStdin(), paths)
	if err != nil {
		return err
	}
	env, err := documentsEnv(a, flags.namingPath, paths)
	if err != nil {
		return err
	}
	env.Service = flags.service
	schema, toModel, err := migrateplan.Documents(sources, in.dialect, env)
	if err != nil {
		return fmt.Errorf("migrate plan: %w", err)
	}
	if flags.printModel {
		canonical, err := toModel.CanonicalJSON()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", canonical)
		return err
	}

	var fromModel *sqlmigrate.Model
	if flags.from == "" {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "migrate plan: no previous version (--from); planning from an empty database")
	} else {
		info, err := os.Stat(flags.from)
		if err != nil {
			return fmt.Errorf("--from: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("--from %s is a directory; with --documents, --from takes the model the database recorded, a JSON file as superschematic-migrate status --model prints it", flags.from)
		}
		if fromModel, err = readModelFile(flags.from, schema.Name, in.dialect); err != nil {
			return err
		}
	}
	plan, err := sqlmigrate.Diff(fromModel, toModel, sqlmigrate.Options{Renames: in.renames})
	if err != nil {
		return err
	}
	return writeMigratePlan(cmd, flags, in, plan)
}

// readDocumentSources reads each document the arguments name: a file, or
// - for the documents on stdin, one JSON value after another.
func readDocumentSources(stdin io.Reader, paths []string) ([]migrateplan.Source, error) {
	stdins := 0
	for _, path := range paths {
		if path == "-" {
			stdins++
		}
	}
	if stdins > 1 {
		return nil, errors.New("--documents names - twice; stdin is read once")
	}
	var sources []migrateplan.Source
	for _, path := range paths {
		if path == "-" {
			streamed, err := migrateplan.ReadSources(stdin, "stdin")
			if err != nil {
				return nil, err
			}
			sources = append(sources, streamed...)
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("schema document not found: %s", path)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%s is a directory; --documents takes schema documents, each a file as format --to=json writes it, or - for stdin", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sources = append(sources, migrateplan.Source{Name: path, Data: data})
	}
	return sources, nil
}

// documentsEnv resolves the naming and the registry a plan from documents
// loads with: --naming, else the superschematic.toml found walking up from
// the first document file, as format finds it, else the defaults.
func documentsEnv(a *app, namingPath string, paths []string) (migrateplan.Env, error) {
	names := naming.Default()
	var err error
	switch {
	case namingPath != "":
		names, err = naming.LoadFile(namingPath)
	default:
		for _, path := range paths {
			if path != "-" {
				names, err = naming.Discover(path)
				break
			}
		}
	}
	if err != nil {
		return migrateplan.Env{}, err
	}
	naming.SetActive(names)
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return migrateplan.Env{}, err
	}
	return migrateplan.Env{Registry: reg, Naming: names}, nil
}

// runMigratePlanRequest answers the plan request --request names
// (migrateplan.Request) with one line of JSON on stdout: the plan with its
// hazards and the ids of those the request refuses, or the error that left
// it with none, which the command also returns, so it exits 1.
func runMigratePlanRequest(cmd *cobra.Command, a *app, flags *migratePlanFlags) error {
	response, err := answerMigratePlanRequest(cmd, a, flags)
	if err != nil {
		response = migrateplan.ErrorResponse(err)
	}
	line, jsonErr := response.JSON()
	if jsonErr != nil {
		return jsonErr
	}
	if _, writeErr := cmd.OutOrStdout().Write(line); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return fmt.Errorf("migrate plan --request: %w", err)
	}
	return nil
}

func answerMigratePlanRequest(cmd *cobra.Command, a *app, flags *migratePlanFlags) (*migrateplan.Response, error) {
	var given []string
	for _, name := range requestRefusedFlags {
		if cmd.Flags().Changed(name) {
			given = append(given, "--"+name)
		}
	}
	if len(given) > 0 {
		return nil, fmt.Errorf("the request holds the documents, the model and the plan's options; drop %s", strings.Join(given, ", "))
	}
	var req *migrateplan.Request
	var err error
	if flags.request == "-" {
		req, err = migrateplan.ReadRequest(cmd.InOrStdin())
	} else {
		var data []byte
		if data, err = os.ReadFile(flags.request); err != nil {
			return nil, err
		}
		req, err = migrateplan.ReadRequest(bytes.NewReader(data))
	}
	if err != nil {
		return nil, err
	}
	env, err := documentsEnv(a, flags.namingPath, nil)
	if err != nil {
		return nil, err
	}
	return migrateplan.Answer(req, env)
}
