// This file runs inside the generated API module of router-options-api
// (TestRouterOptionsAnswerTheirRequests copies it there, twice: once with
// the [go_api] table of the naming file at its defaults and once with each
// key set, which router_options_mode_test.go's optionsSet tells apart). It
// registers the generated routes with an implementation that records each
// call and sends each request to them over httptest. A case holds the
// answer under each table; a case whose two answers are equal shows that
// the key leaves that request alone.
package routeroptionsapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/router-options-api"
	types "example.com/schemas/types/go/router-options-api"
)

// widgets records the input of the last call.
type widgets struct {
	calls int
	last  any
}

func (s *widgets) record(v any) *string {
	s.calls++
	s.last = v
	out := "ok"
	return &out
}

func (s *widgets) SaveWidget(_ context.Context, input *types.WidgetInput) (*string, error) {
	return s.record(input), nil
}

func (s *widgets) SaveStrictWidget(_ context.Context, input *types.StrictWidgetInput) (*string, error) {
	return s.record(input), nil
}

func (s *widgets) TouchWidgets(_ context.Context, input *types.WidgetInput) (*string, error) {
	return s.record(input), nil
}

func (s *widgets) UploadWidgetFile(_ context.Context, id string) (*string, error) {
	return s.record(id), nil
}

func (s *widgets) ClaimWidget(_ context.Context, token types.IdentityUUID) (*string, error) {
	return s.record(token), nil
}

func (s *widgets) FindWidget(_ context.Context, id string, label *string, codes []string) (*string, error) {
	return s.record(map[string]any{"id": id, "label": label, "codes": codes}), nil
}

// answer is what a caller sees of one response: its status, and of a
// problem its code, its detail, the reason and type of its details, and
// each field error as path=validator.
type answer struct {
	Status int
	Code   string
	Detail string
	Reason string
	Type   string
	Errors string
}

func (a answer) String() string {
	out, _ := json.Marshal(a)
	return string(out)
}

// read reads the answer of rec.
func read(t *testing.T, rec *httptest.ResponseRecorder) answer {
	t.Helper()
	got := answer{Status: rec.Code}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") {
		return got
	}
	var problem struct {
		Code    string                       `json:"code"`
		Detail  string                       `json:"detail"`
		Errors  map[string][]json.RawMessage `json:"errors"`
		Details struct {
			Reason string `json:"reason"`
			Type   string `json:"type"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("problem %q is not JSON: %v", rec.Body.String(), err)
	}
	got.Code, got.Detail = problem.Code, problem.Detail
	got.Reason, got.Type = problem.Details.Reason, problem.Details.Type
	var errs []string
	for path, list := range problem.Errors {
		for _, raw := range list {
			var item struct {
				Validator string `json:"validator"`
			}
			_ = json.Unmarshal(raw, &item)
			errs = append(errs, path+"="+item.Validator)
		}
	}
	sort.Strings(errs)
	got.Errors = strings.Join(errs, " ")
	return got
}

func newRouter(t *testing.T) (http.Handler, *widgets) {
	t.Helper()
	impl := &widgets{}
	r := chi.NewRouter()
	if err := api.RegisterRoutes(r, api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Widget: impl},
	}); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}
	return r, impl
}

// send sends one request and reads its answer.
func send(t *testing.T, router http.Handler, method, target string, body io.Reader) answer {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return read(t, rec)
}

// routerCase is one request and its answer under each table.
type routerCase struct {
	name, method, target, body string
	defaults, options          answer
}

func (tc routerCase) want() answer {
	if optionsSet {
		return tc.options
	}
	return tc.defaults
}

func runCases(t *testing.T, cases []routerCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, _ := newRouter(t)
			var body io.Reader
			if tc.body != "" || tc.method != http.MethodGet {
				body = strings.NewReader(tc.body)
			}
			got := send(t, router, tc.method, tc.target, body)
			if want := tc.want(); got != want {
				t.Errorf("%s %s %q:\n got %s\nwant %s", tc.method, tc.target, tc.body, got, want)
			}
		})
	}
}

// TestValidationErrorCode: go_api.validation_error_code is the code of the
// validation problem of a query value that breaks a rule, a query list
// past its bound and a body that its type's Validate refuses.
func TestValidationErrorCode(t *testing.T) {
	runCases(t, []routerCase{
		{
			name: "a query value shorter than its rule", method: http.MethodGet, target: "/api/widgets/w1?label=a",
			defaults: answer{Status: 400, Code: "bad_request", Detail: "Validation failed", Errors: "label=minLength"},
			options:  answer{Status: 400, Code: "VALIDATION-1", Detail: "Validation failed", Errors: "label=min_length"},
		},
		{
			name: "a query list past its bound", method: http.MethodGet, target: "/api/widgets/w1?codes=a,b,c",
			defaults: answer{Status: 400, Code: "bad_request", Detail: "Validation failed", Errors: "codes=listMax"},
			options:  answer{Status: 400, Code: "VALIDATION-1", Detail: "Validation failed", Errors: "codes=list_max"},
		},
		{
			name: "a valid query", method: http.MethodGet, target: "/api/widgets/w1?label=ab&codes=a,b",
			defaults: answer{Status: 200},
			options:  answer{Status: 200},
		},
	})
}

// TestValidatorNames: go_api.validator_names renames the validators of the
// route's own checks, a query value's length and a query list's bound, and
// leaves a validator it does not list alone.
func TestValidatorNames(t *testing.T) {
	runCases(t, []routerCase{
		{
			name: "a query value longer than its rule", method: http.MethodGet, target: "/api/widgets/w1?label=abcde",
			defaults: answer{Status: 400, Code: "bad_request", Detail: "Validation failed", Errors: "label=maxLength"},
			options:  answer{Status: 400, Code: "VALIDATION-1", Detail: "Validation failed", Errors: "label=max_length"},
		},
		{
			name: "a query list past its bound", method: http.MethodGet, target: "/api/widgets/w1?codes=a&codes=b,c",
			defaults: answer{Status: 400, Code: "bad_request", Detail: "Validation failed", Errors: "codes=listMax"},
			options:  answer{Status: 400, Code: "VALIDATION-1", Detail: "Validation failed", Errors: "codes=list_max"},
		},
	})
}

// unknownKeys is the answer of a body with undeclared keys, which every
// generated server refuses.
func unknownKeys(key string) answer {
	return answer{Status: 400, Code: "bad_request", Detail: "Request body does not match the declared input", Reason: "unknown fields: " + key, Errors: key + "=unknown"}
}

// of is a with the input type the refusal names.
func (a answer) of(inputType string) answer {
	a.Type = inputType
	return a
}

// TestStrictInputRefusals: under go_api.strict_input_refusals = "decoder"
// the refusal of a closed type's undeclared key names the type, and a
// string its enum lacks does not match the declared type, where the core
// answers it as an "enum" field error. An open type's answers stay.
func TestStrictInputRefusals(t *testing.T) {
	mismatch := answer{Status: 400, Code: "bad_request", Detail: "Request body does not match the declared input", Reason: "does not match the declared type"}
	enumError := answer{Status: 400, Code: "bad_request", Detail: "Request body does not match the declared input", Reason: "validation failed", Errors: "size=enum"}
	runCases(t, []routerCase{
		{
			name: "an undeclared key of a closed type", method: http.MethodPost, target: "/api/strict-widgets", body: `{"name":"ab","count":1,"extra":true}`,
			defaults: unknownKeys("extra"),
			options:  unknownKeys("extra").of("StrictWidgetInput"),
		},
		{
			name: "a string a closed type's enum lacks", method: http.MethodPost, target: "/api/strict-widgets", body: `{"name":"ab","count":1,"size":"huge"}`,
			defaults: enumError,
			options:  mismatch,
		},
		{
			name: "a string an open type's enum lacks", method: http.MethodPost, target: "/api/widgets", body: `{"name":"ab","count":1,"size":"huge"}`,
			defaults: enumError,
			options:  enumError,
		},
		{
			name: "a closed type's valid body", method: http.MethodPost, target: "/api/strict-widgets", body: `{"name":"ab","count":1,"size":"large"}`,
			defaults: answer{Status: 200},
			options:  answer{Status: 200},
		},
	})
}

// TestUndeclaredInputKeys: under go_api.undeclared_input_keys = "keep" an
// open input type's undeclared key goes on to its decoder, which ignores
// it, and a key in another case reads into its field; a closed type's is
// refused under either value.
func TestUndeclaredInputKeys(t *testing.T) {
	runCases(t, []routerCase{
		{
			name: "an undeclared key", method: http.MethodPost, target: "/api/widgets", body: `{"name":"ab","count":1,"extra":true}`,
			defaults: unknownKeys("extra"),
			options:  answer{Status: 200},
		},
		{
			name: "a key in another case", method: http.MethodPost, target: "/api/widgets", body: `{"Name":"ab","count":1}`,
			defaults: unknownKeys("Name"),
			options:  answer{Status: 200},
		},
		{
			name: "an undeclared key of an optional input", method: http.MethodPost, target: "/api/widgets/touch", body: `{"name":"ab","count":1,"extra":true}`,
			defaults: unknownKeys("extra"),
			options:  answer{Status: 200},
		},
		{
			name: "an undeclared key of a closed type", method: http.MethodPost, target: "/api/strict-widgets", body: `{"name":"ab","count":1,"extra":true}`,
			defaults: unknownKeys("extra"),
			options:  unknownKeys("extra").of("StrictWidgetInput"),
		},
		{
			name: "a declared body", method: http.MethodPost, target: "/api/widgets", body: `{"name":"ab","count":1,"size":"small","tags":["a"]}`,
			defaults: answer{Status: 200},
			options:  answer{Status: 200},
		},
	})
}

// notJSON is the answer of a body that is not one JSON value.
var notJSON = answer{Status: 400, Code: "bad_request", Detail: "Request body is not valid JSON"}

// TestInputTrailingData: under go_api.input_trailing_data = "ignore" the
// route reads the first JSON value of the body, of an open, a closed and an
// optional input alike, and a body that is not JSON is refused under
// either value.
func TestInputTrailingData(t *testing.T) {
	runCases(t, []routerCase{
		{
			name: "trailing data", method: http.MethodPost, target: "/api/widgets", body: `{"name":"ab","count":1} trailing`,
			defaults: notJSON,
			options:  answer{Status: 200},
		},
		{
			name: "two objects", method: http.MethodPost, target: "/api/widgets", body: `{"name":"ab","count":1}{}`,
			defaults: notJSON,
			options:  answer{Status: 200},
		},
		{
			name: "trailing data of a closed type", method: http.MethodPost, target: "/api/strict-widgets", body: `{"name":"ab","count":1} x`,
			defaults: notJSON,
			options:  answer{Status: 200},
		},
		{
			name: "trailing data of an optional input", method: http.MethodPost, target: "/api/widgets/touch", body: `{"name":"ab","count":1} x`,
			defaults: notJSON,
			options:  answer{Status: 200},
		},
		{
			name: "a body cut short", method: http.MethodPost, target: "/api/widgets", body: `{"name":`,
			defaults: notJSON,
			options:  notJSON,
		},
	})
}

// TestOptionalInputEmptyBody: under go_api.optional_input_empty_body =
// "object" the empty body of an optional input is {}, which its type then
// validates, and a required input's empty body is refused under either
// value.
func TestOptionalInputEmptyBody(t *testing.T) {
	bodyRequired := answer{Status: 400, Code: "bad_request", Detail: "Request body is required"}
	runCases(t, []routerCase{
		{
			name: "an empty body of an optional input", method: http.MethodPost, target: "/api/widgets/touch", body: "",
			defaults: bodyRequired,
			options: answer{Status: 400, Code: "bad_request", Detail: "Request body does not match the declared input", Reason: "validation failed",
				Errors: "count=min name=minLength"},
		},
		{
			name: "white space of an optional input", method: http.MethodPost, target: "/api/widgets/touch", body: " \n",
			defaults: bodyRequired,
			options: answer{Status: 400, Code: "bad_request", Detail: "Request body does not match the declared input", Reason: "validation failed",
				Errors: "count=min name=minLength"},
		},
		{
			name: "an empty body of a required input", method: http.MethodPost, target: "/api/widgets", body: "",
			defaults: bodyRequired,
			options:  bodyRequired,
		},
	})
}

// TestBodyArguments: under go_api.body_arguments = "struct" a route reads
// its body arguments as one struct: the null is the empty object, whose
// required argument its type's ValidateRequired refuses at its name, a
// value its type's decoder refuses is a decode refusal, and trailing data
// is not read, as the core's reading does not read it.
func TestBodyArguments(t *testing.T) {
	const token = `"0b7b4c4e-6a43-4d4b-9a8e-1f2d3c4b5a69"`
	runCases(t, []routerCase{
		{
			name: "a valid token", method: http.MethodPost, target: "/api/widgets/claim", body: `{"token":` + token + `}`,
			defaults: answer{Status: 200},
			options:  answer{Status: 200},
		},
		{
			name: "the null", method: http.MethodPost, target: "/api/widgets/claim", body: `null`,
			defaults: answer{Status: 400, Detail: "Invalid request body"},
			options:  answer{Status: 400, Code: "VALIDATION-1", Detail: "Validation failed", Errors: "token=required"},
		},
		{
			name: "no token", method: http.MethodPost, target: "/api/widgets/claim", body: `{}`,
			defaults: answer{Status: 400, Code: "bad_request", Detail: "Validation failed", Errors: "token=required"},
			options:  answer{Status: 400, Code: "VALIDATION-1", Detail: "Validation failed", Errors: "token=required"},
		},
		{
			name: "a token that is not a string", method: http.MethodPost, target: "/api/widgets/claim", body: `{"token":5}`,
			defaults: answer{Status: 400, Code: "bad_request", Detail: "Validation failed", Errors: "token=type"},
			options:  answer{Status: 400, Code: "bad_request", Detail: "Request body does not match the declared input", Reason: "does not match the declared type"},
		},
		{
			name: "trailing data", method: http.MethodPost, target: "/api/widgets/claim", body: `{"token":` + token + `} x`,
			defaults: answer{Status: 200},
			options:  answer{Status: 200},
		},
	})
}

// TestQueryParamListEmptyItem: under go_api.query_param_list_empty_item a
// @query list with an empty item is refused with the key's text, where
// the core drops the item.
func TestQueryParamListEmptyItem(t *testing.T) {
	emptyItem := answer{Status: 400, Detail: "codes cannot contain empty values"}
	runCases(t, []routerCase{
		{
			name: "an empty value", method: http.MethodGet, target: "/api/widgets/w1?codes=",
			defaults: answer{Status: 200},
			options:  emptyItem,
		},
		{
			name: "an empty item", method: http.MethodGet, target: "/api/widgets/w1?codes=a,,b",
			defaults: answer{Status: 200},
			options:  emptyItem,
		},
		{
			name: "an item of white space", method: http.MethodGet, target: "/api/widgets/w1?codes=a&codes=%20",
			defaults: answer{Status: 200},
			options:  emptyItem,
		},
		{
			name: "no empty item", method: http.MethodGet, target: "/api/widgets/w1?codes=a&codes=b",
			defaults: answer{Status: 200},
			options:  answer{Status: 200},
		},
	})
}

// TestPathParamError: go_api.path_param_error is the detail of the 400 of a
// path parameter that does not decode to UTF-8.
func TestPathParamError(t *testing.T) {
	runCases(t, []routerCase{
		{
			name: "a path parameter that is not UTF-8", method: http.MethodGet, target: "/api/widgets/%ff",
			defaults: answer{Status: 400, Detail: "id must be percent-encoded UTF-8"},
			options:  answer{Status: 400, Detail: "id is not a path segment"},
		},
		{
			name: "a percent-encoded path parameter", method: http.MethodGet, target: "/api/widgets/a%20b",
			defaults: answer{Status: 200},
			options:  answer{Status: 200},
		},
	})
}
