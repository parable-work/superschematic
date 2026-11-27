package agents

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/inbound"
	"github.com/parable-work/superschematic/runtime/http/go/providers"
)

// parityPath is the orchestration's vectors, which the TypeScript
// runtime's agents-parity.test.ts writes (AGENTS_PARITY_WRITE=1) and the
// Rust runtime's orm::agents reads too.
const parityPath = "../../testdata/agents_parity.json"

type parityVectors struct {
	DefaultAutonomy []struct {
		Class string `json:"class"`
		Want  string `json:"want"`
	} `json:"defaultAutonomy"`
	TrustOf []struct {
		Recorded any    `json:"recorded"`
		Want     string `json:"want"`
	} `json:"trustOf"`
	EffectiveAutonomy []struct {
		Mode  string  `json:"mode"`
		Class string  `json:"class"`
		Trust *string `json:"trust"`
		Want  string  `json:"want"`
	} `json:"effectiveAutonomy"`
	NarrowReaches []struct {
		Role  map[string][]string   `json:"role"`
		Party []map[string][]string `json:"party"`
		Want  map[string][]string   `json:"want"`
	} `json:"narrowReaches"`
	NarrowPermissions []struct {
		Agent []string `json:"agent"`
		Actor []string `json:"actor"`
		Want  []string `json:"want"`
	} `json:"narrowPermissions"`
	ParamsHash []struct {
		Operation string          `json:"operation"`
		Params    json.RawMessage `json:"params"`
		Want      string          `json:"want"`
	} `json:"paramsHash"`
	BestPolicy []struct {
		Rows []struct {
			Agent          *string `json:"agent"`
			Operation      *string `json:"operation"`
			OperationClass *string `json:"operationClass"`
			Mode           string  `json:"mode"`
		} `json:"rows"`
		Operation string `json:"operation"`
		Class     string `json:"class"`
		Want      *int   `json:"want"`
	} `json:"bestPolicy"`
	BudgetCrossing []struct {
		Before int64  `json:"before"`
		After  int64  `json:"after"`
		Limit  *int64 `json:"limit"`
		WarnAt int64  `json:"warnAt"`
		Warns  bool   `json:"warns"`
		Capped bool   `json:"capped"`
	} `json:"budgetCrossing"`
	RedactText []struct {
		Text    string   `json:"text"`
		Secrets []string `json:"secrets"`
		Want    string   `json:"want"`
	} `json:"redactText"`
	EmailAuthenticated []struct {
		Authentication *providers.InboundAuthentication `json:"authentication"`
		Want           bool                             `json:"want"`
	} `json:"emailAuthenticated"`
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// TestAgentsParityVectors holds the Go orchestration's pure parts to the
// vectors TypeScript writes (D158, D183, D192): DefaultAutonomy, TrustOf,
// EffectiveAutonomy, NarrowReaches, NarrowPermissions, ParamsHash,
// BestPolicy, BudgetCrossing, RedactTokens, and inbound's
// EmailAuthenticated.
func TestAgentsParityVectors(t *testing.T) {
	data, err := os.ReadFile(parityPath)
	if err != nil {
		t.Fatal(err)
	}
	var v parityVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.DefaultAutonomy) == 0 || len(v.RedactText) == 0 || len(v.EmailAuthenticated) == 0 {
		t.Fatal("the parity file holds no vectors")
	}
	for _, c := range v.DefaultAutonomy {
		if got := DefaultAutonomy(c.Class); got != c.Want {
			t.Errorf("DefaultAutonomy(%s) = %s, want %s", c.Class, got, c.Want)
		}
	}
	for _, c := range v.TrustOf {
		if got := TrustOf(c.Recorded); got != c.Want {
			t.Errorf("TrustOf(%v) = %s, want %s", c.Recorded, got, c.Want)
		}
	}
	for _, c := range v.EffectiveAutonomy {
		if got := EffectiveAutonomy(c.Mode, c.Class, text(c.Trust)); got != c.Want {
			t.Errorf("EffectiveAutonomy(%s, %s, %v) = %s, want %s", c.Mode, c.Class, c.Trust, got, c.Want)
		}
	}
	for _, c := range v.NarrowReaches {
		got := NarrowReaches(c.Role, c.Party)
		if !reflect.DeepEqual(got, c.Want) {
			t.Errorf("NarrowReaches(%v, %v) = %v, want %v", c.Role, c.Party, got, c.Want)
		}
	}
	for _, c := range v.NarrowPermissions {
		if got := NarrowPermissions(c.Agent, c.Actor); !slices.Equal(got, c.Want) {
			t.Errorf("NarrowPermissions(%v, %v) = %v, want %v", c.Agent, c.Actor, got, c.Want)
		}
	}
	for _, c := range v.ParamsHash {
		var params any
		if err := json.Unmarshal(c.Params, &params); err != nil {
			t.Fatal(err)
		}
		if got := ParamsHash(c.Operation, params); got != c.Want {
			t.Errorf("ParamsHash(%s, %s) = %s, want %s", c.Operation, c.Params, got, c.Want)
		}
	}
	for _, c := range v.BestPolicy {
		rows := make([]PolicyRow, len(c.Rows))
		for i, r := range c.Rows {
			rows[i] = PolicyRow{Agent: text(r.Agent), Operation: text(r.Operation), OperationClass: text(r.OperationClass), Mode: r.Mode}
		}
		want := -1
		if c.Want != nil {
			want = *c.Want
		}
		if got := BestPolicy(rows, c.Operation, c.Class); got != want {
			t.Errorf("BestPolicy(%+v, %s, %s) = %d, want %d", rows, c.Operation, c.Class, got, want)
		}
	}
	for _, c := range v.BudgetCrossing {
		warns, capped := BudgetCrossing(c.Before, c.After, c.Limit, c.WarnAt)
		if warns != c.Warns || capped != c.Capped {
			t.Errorf("BudgetCrossing(%d, %d, %v, %d) = %v %v, want %v %v", c.Before, c.After, c.Limit, c.WarnAt, warns, capped, c.Warns, c.Capped)
		}
	}
	for _, c := range v.RedactText {
		if got := RedactTokens(c.Text, c.Secrets, nil); got != c.Want {
			t.Errorf("RedactTokens(%q, %v) = %q, want %q", c.Text, c.Secrets, got, c.Want)
		}
	}
	for _, c := range v.EmailAuthenticated {
		if got := inbound.EmailAuthenticated(c.Authentication); got != c.Want {
			t.Errorf("EmailAuthenticated(%+v) = %v, want %v", c.Authentication, got, c.Want)
		}
	}
}
