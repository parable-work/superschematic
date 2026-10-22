import { Validate, behavior } from "@superschematic/schema";

@behavior("Calendar", {
  timeZone: "Europe/London",
  hours: {
    mon: [["09:00", "12:30"], ["13:30", "17:30"]],
    tue: [["09:00", "17:30"]],
    wed: [["09:00", "17:30"]],
    thu: [["09:00", "17:30"]],
    fri: [["09:00", "17:30"]],
    sat: [["10:00", "16:00"]],
  },
  capacity: 4,
  slotMs: 900000,
  minMs: 900000,
  maxMs: 900000,
  bufferMs: { after: 300000 },
  leadMs: 7200000,
  horizonMs: 5184000000,
  permission: "calendar.manage",
})
export abstract class CollectionPoint {
  name: Validate<string, { maxLength: 200 }>;
}
