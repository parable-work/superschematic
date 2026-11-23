import { auth, behaviorOperations } from "@superschematic/api";
import { Order, Refund } from "@schemas/fixture-payments-db";

// An order's payments for any signed-in caller (D70, D103's step 4):
// openPayment, capturePayment, voidPayment and refundPayment under
// payments, the adapter's recordPaymentEvent and listPaymentActions, the
// second on the table as a whole, which need payments.record, and
// refundFor, which takes a call only from a refund row whose relation
// points at the order.
@behaviorOperations(Order)
@auth
export abstract class OrderPayments {}

// A refund's settleRefundFor, which the adapter's event on the order it
// refunds calls, beside the status Workflow's transition, whose move into
// approved asks for the refund.
@behaviorOperations(Refund)
@auth
export abstract class RefundPayments {}
