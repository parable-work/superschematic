package access

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite runtime/http/testdata/access_parity.json")

// parityPath is the access vectors the Go and TypeScript runtimes both run
// (D180).
var parityPath = filepath.Join("..", "..", "..", "http", "testdata", "access_parity.json")

type parityPrincipal struct {
	Subject   string `json:"subject,omitempty"`
	Kind      string `json:"kind,omitempty"`
	ActingFor *struct {
		Kind    string `json:"kind,omitempty"`
		Subject string `json:"subject"`
	} `json:"actingFor,omitempty"`
	Credential *struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"credential,omitempty"`
}

func (p parityPrincipal) principal() Principal {
	out := Principal{Subject: p.Subject, Kind: Kind(p.Kind)}
	if p.ActingFor != nil {
		out.ActingFor = &ActingFor{Kind: Kind(p.ActingFor.Kind), Subject: p.ActingFor.Subject}
	}
	if p.Credential != nil {
		out.Credential = &Credential{Kind: p.Credential.Kind, ID: p.Credential.ID}
	}
	return out
}

type parityEntry struct {
	Kind          string `json:"kind"`
	Principal     string `json:"principal,omitempty"`
	PrincipalKind string `json:"principalKind,omitempty"`
	ActingFor     string `json:"actingFor,omitempty"`
	Credential    string `json:"credential,omitempty"`
	Operation     string `json:"operation,omitempty"`
}

type parityFile struct {
	Comment     string `json:"comment"`
	TokenHashes []struct {
		Token string `json:"token"`
		Want  string `json:"want"`
	} `json:"tokenHashes"`
	Reaches []struct {
		Name    string              `json:"name"`
		Entries []string            `json:"entries"`
		Want    map[string][]string `json:"want"`
	} `json:"reaches"`
	CallEntries []struct {
		Name      string          `json:"name"`
		Principal parityPrincipal `json:"principal"`
		Operation string          `json:"operation"`
		Method    string          `json:"method"`
		Want      *parityEntry    `json:"want"`
	} `json:"callEntries"`
}

func entryOf(e LogEntry, ok bool) *parityEntry {
	if !ok {
		return nil
	}
	return &parityEntry{Kind: e.Kind, Principal: e.Principal, PrincipalKind: e.PrincipalKind, ActingFor: e.ActingFor, Credential: e.Credential, Operation: e.Operation}
}

// TestAccessParityVectors runs access_parity.json, which the TypeScript
// runtime's access-parity.test.ts runs too: a grant token's hash, a role's
// reaches read by type, and the access-log entry of a call. With -update
// it rewrites the wants from this runtime's answers.
func TestAccessParityVectors(t *testing.T) {
	data, err := os.ReadFile(parityPath)
	if err != nil {
		t.Fatal(err)
	}
	var file parityFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	for i, tc := range file.TokenHashes {
		got := GrantTokenHash(tc.Token)
		if *update {
			file.TokenHashes[i].Want = got
		} else if got != tc.Want {
			t.Errorf("GrantTokenHash(%q) = %s, want %s", tc.Token, got, tc.Want)
		}
	}
	for i, tc := range file.Reaches {
		got := ReachesOf(tc.Entries)
		if *update {
			file.Reaches[i].Want = got
		} else if !reflect.DeepEqual(got, tc.Want) && !(len(got) == 0 && len(tc.Want) == 0) {
			t.Errorf("%s: ReachesOf(%q) = %v, want %v", tc.Name, tc.Entries, got, tc.Want)
		}
	}
	for i, tc := range file.CallEntries {
		got := entryOf(CallEntry(tc.Principal.principal(), tc.Operation, tc.Method))
		if *update {
			file.CallEntries[i].Want = got
		} else if !reflect.DeepEqual(got, tc.Want) {
			t.Errorf("%s: CallEntry = %+v, want %+v", tc.Name, got, tc.Want)
		}
	}
	if *update {
		var out bytes.Buffer
		encoder := json.NewEncoder(&out)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(file); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(parityPath, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNewGrantTokenIsPrefixedBase64URLOf32Bytes(t *testing.T) {
	token, err := NewGrantToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, GrantTokenPrefix) || len(token) != len(GrantTokenPrefix)+43 || strings.ContainsAny(token, "+/=") {
		t.Fatalf("token %q is not grant_ and 43 characters of base64url", token)
	}
	other, _ := NewGrantToken()
	if other == token {
		t.Fatal("two tokens are the same")
	}
}

func TestGrantEndedByDate(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, tc := range []struct {
		name  string
		grant Grant
		want  string
	}{
		{"active", Grant{StartsAt: past}, ""},
		{"active until later", Grant{StartsAt: past, EndsAt: &future}, ""},
		{"revoked", Grant{StartsAt: past, RevokedAt: &past}, GrantRevoked},
		{"not started", Grant{StartsAt: future}, GrantNotStarted},
		{"expired", Grant{StartsAt: past, EndsAt: &past}, GrantExpired},
		{"ends now", Grant{StartsAt: past, EndsAt: &now}, GrantExpired},
	} {
		if got := tc.grant.EndedByDate(now); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
