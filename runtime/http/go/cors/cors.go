// Package cors answers the cross-origin requests a static site's browser
// makes to the APIs it calls (D55; docs/stack-model.md, section 8.10).
// Each API a site calls has a CORS field in a stack, the origins of the
// sites that call it, which stackconfig.LoadCORS reads; its server answers
// CORS for those origins, with credentials, the Authorization header and
// the methods the API's operations use. It answers no other origin itself:
// a preflight from one reaches the API's router, whose identity runtime
// answers its config's trusted origins (D55, amended).
//
// The TypeScript HTTP runtime's cors module decides the same way. The
// vectors in runtime/http/testdata/cors_parity.json, which this package's
// tests write with -update, hold both to one decision.
package cors

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// AllowedHeaders are the request headers a site's browser may send an
// API: what the generated SDKs send, the end user's token in
// Authorization among them. A service credential never travels from a
// browser, so Service-Authorization is not one.
var AllowedHeaders = []string{"Accept", "Authorization", "Content-Type"}

// MaxAge is how long, in seconds, a browser may keep a preflight's answer.
const MaxAge = 600

// Vary is the Vary header of an answer to a request with an Origin, and
// PreflightVary of a preflight's: the answer differs by them.
const (
	Vary          = "Origin"
	PreflightVary = "Origin, Access-Control-Request-Method, Access-Control-Request-Headers"
)

// Policy is one API's CORS: the origins of the sites that call it, as its
// CORS field lists them, and the methods its operations use.
type Policy struct {
	// Origins are the origins the API answers, `<scheme>://<host>[:<port>]`
	// each, compared exactly. None answers no CORS at all.
	Origins []string

	// Methods are the HTTP methods of the API's operations, in upper case,
	// which a preflight's answer allows.
	Methods []string
}

// Decision is what a request's CORS check decides: whether it is a
// preflight, whether the server answers it itself, and the headers the
// answer carries.
type Decision struct {
	// Preflight is true for an OPTIONS request with an Origin and an
	// Access-Control-Request-Method.
	Preflight bool

	// Answer is true for a preflight from an origin the policy lists,
	// which the server answers itself with 204 and the headers alone. Any
	// other request reaches the API with the headers, a preflight from an
	// origin the policy does not list included, so a CORS check of the
	// API's own, the identity runtime's for its trusted origins, answers
	// it.
	Answer bool

	// Headers are the headers to set on the answer, by canonical name.
	// Empty for a request with no Origin.
	Headers map[string]string
}

// IsPreflight reports whether a request with method and header is a CORS
// preflight.
func IsPreflight(method string, header http.Header) bool {
	return method == http.MethodOptions && header.Get("Origin") != "" && header.Get("Access-Control-Request-Method") != ""
}

// Decide decides a request's CORS under p. A request with no Origin gets
// no header. One with an Origin p lists gets Access-Control-Allow-Origin
// with that origin and Access-Control-Allow-Credentials; its preflight is
// the server's to answer, and gets the methods, the headers and the max
// age too. One with an Origin p does not list gets Vary alone and is not
// the server's to answer, a preflight included: the API answers it, and
// unless a CORS check of its own allows the origin, the browser refuses
// the answer.
func (p Policy) Decide(method string, header http.Header) Decision {
	origin := header.Get("Origin")
	d := Decision{Preflight: IsPreflight(method, header), Headers: map[string]string{}}
	if origin == "" {
		return d
	}
	d.Headers["Vary"] = Vary
	if d.Preflight {
		d.Headers["Vary"] = PreflightVary
	}
	if !slices.Contains(p.Origins, origin) {
		return d
	}
	d.Headers["Access-Control-Allow-Origin"] = origin
	d.Headers["Access-Control-Allow-Credentials"] = "true"
	if d.Preflight {
		d.Answer = true
		d.Headers["Access-Control-Allow-Methods"] = strings.Join(p.Methods, ", ")
		d.Headers["Access-Control-Allow-Headers"] = strings.Join(AllowedHeaders, ", ")
		d.Headers["Access-Control-Max-Age"] = strconv.Itoa(MaxAge)
	}
	return d
}

// API is one API a server serves, with its policy: Match reports whether
// its routes take a method on a path, and a nil Match takes every request.
type API struct {
	Policy Policy
	Match  func(method, path string) bool
}

// Handler answers CORS for the APIs a server serves around next. A request
// with an Origin is the first API's whose Match takes it, by the method a
// preflight asks for in place of OPTIONS: a preflight from an origin its
// policy lists is answered 204 with the policy's headers, and any other
// request it takes reaches next with them, a preflight from an origin the
// policy does not list included, which the API's router answers, through
// the identity runtime's CORS for a trusted origin. A request no API takes
// reaches next as it is.
func Handler(next http.Handler, apis []API) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "" {
			next.ServeHTTP(w, r)
			return
		}
		method := r.Method
		if IsPreflight(r.Method, r.Header) {
			method = r.Header.Get("Access-Control-Request-Method")
		}
		path := r.URL.RawPath
		if path == "" {
			path = r.URL.Path
		}
		for _, api := range apis {
			if api.Match != nil && !api.Match(method, path) {
				continue
			}
			d := api.Policy.Decide(r.Method, r.Header)
			for name, value := range d.Headers {
				w.Header().Set(name, value)
			}
			if d.Answer {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			break
		}
		next.ServeHTTP(w, r)
	})
}
