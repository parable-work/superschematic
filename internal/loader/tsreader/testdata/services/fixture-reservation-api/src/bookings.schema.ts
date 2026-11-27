import { auth, behaviorOperations, behaviorWorker } from "@superschematic/api";
import { Booking, Room } from "@schemas/fixture-reservation-db";

// A room's time for any signed-in caller (D103, step 5): Calendar's
// operations, as a room implements it apart from any bundle.
@behaviorOperations(Room)
@auth
export abstract class RoomCalendar {}

// A booking's operations, its Reservation's parts': the status Workflow's
// transition, Holds' and Payments' operations, and Timers', each named as
// the use it is and none for the bundle.
@behaviorOperations(Booking)
@auth
export abstract class BookingReservation {}

// The worker that runs a booking's deadlines, the Timers deadline that
// expires a hold, and its change queue.
@behaviorWorker(Booking)
export abstract class BookingDeadlines {}
