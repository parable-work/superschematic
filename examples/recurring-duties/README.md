# recurring-duties

A duties app with recurrence and a calendar (D152): duties that recur on a
calendar rule, after they were last done, or before an agreement ends;
due dates that move off weekends and the holidays of a duty's calendar;
and a calendar, read and fed to a calendar client, that shows each person
only what they may read. The guide's
[A row's recurrence, and the calendar](https://parable-work.github.io/superschematic/guides/database-tables/#a-rows-recurrence-and-the-calendar)
section covers it in full.

| Path | What it is |
|---|---|
| `schemas/services/duties-db` | DB, Postgres: `Duty` implements `Recurrence` and is an `@audience` table its owner sees; `Holiday`, the rows of the holiday calendars the app fills; `Agreement`, whose end a duty may count back from; both `Duty` (its occurrences) and `Agreement` (its end date) are `@calendarSource`s; `Account`, `Role` and `Grant`, which a feed's token is one of |
| `schemas/services/duties-api` | API over `duties-db`: `DutyActions` serves `setRecurrence`, `complete`, `skip`, `moveOne` and `listOccurrences`; `DutyDeadlines` is the behavior worker that fires each occurrence's open, remind, overdue and plan deadlines |
| `server/server.ts` | what the implementation adds to the generated server, which mounts `calendarRoutes` for the calendar read, the feed, and the feeds' issue, rotation and revocation; each source's permission is the schema's, and `createRuntimeOptions` may name another |

## What it shows

- `setRecurrence({ anchor: "calendar", rule: "FREQ=MONTHLY;BYMONTHDAY=-1",
  start: "2027-01-31T17:00" })` plans the last day of each month at 17:00
  in Paris up to 400 days ahead. A month that ends on a Saturday is due on
  the Monday, or the Tuesday when the Monday is a holiday of the duty's
  calendar.
- Each occurrence opens a week before it is due, reminds two days before,
  and is missed a day after. A missed one never blocks the next.
- Changing the rule replans the occurrences that have not opened and
  leaves the open one as it was.
- `complete({ completedBy: { table: "Payment", key: "p-17" } })` records
  what completed it and who.
- `GET /api/calendar?from=...&to=...` merges the duties' occurrences and
  the agreements' end dates. A private duty is on its owner's calendar
  alone.
- `POST /api/calendar/feeds { role, detail: "busy" }` gives the caller a
  feed URL for a calendar client. It shows "Busy" and a reference number
  for each entry, reads only what the caller reads, and is refused once it
  is rotated, revoked or a year old.

## Where it runs

`TestRecurringDutiesExampleLoads` (`internal/loader`) loads these schemas.
The same scenario, under the fixture names `fixture-recurrence-db` and
`fixture-recurrence-api`, runs end to end against Postgres in
`TestGeneratedRecurrenceEndToEnd` (`internal/generator/tsrestgen`), and the
feed's access in `TestGeneratedAccessEndToEnd`.

The calendar read and its feed are the TypeScript runtime's. The generated
TypeScript server of an API over a Postgres database with a
`@calendarSource` mounts `calendarRoutes` at `/api/calendar` beside its
router, serves the `calendar` tool on its MCP endpoint, and puts
`grantAuthenticator` in front of the implementation's authenticator, so a
feed's token reads its feed alone.
