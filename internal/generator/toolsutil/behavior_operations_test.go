package toolsutil

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// behaviorEndpoint is an endpoint of namespace running op of type's use,
// a behavior operation the loader added (D103).
func behaviorEndpoint(namespace, typeName, use, op string) apigen.EndpointInfo {
	return apigen.EndpointInfo{
		Name: use + strings.ToUpper(op[:1]) + op[1:], Namespace: namespace, HandlerName: typeName + use + op,
		Operation: &ir.FieldDef{BehaviorOperation: &ir.BehaviorOperation{Type: typeName, Use: use, Behavior: "Workflow", Operation: op}},
	}
}

// TestBehaviorMembers: a namespace's behavior operations group by use, in
// first-seen order, each into a member every SDK names for the use; a use
// whose member would be an operation of the namespace, a member another
// use takes, or one the SDKs reserve is refused, naming both; the tool and
// method names nest under the member.
func TestBehaviorMembers(t *testing.T) {
	getOrder := apigen.EndpointInfo{Name: "getOrder", Namespace: "order", Operation: &ir.FieldDef{}}
	members, err := BehaviorMembers("shipment", []apigen.EndpointInfo{
		behaviorEndpoint("shipment", "Shipment", "paymentStatus", "transition"),
		getOrder,
		behaviorEndpoint("shipment", "Shipment", "status", "transition"),
		behaviorEndpoint("shipment", "Shipment", "paymentStatus", "refund"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, member := range members {
		var ops []string
		for _, ep := range member.Endpoints {
			ops = append(ops, ep.BehaviorOperation().Operation)
		}
		got = append(got, member.Type+"."+member.Use+"("+member.Behavior+"): "+strings.Join(ops, ", "))
	}
	if want := "Shipment.paymentStatus(Workflow): transition, refund; Shipment.status(Workflow): transition"; strings.Join(got, "; ") != want {
		t.Errorf("members = %q, want %q", strings.Join(got, "; "), want)
	}
	if members, err := BehaviorMembers("order", []apigen.EndpointInfo{getOrder}); err != nil || members != nil {
		t.Errorf("a namespace with no behavior operation = %v, %v", members, err)
	}

	for _, tc := range []struct {
		name      string
		endpoints []apigen.EndpointInfo
		want      string
	}{
		{
			"an operation of the member's name",
			[]apigen.EndpointInfo{{Name: "status", Namespace: "order", Operation: &ir.FieldDef{}}, behaviorEndpoint("order", "Order", "status", "transition")},
			"toolsutil: namespace order holds the operation status and the operations of Order's status, which every SDK puts under order.status; rename the operation, or give the set a name of another namespace",
		},
		{
			"two tables' uses of one name",
			[]apigen.EndpointInfo{behaviorEndpoint("order", "Order", "status", "transition"), behaviorEndpoint("order", "Invoice", "status", "transition")},
			"toolsutil: namespace order holds the operations of Order's status and of Invoice's status, which every SDK puts under order.status; give one set a name of another namespace",
		},
		{
			"a member the SDKs reserve",
			[]apigen.EndpointInfo{behaviorEndpoint("order", "Order", "client", "transition")},
			"toolsutil: namespace order holds the operations of Order's client, which every SDK puts under order.client, which the SDKs reserve; rename the field",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BehaviorMembers("order", tc.endpoints); err == nil || err.Error() != tc.want {
				t.Errorf("BehaviorMembers = %v, want %q", err, tc.want)
			}
		})
	}

	ep := behaviorEndpoint("shipment", "Shipment", "paymentStatus", "transition")
	if got := EndpointToolName(ep); got != "shipment.paymentStatus.transition" {
		t.Errorf("EndpointToolName = %q", got)
	}
	if got := EndpointMethodName(ep); got != "paymentStatus.transition" {
		t.Errorf("EndpointMethodName = %q", got)
	}
	if got := EndpointToolName(getOrder); got != "order.getOrder" {
		t.Errorf("EndpointToolName of an authored operation = %q", got)
	}
	if got := BehaviorMemberDoc("Order", "status", "Workflow"); got != "The operations of Order's status, a Workflow (D103)." {
		t.Errorf("BehaviorMemberDoc = %q", got)
	}
	if got := BehaviorMemberPhrase("Order", "fill", "acme.Gauge"); got != "the operations of Order's fill, an acme.Gauge (D103)" {
		t.Errorf("BehaviorMemberPhrase = %q", got)
	}
}
