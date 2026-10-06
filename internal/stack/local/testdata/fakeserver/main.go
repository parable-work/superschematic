// Command fakeserver honours the contract of a generated server
// entrypoint (docs/stack-model.md, section 8.1) for the local target's
// integration test: it reads its config from the environment, listens on
// $PORT, answers /healthz and /readyz, and echoes its environment at /env.
//
// It runs the service auth of D37 as the entrypoint will, with the HTTP
// runtime's serviceauth package: given SERVICE_AUTH, it verifies the
// Service-Authorization credential at /whoami and answers the caller; and
// /call/{field} calls /whoami on the API whose derived field is field,
// with a token signed as the field's credential variables say.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
)

func main() {
	port := os.Getenv("PORT")
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)
	mux.HandleFunc("GET /env", func(w http.ResponseWriter, _ *http.Request) {
		env := map[string]string{}
		for _, kv := range os.Environ() {
			key, value, _ := strings.Cut(kv, "=")
			env[key] = value
		}
		writeJSON(w, env)
	})
	if raw := os.Getenv("SERVICE_AUTH"); raw != "" {
		var cfg serviceauth.Config
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			fail("SERVICE_AUTH: %v", err)
		}
		verifier, err := serviceauth.New(cfg)
		if err != nil {
			fail("SERVICE_AUTH: %v", err)
		}
		mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
			caller, err := verifier.Authenticate(r)
			if err != nil || caller == nil {
				http.Error(w, fmt.Sprintf("no caller: %v", err), http.StatusUnauthorized)
				return
			}
			writeJSON(w, caller)
		})
	}
	mux.HandleFunc("GET /call/{field}", func(w http.ResponseWriter, r *http.Request) {
		field := r.PathValue("field")
		issuer := os.Getenv(field + "_CREDENTIAL_ISSUER")
		source, err := serviceauth.SignedToken([]byte(os.Getenv(field+"_CREDENTIAL_KEY")), issuer, issuer, os.Getenv(field+"_CREDENTIAL_AUDIENCE"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		token, err := source(r.Context(), false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, os.Getenv(field+"_URL")+"/whoami", nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Service-Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
	fmt.Printf("fake server listening on 127.0.0.1:%s\n", port)
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		fail("%v", err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
