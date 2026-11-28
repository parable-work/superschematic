import { behaviorOperations, behaviorWorker, requirePermission } from "@superschematic/api";
import { Checkout, Refund } from "@acme/shop-db";

// A refund's sign-offs and its moves, for staff who hold refunds.approve
// (D70, D103): Approvals' signOff, which counts two members of staff, not
// the one who asked for it, before the move to approved, and the status
// Workflow's transition, whose move into approved asks the checkout's
// Payments for the refund, which shop-orders' SettlePayments makes at
// acmepay. The build serves each, with no method of the implementation's.
// They are shop-api's, the staff's API, rather than shop-orders', whose
// Topcoat crate renders no table's behaviors yet.
@behaviorOperations(Refund)
@requirePermission(["refunds.approve"])
export abstract class RefundActions {}

// Runs Checkout's deadlines and its change queue (D103): it expires a
// checkout still held fifteen minutes on, or at the two-hour backstop
// while a payment is in flight, which lets go of the units and the slot
// it holds and voids its payment. A worker of shop-api's, a process of its
// own, with no method of the implementation's.
@behaviorWorker(Checkout)
export abstract class CheckoutTimers {}
