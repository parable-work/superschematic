package notify

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// parityPath is the notifier's vectors, which the TypeScript runtime's
// notify-parity.test.ts writes (NOTIFY_PARITY_WRITE=1) and the Rust
// runtime's orm::notify reads too.
const parityPath = "../../testdata/notify_parity.json"

type parityVectors struct {
	Addresses []struct {
		Kind, Address, Want string
	} `json:"addresses"`
	Replies []struct {
		Text string `json:"text"`
		Want struct {
			Verb      string   `json:"verb"`
			Reference *int64   `json:"reference"`
			Args      []string `json:"args"`
			Ack       bool     `json:"ack"`
			Undo      bool     `json:"undo"`
			Code      bool     `json:"code"`
		} `json:"want"`
	} `json:"replies"`
	QuietHours []struct {
		Wall  string  `json:"wall"`
		Start *string `json:"start"`
		End   *string `json:"end"`
		Want  int     `json:"want"`
	} `json:"quietHours"`
	Trust []struct {
		Held     []string `json:"held"`
		Cap      []string `json:"cap"`
		Need     string   `json:"need"`
		Allows   bool     `json:"allows"`
		Narrowed []string `json:"narrowed"`
	} `json:"trust"`
	CodeHashes []struct{ Code, Want string } `json:"codeHashes"`
	SameIDs    []struct{ ID, Want string }   `json:"sameIds"`
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// TestNotifyParityVectors: the Go notifier's pure parts answer the vectors
// the TypeScript runtime wrote, as the Rust runtime's do (D190, D191).
func TestNotifyParityVectors(t *testing.T) {
	data, err := os.ReadFile(parityPath)
	if err != nil {
		t.Fatal(err)
	}
	var vectors parityVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Addresses) == 0 || len(vectors.Replies) == 0 || len(vectors.QuietHours) == 0 || len(vectors.Trust) == 0 || len(vectors.CodeHashes) == 0 || len(vectors.SameIDs) == 0 {
		t.Fatal("the vectors are missing a section")
	}
	for _, c := range vectors.Addresses {
		if got := NormaliseAddress(c.Kind, c.Address); got != c.Want {
			t.Errorf("address %s %q: %q, want %q", c.Kind, c.Address, got, c.Want)
		}
	}
	for _, c := range vectors.Replies {
		read := ReadReplyText(c.Text)
		var reference *int64
		if read.HasReference {
			reference = &read.Reference
		}
		if read.Verb != c.Want.Verb || (reference == nil) != (c.Want.Reference == nil) || (reference != nil && *reference != *c.Want.Reference) || !slices.Equal(read.Args, c.Want.Args) || read.Acknowledges() != c.Want.Ack || read.Undoes() != c.Want.Undo || LooksLikeCode(c.Text) != c.Want.Code {
			t.Errorf("reply %q: %+v (code %v), want %+v", c.Text, read, LooksLikeCode(c.Text), c.Want)
		}
	}
	for _, c := range vectors.QuietHours {
		parts := strings.Split(c.Wall, ":")
		hour, _ := strconv.Atoi(parts[0])
		minute, _ := strconv.Atoi(parts[1])
		second, _ := strconv.Atoi(parts[2])
		if got := QuietDelaySeconds(hour*60+minute, second, text(c.Start), text(c.End)); got != c.Want {
			t.Errorf("quiet hours at %s in [%s, %s): %d, want %d", c.Wall, text(c.Start), text(c.End), got, c.Want)
		}
	}
	for _, c := range vectors.Trust {
		if got := ReplyAllows(c.Held, c.Cap, c.Need); got != c.Allows {
			t.Errorf("trust %v within %v of %s: %v, want %v", c.Held, c.Cap, c.Need, got, c.Allows)
		}
		if got := NarrowedPermissions(c.Held, c.Cap); !slices.Equal(got, c.Narrowed) {
			t.Errorf("trust %v narrowed to %v: %v, want %v", c.Held, c.Cap, got, c.Narrowed)
		}
	}
	for _, c := range vectors.CodeHashes {
		if got := CodeHash(c.Code); got != c.Want {
			t.Errorf("code %q: %s, want %s", c.Code, got, c.Want)
		}
	}
	for _, c := range vectors.SameIDs {
		if got := SameID(c.ID); got != c.Want {
			t.Errorf("id %q: %s, want %s", c.ID, got, c.Want)
		}
	}
}
