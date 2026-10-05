// Package d1fake serves Cloudflare D1's REST query endpoints over a SQLite
// file, for the runner's tests. It keeps to what Cloudflare documents and
// what the D1 driver relies on; each guess is marked as one.
//
// What it mirrors:
//   - POST /client/v4/accounts/{account}/d1/database/{database}/query and
//     .../raw, with a bearer token, taking {"sql", "params"} or {"batch":
//     [{"sql", "params"}, ...]}, params strings, and answering {"success",
//     "errors", "messages", "result"} with one result per statement: rows
//     as objects from query, as {"columns", "rows"} from raw, and meta
//     with changes.
//     https://developers.cloudflare.com/api/resources/d1/subresources/database/methods/query/
//   - A request runs as one transaction: a failed statement, or a foreign
//     key still broken at the end, rolls all of it back, and the answer
//     carries no results.
//   - Foreign keys are on. A request runs inside a transaction, where
//     SQLite makes PRAGMA foreign_keys a no-op, so PRAGMA foreign_keys = OFF
//     changes nothing, as D1 says of its own: "Because D1 runs every query
//     inside an implicit transaction, user queries cannot change this".
//     https://developers.cloudflare.com/d1/sql-api/foreign-keys/
//   - PRAGMAs last for the request: each request runs on a new connection.
//     PRAGMAs outside D1's list are refused.
//     https://developers.cloudflare.com/d1/sql-api/sql-statements/
//   - BEGIN, COMMIT, END, ROLLBACK, SAVEPOINT and RELEASE are refused.
//   - Requests run one at a time, as D1 runs one write at a time.
package d1fake

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	_ "modernc.org/sqlite"
)

// Options configure a Server.
type Options struct {
	// Token is the bearer token the server takes.
	Token string
	// AccountID and DatabaseID name the one database the server serves.
	AccountID  string
	DatabaseID string
}

// Server is a fake D1 database behind its REST API.
type Server struct {
	opts   Options
	path   string
	db     *sql.DB
	server *httptest.Server
	mu     sync.Mutex
}

// New serves the SQLite file at path, creating it when it is missing.
func New(path string, opts Options) (*Server, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// No connection is reused, so no request's PRAGMA outlives it.
	db.SetMaxIdleConns(0)
	s := &Server{opts: opts, path: path, db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /client/v4/accounts/{account}/d1/database/{database}/{endpoint}", s.serve)
	s.server = httptest.NewServer(mux)
	return s, nil
}

// BaseURL is the API's base URL, the D1 driver's Options.BaseURL.
func (s *Server) BaseURL() string { return s.server.URL + "/client/v4" }

// DatabaseURL is the d1:// URL of the database the server serves.
func (s *Server) DatabaseURL() string { return "d1://" + s.opts.AccountID + "/" + s.opts.DatabaseID }

// Path is the SQLite file the server serves.
func (s *Server) Path() string { return s.path }

// Client is an HTTP client for the server.
func (s *Server) Client() *http.Client { return s.server.Client() }

// Close stops the server.
func (s *Server) Close() {
	s.server.Close()
	_ = s.db.Close()
}

// Cloudflare's error codes. 7500 is the code D1 gives a failed statement;
// the others are guesses.
const (
	codeAuthentication = 10000
	codeNotFound       = 7404
	codeMalformed      = 7400
	codeStatement      = 7500
)

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	Result   []any      `json:"result"`
	Success  bool       `json:"success"`
	Errors   []apiError `json:"errors"`
	Messages []any      `json:"messages"`
}

func reply(w http.ResponseWriter, status int, body envelope) {
	if body.Result == nil {
		body.Result = []any{}
	}
	if body.Errors == nil {
		body.Errors = []apiError{}
	}
	body.Messages = []any{}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// fail answers with one error. The HTTP statuses are guesses.
func fail(w http.ResponseWriter, status, code int, message string) {
	reply(w, status, envelope{Errors: []apiError{{Code: code, Message: message}}})
}

// request is a request body: one statement or a batch.
type request struct {
	SQL    *string           `json:"sql"`
	Params []json.RawMessage `json:"params"`
	Batch  []struct {
		SQL    string            `json:"sql"`
		Params []json.RawMessage `json:"params"`
	} `json:"batch"`
}

type statement struct {
	sql    string
	params []any
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.opts.Token {
		fail(w, http.StatusForbidden, codeAuthentication, "Authentication error")
		return
	}
	endpoint := r.PathValue("endpoint")
	if r.PathValue("account") != s.opts.AccountID || r.PathValue("database") != s.opts.DatabaseID || (endpoint != "query" && endpoint != "raw") {
		fail(w, http.StatusNotFound, codeNotFound, "Not found")
		return
	}
	var body request
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, codeMalformed, "The request is malformed: "+err.Error())
		return
	}
	statements, err := parse(body)
	if err != nil {
		fail(w, http.StatusBadRequest, codeMalformed, "The request is malformed: "+err.Error())
		return
	}
	s.mu.Lock()
	results, err := s.run(r.Context(), statements, endpoint == "raw")
	s.mu.Unlock()
	if err != nil {
		// A guess: D1 names no statement in a failed request's answer.
		fail(w, http.StatusBadRequest, codeStatement, err.Error())
		return
	}
	reply(w, http.StatusOK, envelope{Result: results, Success: true})
}

// parse reads the statements of a request. Several statements in one sql
// take no params ("params with multiple statements is not supported", as
// D1 is reported to answer); a batch element holds one statement (a guess).
func parse(body request) ([]statement, error) {
	switch {
	case body.SQL != nil && body.Batch != nil:
		return nil, errors.New("sql and batch together")
	case body.SQL != nil:
		params, err := stringParams(body.Params)
		if err != nil {
			return nil, err
		}
		split := splitStatements(*body.SQL)
		switch {
		case len(split) == 0:
			return nil, errors.New("no statement")
		case len(split) > 1 && len(params) > 0:
			return nil, errors.New("params with multiple statements is not supported")
		}
		out := make([]statement, len(split))
		for i, text := range split {
			out[i] = statement{sql: text}
		}
		out[0].params = params
		return out, nil
	case len(body.Batch) > 0:
		out := make([]statement, len(body.Batch))
		for i, element := range body.Batch {
			params, err := stringParams(element.Params)
			if err != nil {
				return nil, err
			}
			if n := len(splitStatements(element.SQL)); n != 1 {
				return nil, fmt.Errorf("batch element %d holds %d statements", i, n)
			}
			out[i] = statement{sql: element.SQL, params: params}
		}
		return out, nil
	}
	return nil, errors.New("no sql and no batch")
}

// stringParams reads params, which the API documents as strings.
func stringParams(raw []json.RawMessage) ([]any, error) {
	out := make([]any, len(raw))
	for i, param := range raw {
		var s string
		if err := json.Unmarshal(param, &s); err != nil {
			return nil, fmt.Errorf("param %d is not a string", i+1)
		}
		out[i] = s
	}
	return out, nil
}
