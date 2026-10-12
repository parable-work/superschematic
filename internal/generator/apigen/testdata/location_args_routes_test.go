// This file runs inside the generated API module of location-args-api
// (TestLocationArgsRoutesCheckTheirJSON copies it there). It registers the
// generated routes with an implementation that records what each call
// received, and drives pinPlace over httptest with Geo.Location body
// arguments alone, in a list, a list of lists, a map and a map of lists.
// The route checks each value on its own JSON with superscalar before it
// decodes it, as the generated types do, so what encoding/json drops,
// overwrites or zero-fills is refused under the core's kind.
package locationargsapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/location-args-api"
	types "example.com/schemas/types/go/location-args-api"
)

// places records the arguments of the last call; calls counts every call,
// so a test can check a refused request never arrived.
type places struct {
	calls int
	at    types.GeoLocation
	near  types.GeoLocation
	route []types.GeoLocation
	grid  [][]types.GeoLocation
	named map[string]types.GeoLocation
	legs  map[string][]types.GeoLocation
}

func (s *places) PinPlace(_ context.Context, at types.GeoLocation, near types.GeoLocation, route []types.GeoLocation, grid [][]types.GeoLocation, named map[string]types.GeoLocation, legs map[string][]types.GeoLocation) (*bool, error) {
	s.calls++
	s.at, s.near, s.route, s.grid, s.named, s.legs = at, near, route, grid, named, legs
	pinned := true
	return &pinned, nil
}

const pinPlacePath = "/api/places"

func serve(t *testing.T) (*httptest.Server, *places) {
	t.Helper()
	impl := &places{}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Place: impl},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, impl
}

// pin posts body to pinPlace and returns the status and the decoded
// response object.
func pin(t *testing.T, server *httptest.Server, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(server.URL+pinPlacePath, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s: response is not a JSON object: %s", body, raw)
	}
	return resp.StatusCode, decoded
}

// accepted posts body and checks the implementation was called.
func accepted(t *testing.T, server *httptest.Server, impl *places, body string) {
	t.Helper()
	before := impl.calls
	if status, response := pin(t, server, body); status != http.StatusOK {
		t.Fatalf("%s: status = %d, want 200; response %v", body, status, response)
	}
	if impl.calls != before+1 {
		t.Fatalf("%s: the implementation was not called", body)
	}
}

// refusedWith posts body and checks the route answered 400 with exactly
// the expected errors, path -> "validator: message", without calling the
// implementation.
func refusedWith(t *testing.T, server *httptest.Server, impl *places, body string, want map[string]string) {
	t.Helper()
	before := impl.calls
	status, response := pin(t, server, body)
	if status != http.StatusBadRequest {
		t.Fatalf("%s: status = %d, want 400; response %v", body, status, response)
	}
	if impl.calls != before {
		t.Fatalf("%s: a refused request reached the implementation", body)
	}
	errs, ok := response["errors"].(map[string]any)
	if !ok {
		t.Fatalf("%s: response has no errors object: %v", body, response)
	}
	got := map[string]string{}
	for path, value := range errs {
		entries, _ := value.([]any)
		for _, entry := range entries {
			e, _ := entry.(map[string]any)
			validator, _ := e["validator"].(string)
			message, _ := e["message"].(string)
			if got[path] != "" {
				got[path] += "; "
			}
			got[path] += validator + ": " + message
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: errors = %v, want %v", body, got, want)
	}
}

const (
	unknownAlt = `custom: custom: unknown key "alt": a Geo.Location has only "lat" and "lon"`
	missingLon = `custom: custom: missing key "lon"`
	missingLat = `custom: custom: missing key "lat"`
	latTooFar  = `range: range: "lat" must be from -90 to 90, got 91`
	lonTooFar  = `range: range: "lon" must be from -180 to 180, got -180.5`
)

// TestALocationIsAPointAndZeroIsOne: a location in each shape reaches the
// implementation as the point it holds, keys in any order, and
// {"lat": 0, "lon": 0} is a point, not a missing value.
func TestALocationIsAPointAndZeroIsOne(t *testing.T) {
	server, impl := serve(t)
	accepted(t, server, impl, `{
		"at": {"lat": 0, "lon": 0},
		"near": {"lon": -122.4194, "lat": 37.7749},
		"route": [{"lat": 0, "lon": 0}, {"lat": 90, "lon": -180}],
		"grid": [[{"lat": -90, "lon": 180}], []],
		"named": {"origin": {"lat": 0, "lon": 0}},
		"legs": {"out": [{"lat": 1, "lon": 2}], "back": []}
	}`)
	if impl.at != (types.GeoLocation{}) {
		t.Errorf("at = %+v, want the point {0, 0}", impl.at)
	}
	if want := (types.GeoLocation{Lat: 37.7749, Lon: -122.4194}); impl.near != want {
		t.Errorf("near = %+v, want %+v", impl.near, want)
	}
	if want := []types.GeoLocation{{}, {Lat: 90, Lon: -180}}; !reflect.DeepEqual(impl.route, want) {
		t.Errorf("route = %+v, want %+v", impl.route, want)
	}
	if want := [][]types.GeoLocation{{{Lat: -90, Lon: 180}}, {}}; !reflect.DeepEqual(impl.grid, want) {
		t.Errorf("grid = %+v, want %+v", impl.grid, want)
	}
	if want := map[string]types.GeoLocation{"origin": {}}; !reflect.DeepEqual(impl.named, want) {
		t.Errorf("named = %+v, want %+v", impl.named, want)
	}
	if want := map[string][]types.GeoLocation{"out": {{Lat: 1, Lon: 2}}, "back": {}}; !reflect.DeepEqual(impl.legs, want) {
		t.Errorf("legs = %+v, want %+v", impl.legs, want)
	}

	// An optional location left out is the zero value, as in Go it must
	// be; only a required one is checked for presence.
	accepted(t, server, impl, `{"at": {"lat": 1, "lon": 2}}`)
	if impl.near != (types.GeoLocation{}) || impl.route != nil || impl.named != nil {
		t.Errorf("absent optional arguments = %+v, %+v, %+v, want zero values", impl.near, impl.route, impl.named)
	}
}

// TestALocationIsCheckedOnItsJSON: an unknown key, a missing key and a
// degree out of range are each refused at the value's path, alone, in a
// list, a list of lists, a map and a map of lists, under the core's kind
// and message. So are what encoding/json would read anyway: a duplicate
// key, a key in another case and a member that is not a number. A value
// that is not an object is "type", and a required one absent or null is
// "required".
func TestALocationIsCheckedOnItsJSON(t *testing.T) {
	server, impl := serve(t)
	for _, tc := range []struct {
		body string
		want map[string]string
	}{
		// A single value, required and optional.
		{`{"at": {"lat": 1, "lon": 2, "alt": 3}}`, map[string]string{"at": unknownAlt}},
		{`{"at": {"lat": 1}}`, map[string]string{"at": missingLon}},
		{`{"at": {"lat": 91, "lon": 0}}`, map[string]string{"at": latTooFar}},
		{`{"at": {"lat": 0, "lon": 0}, "near": {"lat": 0, "lon": 0, "alt": 3}}`, map[string]string{"near": unknownAlt}},
		{`{"at": {"lat": 0, "lon": 0}, "near": {"lon": 2}}`, map[string]string{"near": missingLat}},
		{`{"at": {"lat": 0, "lon": 0}, "near": {"lat": 0, "lon": -180.5}}`, map[string]string{"near": lonTooFar}},
		{`{"at": {"lat": 1, "lat": 2, "lon": 3}}`, map[string]string{"at": `custom: custom: duplicate key "lat"`}},
		{`{"at": {"LAT": 1, "lon": 2}}`, map[string]string{"at": `custom: custom: unknown key "LAT": a Geo.Location has only "lat" and "lon"`}},
		{`{"at": {"lat": "1", "lon": 2}}`, map[string]string{"at": `custom: custom: "lat" must be a JSON number, got a string`}},
		{`{"at": {"lat": 1, "lon": null}}`, map[string]string{"at": `custom: custom: "lon" must be a JSON number, got null`}},
		{`{"at": "37.7749,-122.4194"}`, map[string]string{"at": "type: expected an object"}},
		{`{"at": [37.7749, -122.4194]}`, map[string]string{"at": "type: expected an object"}},
		{`{"at": null}`, map[string]string{"at": "required: required field"}},
		{`{}`, map[string]string{"at": "required: required field"}},
		// A list and a list of lists.
		{`{"at": {"lat": 0, "lon": 0}, "route": [{"lat": 0, "lon": 0}, {"lat": 1, "lon": 2, "alt": 3}, {"lat": 1}, {"lat": 91, "lon": 0}, null, 7]}`, map[string]string{
			"route[1]": unknownAlt,
			"route[2]": missingLon,
			"route[3]": latTooFar,
			"route[4]": "required: required field",
			"route[5]": "type: expected an object",
		}},
		{`{"at": {"lat": 0, "lon": 0}, "grid": [[{"lat": 0, "lon": 0}, {"lat": 1, "lon": 2, "alt": 3}], [{"lon": 0}, {"lat": 91, "lon": 0}]]}`, map[string]string{
			"grid[0][1]": unknownAlt,
			"grid[1][0]": missingLat,
			"grid[1][1]": latTooFar,
		}},
		// A map and a map of lists.
		{`{"at": {"lat": 0, "lon": 0}, "named": {"origin": {"lat": 0, "lon": 0}, "a": {"lat": 1, "lon": 2, "alt": 3}, "b": {"lat": 1}, "c": {"lat": 0, "lon": -180.5}, "d": null}}`, map[string]string{
			"named[a]": unknownAlt,
			"named[b]": missingLon,
			"named[c]": lonTooFar,
			"named[d]": "required: required field",
		}},
		{`{"at": {"lat": 0, "lon": 0}, "legs": {"out": [{"lat": 0, "lon": 0}, {"lat": 1, "lon": 2, "alt": 3}], "back": [{"lon": 0}, {"lat": 91, "lon": 0}]}}`, map[string]string{
			"legs[out][1]":  unknownAlt,
			"legs[back][0]": missingLat,
			"legs[back][1]": latTooFar,
		}},
	} {
		refusedWith(t, server, impl, tc.body, tc.want)
	}
}
