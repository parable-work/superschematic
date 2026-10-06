// Command fakeserver honours the contract of a generated server
// entrypoint (docs/stack-model.md, section 8.1) for the local target's
// integration test: it reads its config from the environment, listens on
// $PORT, answers /healthz and /readyz, and echoes its environment at /env.
//
// /token/{field} answers the service credential a call to the API whose
// derived field is field carries (D37): a token signed with the HTTP
// runtime's serviceauth.SignedToken, as the field's credential variables
// say, as the generated entrypoint's client signs it.
package main

import (
	"encoding/json"
	"fmt"
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(env)
	})
	mux.HandleFunc("GET /token/{field}", func(w http.ResponseWriter, r *http.Request) {
		field := r.PathValue("field")
		if source := os.Getenv(field + "_CREDENTIAL_SOURCE"); source != "signed-token" {
			http.Error(w, field+" has credential source "+source, http.StatusNotFound)
			return
		}
		issuer := os.Getenv(field + "_CREDENTIAL_ISSUER")
		sign, err := serviceauth.SignedToken([]byte(os.Getenv(field+"_CREDENTIAL_KEY")), issuer, issuer, os.Getenv(field+"_CREDENTIAL_AUDIENCE"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		token, err := sign(r.Context(), false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, token)
	})
	fmt.Printf("fake server listening on 127.0.0.1:%s\n", port)
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
