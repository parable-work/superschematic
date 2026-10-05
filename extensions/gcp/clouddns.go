package gcp

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// lowerRecords is the Cloud DNS platform (section 6.9): it writes each
// record into the managed zone that holds the environment's domain, in the
// environment's project. The values name the zone, and its project when
// the zone lives in another; the zone defaults to the domain with its dots
// as hyphens (`staging.acme.dev` is `staging-acme-dev`). The zone itself
// is not created here: it holds the domain before the stack does.
func lowerRecords(ctx registry.DNSContext) ([]*ir.Resource, error) {
	zone, _ := ctx.Values["zone"].(string)
	if zone == "" {
		zone = strings.ReplaceAll(ctx.Environment.Domain, ".", "-")
	}
	project, _ := ctx.Values["project"].(string)
	if project == "" {
		project = valuesOf(ctx.Environment).project
	}
	count := map[string]int{}
	var out []*ir.Resource
	for _, rec := range ctx.Records {
		key := rec.Deployable + "." + strings.ToLower(rec.Type)
		count[key]++
		id := "dns." + key
		if count[key] > 1 {
			id = fmt.Sprintf("%s.%d", id, count[key])
		}
		out = append(out, &ir.Resource{ID: id, Type: TypeRecordSet, Properties: map[string]any{
			"project":     project,
			"managedZone": zone,
			"name":        join(rec.Name, "."),
			"type":        rec.Type,
			"ttl":         recordTTL,
			"rrdatas":     []any{rec.Value},
		}})
	}
	return out, nil
}
