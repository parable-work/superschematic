// Command fakeserver honours the contract of a generated server
// entrypoint (docs/stack-model.md, section 8.1) for the local target's
// integration test: it reads its config from the environment, listens on
// $PORT, answers /healthz and /readyz, and echoes its environment at /env.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
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
	fmt.Printf("fake server listening on 127.0.0.1:%s\n", port)
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
