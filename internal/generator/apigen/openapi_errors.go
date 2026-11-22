package apigen

// Every error a generated server answers is an RFC 9457 problem: the Go,
// TypeScript and Rust HTTP runtimes write it with the content type
// application/problem+json and the body ProblemDetails describes
// (runtime/http/go/response, runtime/http/typescript/src/problem.ts,
// runtime/http/rust/src/response.rs). The OpenAPI document says the same
// for each error response it declares: the 400 and 500 of every
// operation, the user model's errors (D50) and the behaviors' refusals
// (D103).

const (
	// problemDetailsSchemaName is the component that describes a problem.
	problemDetailsSchemaName = "ProblemDetails"
	// problemDetailsMediaType is the content type of every error response.
	problemDetailsMediaType = "application/problem+json"
)

// problemResponse is an error response with description, whose body is a
// problem.
func problemResponse(description string) map[string]interface{} {
	return map[string]interface{}{
		"description": description,
		"content": map[string]interface{}{
			problemDetailsMediaType: map[string]interface{}{
				"schema": map[string]interface{}{
					"$ref": "#/components/schemas/" + problemDetailsSchemaName,
				},
			},
		},
	}
}

// problemDetailsSchema is the body the HTTP runtimes write for every
// error: type, title, status and detail always, and code, requestId,
// details and errors when the problem has them.
func problemDetailsSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":        "object",
		"description": "An error response in the Problem Details format (RFC 9457).",
		"required":    []string{"type", "title", "status", "detail"},
		"properties": map[string]interface{}{
			"type": map[string]interface{}{
				"type":        "string",
				"description": "The problem type. It is `about:blank`: the status and `code` identify the problem.",
				"example":     "about:blank",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "A short summary, usually the text of the HTTP status.",
				"example":     "Bad Request",
			},
			"status": map[string]interface{}{
				"type":        "integer",
				"description": "The HTTP status code.",
				"example":     400,
			},
			"detail": map[string]interface{}{
				"type":        "string",
				"description": "What went wrong with this request.",
			},
			"code": map[string]interface{}{
				"type":        "string",
				"description": "The stable error code, when the problem has one.",
			},
			"requestId": map[string]interface{}{
				"type":        "string",
				"description": "The ID of the request, when the problem has one.",
			},
			"details": map[string]interface{}{
				"description": "Structured facts about the refusal, such as the entity or the path it concerns. Only some refusals have it.",
			},
			"errors": map[string]interface{}{
				"description": "The fields or filters that failed validation. Only a validation or filter error has it.",
			},
		},
	}
}
