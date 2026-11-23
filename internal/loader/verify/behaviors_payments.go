package verify

import (
	"encoding/json"
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// paymentsBehavior is the core's Payments on a table (D70, D103's step 4).
const paymentsBehavior = "Payments"

// paymentsRefundOf is the part of a Payments config the build reads of a
// refund table: the relation to what it refunds and the adapter's
// permission.
type paymentsRefundOf struct {
	RefundOf *struct {
		Link string `json:"link"`
	} `json:"refundOf"`
	Record *struct {
		Permission string `json:"permission"`
	} `json:"record"`
}

// checkPaymentsConfig holds the config of a Payments use the table td
// implements to the tables of its database (D70, D103's step 4), the
// rules a type's define check on the core cannot see, since they read
// another table: a refund's refundOf names a required to-one relation, to
// a table of the database that implements Payments, is no refund itself,
// and records with the same record.permission, since the adapter that
// records the refund's outcome on that table settles it on the refund in
// the same call, as itself. Each problem is `type <T>: behavior Payments
// config: refundOf.link <link>...: <reason>`, the first reported. A link
// that is no to-one relation of td is the core's to refuse, in its words,
// and so is everything the type's own spec shows (Holds' place, the
// states, the fields). A config the configSchema refuses is reported by
// checkTypeBehaviors and left alone here.
func checkPaymentsConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	if why := holdsPlace(td, ref); why != "" {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, why)
		return
	}
	var config paymentsRefundOf
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil || config.RefundOf == nil {
		return
	}
	link := config.RefundOf.Link
	record := ""
	if config.Record != nil {
		record = config.Record.Permission
	}
	if why := refundOfProblem(schema, td, link, record); why != "" {
		r.errorf(td.Owner, "type %s: behavior %s config: refundOf.link %s%s", td.Name, ref.Name, link, why)
	}
}

// holdsPlace is why an amount from Holds cannot be read on td, in the
// core's words ("" when it can): Holds' after-change hook writes the total
// Payments' reads, so the table lists Holds, and before Payments. The core
// refuses it too, at the first command on the table; the build says so
// before.
func holdsPlace(td *ir.TypeDef, ref ir.BehaviorRef) string {
	var config struct {
		Amount struct {
			Holds bool `json:"holds"`
		} `json:"amount"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil || !config.Amount.Holds {
		return ""
	}
	holds, payments := -1, -1
	for i, use := range td.Behaviors {
		switch {
		case use.Name == holdsBehavior && holds < 0:
			holds = i
		case use.Name == paymentsBehavior && use.UseID() == ref.UseID():
			payments = i
		}
	}
	switch {
	case holds < 0:
		return "amount.holds reads Holds.total, and the type does not list Holds"
	case holds > payments:
		return "amount.holds reads Holds.total, so the type lists Payments after Holds"
	}
	return ""
}

// refundOfProblem is why a refund's refundOf link cannot name what it
// refunds, after the link's name ("" when it can).
func refundOfProblem(schema *ir.Schema, td *ir.TypeDef, link, record string) string {
	field := fieldNamedIn(td, link)
	if field == nil || field.Owned() || field.TypeRef.IsArray || !sqlutil.IsTableRelation(schema, field) {
		// The core refuses a link that is no to-one relation, in its
		// words.
		return ""
	}
	if !field.Required {
		return " is not required, and every refund names what it refunds"
	}
	target := schema.Types[field.TypeRef.Name]
	if target == nil {
		return ""
	}
	var theirs *ir.BehaviorRef
	for _, use := range target.TableBehaviors() {
		if use.Name == paymentsBehavior {
			use := use
			theirs = &use
		}
	}
	if theirs == nil {
		return fmt.Sprintf(": %s, which the link points at, does not implement Payments", target.Name)
	}
	var config paymentsRefundOf
	if len(theirs.Config) > 0 && json.Unmarshal(theirs.Config, &config) != nil {
		return ""
	}
	if config.RefundOf != nil {
		return fmt.Sprintf(": %s, which the link points at, is a refund itself", target.Name)
	}
	if config.Record != nil && config.Record.Permission != record {
		return fmt.Sprintf(": record.permission is %s, and %s's Payments, whose adapter settles this refund's outcome here, records with %s; give both the same", record, target.Name, config.Record.Permission)
	}
	return ""
}
