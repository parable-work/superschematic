package cloudflare_test

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/cloudflare"
	"github.com/parable-work/superschematic/extensions/cloudflare/schemas"
)

// TestPin checks that the pin records a digest for each upstream file at
// its version, and that every pinned type has its file and every file is
// a pinned type's.
func TestPin(t *testing.T) {
	pin, err := schemas.ReadPin()
	if err != nil {
		t.Fatal(err)
	}
	if len(pin.Sources) != 2 {
		t.Errorf("%s records %d sources, want schema.json and bridge-metadata.json", schemas.PinFile, len(pin.Sources))
	}
	for _, src := range pin.Sources {
		if len(src.SHA256) != 64 || !strings.Contains(src.URL, "/pulumi-cloudflare/v"+pin.Version+"/") {
			t.Errorf("source %s: %s at %s does not pin version %s by digest", src.Name, src.SHA256, src.URL, pin.Version)
		}
	}
	if !sort.StringsAreSorted(pin.Types) {
		t.Errorf("%s lists its types out of order", schemas.PinFile)
	}
	files, err := filepath.Glob("schemas/*.json")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{schemas.PinFile}
	for _, token := range pin.Types {
		s, err := schemas.Load(token)
		if err != nil {
			t.Error(err)
			continue
		}
		if _, err := s.JSONSchema(); err != nil {
			t.Error(err)
		}
		if !strings.HasPrefix(s.Terraform.Type, "cloudflare_") {
			t.Errorf("%s has Terraform type %q", token, s.Terraform.Type)
		}
		want = append(want, schemas.FileName(token))
	}
	var got []string
	for _, f := range files {
		got = append(got, filepath.Base(f))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("schemas/ holds %v, want %v", got, want)
	}
}

// TestRecordType checks the pinned record type: the Terraform resource
// pulumi-cloudflare 6 bridges it from, the renames of the properties the
// DNS platform sets, and its required inputs, which the platform sets.
func TestRecordType(t *testing.T) {
	s, err := schemas.Load(cloudflare.TypeRecord)
	if err != nil {
		t.Fatal(err)
	}
	if s.Terraform.Type != "cloudflare_dns_record" {
		t.Errorf("%s is %s in Terraform, want cloudflare_dns_record", cloudflare.TypeRecord, s.Terraform.Type)
	}
	if got := s.Terraform.Renames["zoneId"]; got != "zone_id" {
		t.Errorf("zoneId is %q in Terraform, want zone_id", got)
	}
	for _, name := range []string{"name", "type", "content", "ttl", "proxied"} {
		if _, ok := s.Terraform.Renames[name]; ok {
			t.Errorf("%s renames %s, which is the same in Terraform", cloudflare.TypeRecord, name)
		}
	}
	if want := []string{"name", "ttl", "type", "zoneId"}; !slices.Equal(s.RequiredInputs, want) {
		t.Errorf("required inputs = %v, want %v", s.RequiredInputs, want)
	}
}

// TestNoDeprecatedProperties walks every property every record of the
// golden environments sets, through the pinned schema of its type, and
// refuses one the provider deprecates. Resolution already refused one the
// schema does not have.
func TestNoDeprecatedProperties(t *testing.T) {
	reg := assemble(t)
	for _, name := range []string{"Staging", "Production", "Preview"} {
		env := resolve(t, reg, shop(), name)
		for _, res := range env.Resources.Resources {
			if !slices.Contains(res.Owners, "dns") {
				continue
			}
			s, err := schemas.Load(res.Type)
			if err != nil {
				t.Fatal(err)
			}
			for key := range res.Properties {
				p := s.Property(key)
				if p == nil {
					t.Errorf("%s: %s sets %s, which %s does not have", name, res.ID, key, res.Type)
				} else if p.DeprecationMessage != "" {
					t.Errorf("%s: %s sets %s, which is deprecated: %s", name, res.ID, key, p.DeprecationMessage)
				}
			}
		}
	}
}
