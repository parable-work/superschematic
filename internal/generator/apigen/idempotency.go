package apigen

import (
	"fmt"
	"strconv"

	ir "github.com/parable-work/superschematic/ir"
)

// HasIdempotent reports whether a route the router mounts is @idempotent
// (D140): Config then takes an IdempotencyStore.
func (o *APIOutput) HasIdempotent() bool {
	for _, endpoint := range o.Endpoints {
		if endpoint.Idempotent != nil {
			return true
		}
	}
	return false
}

// checkIdempotency refuses an @idempotent operation that uploads files,
// and an API with an @idempotent operation and no table for its keys: the
// loader's check found the table, so the build was handed no database to
// read it from.
func (o *APIOutput) checkIdempotency() error {
	for _, endpoint := range o.Endpoints {
		// A multipart body's boundary differs between two sends of the
		// same files, so a retry would never hash as the first request.
		if endpoint.Idempotent != nil && endpoint.HasFileUpload {
			return fmt.Errorf("apigen: operation %s.%s is @idempotent and uploads files: a multipart body is written with a new boundary each time, so a retry is never the same request; upload the file to a bucket through a signed URL and send its name (D140)", endpoint.Namespace, endpoint.Name)
		}
	}
	if o.HasIdempotent() && o.IdempotencyTable == nil {
		return fmt.Errorf("apigen: %s has @idempotent operations, and the build read no table for their keys from its database (D140)", o.SchemaName)
	}
	return nil
}

// IdempotencyOperation names the endpoint to the store: its namespace and
// its name, as the TypeScript router names it.
func (e EndpointInfo) IdempotencyOperation() string {
	return e.Namespace + "." + e.Name
}

// IdempotencyTTLSeconds is how long the endpoint's keys are kept.
func (e EndpointInfo) IdempotencyTTLSeconds() int {
	return e.Idempotent.TTL()
}

// IdempotencyTableLiteral is the names of the API's idempotency table as
// the Go HTTP runtime's idempotency.Table.
func (o *APIOutput) IdempotencyTableLiteral() string {
	t := o.IdempotencyTable
	if t == nil {
		t = &ir.IdempotencyTable{}
	}
	q := strconv.Quote
	return "runtimeidempotency.Table{Table: " + q(t.Table) + ", Columns: runtimeidempotency.Columns{" +
		"Lookup: " + q(t.Lookup) + ", Operation: " + q(t.Operation) + ", Principal: " + q(t.Principal) + ", Key: " + q(t.Key) +
		", RequestHash: " + q(t.RequestHash) + ", State: " + q(t.State) + ", Status: " + q(t.Status) + ", Body: " + q(t.Body) +
		", StartedAt: " + q(t.StartedAt) + ", ExpiresAt: " + q(t.ExpiresAt) + "}}"
}

// IdempotencyReplayHeader is the response header that marks an answer a
// repeat was given from the first request's, and its value.
const IdempotencyReplayHeader = "Idempotent-Replay"

// idempotencyOpenAPIParameter is the Idempotency-Key header of an
// @idempotent operation (D140).
func idempotencyOpenAPIParameter(endpoint EndpointInfo) map[string]interface{} {
	description := fmt.Sprintf("The key of this request, which the caller makes (a UUID, say). A repeat under the same key, by the same caller, within %d seconds, runs nothing and is answered what the first request was, with `%s: true`. The same key with another method, path, query or body is refused 422, and a repeat while the first request is still running 409 with Retry-After.", endpoint.IdempotencyTTLSeconds(), IdempotencyReplayHeader)
	if !endpoint.Idempotent.Required {
		description += " Without the header the operation runs each time."
	}
	return map[string]interface{}{
		"name":        ir.IdempotencyKeyHeader,
		"in":          "header",
		"required":    endpoint.Idempotent.Required,
		"description": description,
		"schema": map[string]interface{}{
			"type":      "string",
			"minLength": 1,
			"maxLength": 255,
			"pattern":   "^[\\x20-\\x7E]+$",
			"example":   "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40",
		},
	}
}

// addIdempotencyResponses declares, on an @idempotent operation's
// responses, the refusals its key meets and the header of a repeat's
// answer (D140). A status the operation already declares keeps its
// description, with the key's refusal after it.
func addIdempotencyResponses(responses map[string]interface{}) {
	refusals := []struct{ status, description string }{
		{"409", "The first request under this Idempotency-Key is still running (code `idempotency_in_flight`): send it again after Retry-After seconds."},
		{"422", "This Idempotency-Key was used for another request, one of another method, path, query or body (code `idempotency_key_reused`)."},
	}
	for _, refusal := range refusals {
		if declared, ok := responses[refusal.status].(map[string]interface{}); ok {
			if text, _ := declared["description"].(string); text != "" {
				declared["description"] = text + " Or: " + refusal.description
			}
			continue
		}
		response := problemResponse(refusal.description)
		if refusal.status == "409" {
			response["headers"] = map[string]interface{}{
				"Retry-After": map[string]interface{}{
					"description": "Seconds until the first request has run for as long as it may",
					"schema":      map[string]interface{}{"type": "integer", "minimum": 1},
				},
			}
		}
		responses[refusal.status] = response
	}
	if success, ok := responses["200"].(map[string]interface{}); ok {
		headers, _ := success["headers"].(map[string]interface{})
		if headers == nil {
			headers = map[string]interface{}{}
			success["headers"] = headers
		}
		headers[IdempotencyReplayHeader] = map[string]interface{}{
			"description": "true when this is the answer of an earlier request under the same Idempotency-Key, which ran the operation; absent when this request ran it",
			"schema":      map[string]interface{}{"type": "string", "enum": []string{"true"}},
		}
	}
}
