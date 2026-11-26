/*
The wiring of duties-api's TypeScript server (D152): the generated router
over duties-db's repositories, and the calendar's routes beside it. A
duty's Recurrence operations are the router's; the calendar read, its feed
and the feeds' issue, rotation and revocation are calendarRoutes'. The
behavior worker DutyDeadlines fires each duty's deadlines; its main is the
build's.

The imports name the packages `superschematic build-all` writes for the
example's two services.
*/
import pg from 'pg';
import { Hono } from 'hono';
import type { Authenticator, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { calendarRoutes, grantAuthenticator } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@schemas/duties-db-types/orm';
import { buildRouter } from '@schemas/duties-api-api/router';

/** The server: users through authenticate; a feed's token reads its feed alone. */
export function dutiesServer(pool: pg.Pool, authenticate: Authenticator): Hono {
  const orm = createOrm(pool);
  const app = new Hono();
  // A source's permission: a caller without duties.work sees no duty on
  // the calendar, whatever its audience.
  const calendar = calendarRoutes({
    orm,
    authenticate: request => authenticate({ headers: request.headers } as RequestContext),
    permissions: { duty: 'duties.work' },
  });
  app.all('/api/calendar/*', async c => (await calendar(c.req.raw)) ?? c.notFound());
  app.get('/api/calendar', async c => (await calendar(c.req.raw)) ?? c.notFound());
  app.route('/', buildRouter({}, { orm, authenticate: grantAuthenticator({ orm, inner: authenticate }) }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
  return app;
}
