import { auth, behaviorOperations } from "@superschematic/api";
import { Booking, Desk } from "@schemas/fixture-calendar-db";

// A desk's time for any signed-in caller (D70, D103's step 4):
// setOpeningHours, addClosure and removeClosure under calendar, which need
// desk.manage, listBookings, findAvailability on the table as a whole, and
// the contract Supply's operations, which a caller's direct call is
// not_requested of unless it changes nothing: a booking takes time
// through its Holds alone.
@behaviorOperations(Desk)
@auth
export abstract class DeskCalendar {}

// A booking's holds: retakeHolds and listHolds under holds, beside the
// status Workflow's transition, whose moves take, commit and release its
// slot.
@behaviorOperations(Booking)
@auth
export abstract class BookingHolds {}
