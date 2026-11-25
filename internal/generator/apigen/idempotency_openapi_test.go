package apigen

import "testing"

// TestIdempotencyResponsesMarkEverySuccess: Idempotent-Replay is declared
// on each success an @idempotent operation declares, 201 and 204 as much
// as 200, and on no refusal (D140).
func TestIdempotencyResponsesMarkEverySuccess(t *testing.T) {
	responses := map[string]interface{}{
		"200": map[string]interface{}{"description": "Successful response"},
		"201": map[string]interface{}{"description": "Created"},
		"204": map[string]interface{}{"description": "No content", "headers": map[string]interface{}{"ETag": map[string]interface{}{}}},
		"400": problemResponse("The request is not valid."),
	}
	addIdempotencyResponses(responses)
	for status, want := range map[string]bool{"200": true, "201": true, "204": true, "400": false, "409": false, "422": false} {
		response, ok := responses[status].(map[string]interface{})
		if !ok {
			t.Fatalf("no %s response", status)
		}
		headers, _ := response["headers"].(map[string]interface{})
		if _, marked := headers[IdempotencyReplayHeader]; marked != want {
			t.Errorf("%s declares %s: %t, want %t", status, IdempotencyReplayHeader, marked, want)
		}
	}
	if _, kept := responses["204"].(map[string]interface{})["headers"].(map[string]interface{})["ETag"]; !kept {
		t.Error("the 204's own headers were dropped")
	}
}
