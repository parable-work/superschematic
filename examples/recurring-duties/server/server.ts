/*
What duties-api's implementation adds to its generated TypeScript server
(D152). The generated server wires the rest itself: a duty's Recurrence
operations are the router's, over duties-db's repositories; the calendar
read, its feed and the feeds' issue, rotation and revocation are
calendarRoutes', which it mounts at /api/calendar beside the router, and
the calendar tool its MCP endpoint serves; a grant's token, a feed's among
them, runs through grantAuthenticator in front of the implementation's
authenticator, so a feed's token reads its feed alone. The behavior worker
DutyDeadlines fires each duty's deadlines; its main is the build's too.

Left to the app: each calendar source's permission, beside its table's
audience and grants (createRuntimeOptions), which the schema does not
declare yet.

The imports name the package `superschematic build-all` writes for
duties-api.
*/
import type { RuntimeOptionsConstructor } from '@schemas/duties-api-api';

/** A caller without duties.work sees no duty on the calendar, whatever its audience. */
export const createRuntimeOptions: RuntimeOptionsConstructor = () => ({
  calendar: { permissions: { duty: 'duties.work' } },
});
