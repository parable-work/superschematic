package cloudflare

import (
	"encoding/json"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

const testZoneID = "0123456789abcdef0123456789abcdef"

func context(proxied bool, records ...ir.DNSRecord) registry.DNSContext {
	return registry.DNSContext{
		Environment: registry.StackEnvironment{Stack: "shop", Name: "Staging", Domain: "staging.acme.dev"},
		Values:      map[string]any{"zone": "acme.dev", "zoneId": testZoneID, "proxied": proxied},
		Records:     records,
	}
}

// TestLowerRecordTypes lowers the record types exposure produces, as the
// gcp target's exposure does for a host and its certificate's validation,
// with and without proxying.
func TestLowerRecordTypes(t *testing.T) {
	host := ir.Concat{"shop-api-", ir.Parameter("pr"), ".staging.acme.dev"}
	records := []ir.DNSRecord{
		{Deployable: "shop-api", Name: host, Type: "A", Value: ir.Output{Resource: "shop-api.address", Name: "address"}},
		{Deployable: "shop-api", Name: host, Type: "AAAA", Value: "2001:db8::1"},
		{Deployable: "shop-api", Name: ir.Concat{"_acme-challenge.shop-api-", ir.Parameter("pr"), ".staging.acme.dev"}, Type: "CNAME",
			Value: ir.Output{Resource: "shop-api.dns-authorization", Name: "dnsResourceRecords[0].data"}},
		{Deployable: "www", Name: "www.staging.acme.dev.", Type: "CNAME", Value: "shop-api.staging.acme.dev."},
		{Deployable: "www", Name: "_verify.staging.acme.dev", Type: "TXT", Value: `token "x"`},
		{Deployable: "www", Name: "staging.acme.dev", Type: "TXT", Value: ir.Output{Resource: "www.verification", Name: "token"}},
	}
	for _, proxied := range []bool{false, true} {
		got, err := lowerRecords(context(proxied, records...))
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, res := range got {
			if res.Type != TypeRecord || res.Properties["zoneId"] != testZoneID {
				t.Errorf("%s: type %s zone %v", res.ID, res.Type, res.Properties["zoneId"])
			}
			data, err := json.Marshal(map[string]any{
				"name": res.Properties["name"], "content": res.Properties["content"],
				"proxied": res.Properties["proxied"], "ttl": res.Properties["ttl"],
			})
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, res.ID+" "+res.Properties["type"].(string)+" "+string(data))
		}
		hostTTL, hostProxied := "300", "false"
		if proxied {
			hostTTL, hostProxied = "1", "true"
		}
		want := []string{
			`dns.shop-api.a A {"content":{"$output":{"resource":"shop-api.address","name":"address"}},"name":{"$concat":["shop-api-",{"$parameter":"pr"},".staging.acme.dev"]},"proxied":` + hostProxied + `,"ttl":` + hostTTL + `}`,
			`dns.shop-api.aaaa AAAA {"content":"2001:db8::1","name":{"$concat":["shop-api-",{"$parameter":"pr"},".staging.acme.dev"]},"proxied":` + hostProxied + `,"ttl":` + hostTTL + `}`,
			`dns.shop-api.cname CNAME {"content":{"$output":{"resource":"shop-api.dns-authorization","name":"dnsResourceRecords[0].data"}},"name":{"$concat":["_acme-challenge.shop-api-",{"$parameter":"pr"},".staging.acme.dev"]},"proxied":false,"ttl":300}`,
			`dns.www.cname CNAME {"content":"shop-api.staging.acme.dev","name":"www.staging.acme.dev","proxied":` + hostProxied + `,"ttl":` + hostTTL + `}`,
			`dns.www.txt TXT {"content":"\"token \\\"x\\\"\"","name":"_verify.staging.acme.dev","proxied":false,"ttl":300}`,
			`dns.www.txt.2 TXT {"content":{"$concat":["\"",{"$output":{"resource":"www.verification","name":"token"}},"\""]},"name":"staging.acme.dev","proxied":false,"ttl":300}`,
		}
		if strings.Join(lines, "\n") != strings.Join(want, "\n") {
			t.Errorf("proxied=%v:\n%s\nwant:\n%s", proxied, strings.Join(lines, "\n"), strings.Join(want, "\n"))
		}
	}
}

// TestLowerRefuses: a record type Cloudflare DNS does not write, a record
// outside the zone and a domain outside it fail with what is at fault.
func TestLowerRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  registry.DNSContext
		want string
	}{
		{"type", context(false, ir.DNSRecord{Deployable: "shop-api", Name: "staging.acme.dev", Type: "MX", Value: "mail.acme.dev"}),
			"record staging.acme.dev of shop-api has type MX; the cloudflare DNS platform writes A, AAAA, CNAME and TXT records"},
		{"record outside", context(false, ir.DNSRecord{Deployable: "shop-api", Name: "shop-api.acme.com", Type: "A", Value: "192.0.2.1"}),
			"record shop-api.acme.com of shop-api is not in zone acme.dev"},
		{"concat outside", context(false, ir.DNSRecord{Deployable: "shop-api", Name: ir.Concat{"shop-api-", ir.Parameter("pr"), ".notacme.dev"}, Type: "A", Value: "192.0.2.1"}),
			`record {"$concat":["shop-api-",{"$parameter":"pr"},".notacme.dev"]} of shop-api is not in zone acme.dev`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := lowerRecords(tc.ctx)
			if err == nil || err.Error() != tc.want {
				t.Errorf("err = %v, want %s", err, tc.want)
			}
		})
	}
	ctx := context(false)
	ctx.Environment.Domain = "acme.devx"
	if _, err := lowerRecords(ctx); err == nil || !strings.Contains(err.Error(), "domain acme.devx, which is not in zone acme.dev") {
		t.Errorf("domain outside the zone: %v", err)
	}
}

// TestTXTContent quotes a TXT value as RFC 1035 character strings of at
// most 255 bytes each, split between characters.
func TestTXTContent(t *testing.T) {
	if got := txtContent(`"already" "quoted"`); got != `"already" "quoted"` {
		t.Errorf("a quoted value = %v", got)
	}
	if got := txtContent(`a\b`); got != `"a\\b"` {
		t.Errorf("a backslash = %v", got)
	}
	long := strings.Repeat("a", 254) + "\u00e9" + strings.Repeat("b", 10)
	parts := strings.Split(txtContent(long).(string), `" "`)
	if len(parts) != 2 || len(strings.Trim(parts[0], `"`)) != 254 || strings.Trim(parts[1], `"`) != "\u00e9"+strings.Repeat("b", 10) {
		t.Errorf("a long value splits into %q", parts)
	}
}
