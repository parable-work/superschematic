package migrateplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// RequestVersion is the version of the request and the response forms of
// `migrate plan --request`, which docs/src/content/docs/reference/cli.md
// specifies for a runtime library that runs the binary.
const RequestVersion = 1

// Request is a plan asked for from schema documents, as a runtime library
// sends it on the binary's stdin (`migrate plan --request -`): the new
// version as one DB service's documents, the model its database recorded,
// and the renames and allowed hazards a publish names (D117).
type Request struct {
	// Version is RequestVersion.
	Version int `json:"version"`
	// Dialect is the database's: postgres or sqlite.
	Dialect sqlmigrate.Dialect `json:"dialect"`
	// Documents are every schema document of the DB service, each as
	// `format --to=json` writes it.
	Documents []json.RawMessage `json:"documents"`
	// From is the model the database recorded, as
	// `superschematic-migrate status --model` prints it, or null for an
	// empty database. It is required, so a request that leaves it out by
	// mistake is not planned from an empty database.
	From json.RawMessage `json:"from"`
	// Renames are the renames the publish means, as --rename takes them
	// (purchase=order, order.total=order.amount).
	Renames []string `json:"renames,omitempty"`
	// FailOn are hazard classes, or all, whose hazards the response
	// refuses unless Allow names them, as --fail-on takes them.
	FailOn []string `json:"failOn,omitempty"`
	// Allow are hazard ids the publish allows, as --allow takes them.
	Allow []string `json:"allow,omitempty"`
}

// Response answers a Request: the plan, with its hazards and the ids of
// those the request refuses, or the error that left it with none.
type Response struct {
	// Plan is the plan's canonical JSON, as --out writes it and
	// superschematic-migrate apply reads it.
	Plan json.RawMessage
	// Hazards are the plan's hazards in step order.
	Hazards []*sqlmigrate.Hazard
	// Refused are the ids of the hazards of a FailOn class that Allow does
	// not name, in step order.
	Refused []string
	// Error says why there is no plan; empty when there is one.
	Error string
}

// planned and refusal are a Response's two JSON forms.
type planned struct {
	Version int                  `json:"version"`
	Plan    json.RawMessage      `json:"plan"`
	Hazards []*sqlmigrate.Hazard `json:"hazards"`
	Refused []string             `json:"refused"`
}

type refusal struct {
	Version int    `json:"version"`
	Error   string `json:"error"`
}

// ErrorResponse is the response that says err left the request with no
// plan.
func ErrorResponse(err error) *Response {
	return &Response{Error: err.Error()}
}

// JSON writes the response as one line of JSON: version, plan, hazards and
// refused when it holds a plan, else version and error. The plan's bytes
// are its canonical JSON, unescaped.
func (r *Response) JSON() ([]byte, error) {
	var value any = refusal{Version: RequestVersion, Error: r.Error}
	if r.Error == "" {
		hazards, refused := r.Hazards, r.Refused
		if hazards == nil {
			hazards = []*sqlmigrate.Hazard{}
		}
		if refused == nil {
			refused = []string{}
		}
		value = planned{Version: RequestVersion, Plan: r.Plan, Hazards: hazards, Refused: refused}
	}
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ReadRequest decodes a request, refusing a member a request does not have,
// a second JSON value after it, and a version this compiler does not read.
func ReadRequest(r io.Reader) (*Request, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var req Request
	if err := decoder.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the request is empty: want a JSON object with version, dialect, documents and from")
		}
		return nil, fmt.Errorf("not a plan request (%w)", err)
	}
	if decoder.More() {
		return nil, errors.New("not a plan request: more than one JSON value")
	}
	if req.Version != RequestVersion {
		return nil, fmt.Errorf("request version %d; this compiler reads version %d", req.Version, RequestVersion)
	}
	return &req, nil
}

// Answer plans what req asks for, with env's registry and naming. An error
// is a request the planner refuses: a dialect, model, rename, hazard class
// or hazard id it does not read, documents that are not one DB service's,
// or a change the diff cannot plan. A hazard the request does not allow is
// no error: the response lists it in Refused, and the caller refuses the
// publish.
func Answer(req *Request, env Env) (*Response, error) {
	switch req.Dialect {
	case sqlmigrate.Postgres, sqlmigrate.SQLite:
	case "":
		return nil, errors.New("the request names no dialect (dialect): want postgres or sqlite")
	default:
		return nil, fmt.Errorf("dialect %q: want postgres or sqlite", req.Dialect)
	}
	var from *sqlmigrate.Model
	switch trimmed := bytes.TrimSpace(req.From); {
	case len(trimmed) == 0:
		return nil, errors.New("the request names no model to plan from (from): give the model the database recorded, as superschematic-migrate status --model prints it, or null for an empty database")
	case string(trimmed) == "null":
	default:
		model, err := ReadModel(trimmed)
		if err != nil {
			return nil, fmt.Errorf("from: %w", err)
		}
		from = model
	}
	var renames []sqlmigrate.Rename
	for _, value := range req.Renames {
		rename, err := sqlmigrate.ParseRename(value)
		if err != nil {
			return nil, fmt.Errorf("renames: %w", err)
		}
		renames = append(renames, rename)
	}
	classes, err := ParseHazardClasses(req.FailOn)
	if err != nil {
		return nil, fmt.Errorf("failOn: %w", err)
	}
	for _, id := range req.Allow {
		if err := CheckHazardID(id); err != nil {
			return nil, fmt.Errorf("allow: %w", err)
		}
	}
	sources := make([]Source, len(req.Documents))
	for i, doc := range req.Documents {
		sources[i] = Source{Name: fmt.Sprintf("documents[%d]", i), Data: doc}
	}

	plan, err := Plan(sources, from, req.Dialect, renames, env)
	if err != nil {
		return nil, err
	}
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	response := &Response{Plan: canonical, Hazards: plan.Hazards()}
	for _, hazard := range plan.Unallowed(classes, req.Allow) {
		response.Refused = append(response.Refused, hazard.ID)
	}
	return response, nil
}
