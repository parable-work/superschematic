/*
The wiring of records-api's TypeScript server (D156): the generated router
over records-db's repositories, with grants and the access log turned on.
A request with a grant's token runs as the grant's holder; any other is the
identity service's, or the project's own authenticator's. The repositories
keep every read of an operation to its caller's audience and grants, so the
router needs nothing more.

The imports name the packages `superschematic build-all` writes for the
example's two services.
*/
import pg from 'pg';
import { Hono } from 'hono';
import type { Authenticator } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { accessLogOf, grantAuthenticator, issueGrant } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@schemas/records-db-types/orm';
import { buildRouter } from '@schemas/records-api-api/router';

/** The server: users through authenticate, parties and agent runs through their grants. */
export function recordsServer(pool: pg.Pool, authenticate: Authenticator, fieldKeys: string): Hono {
  const orm = createOrm(pool, { fieldKeys });
  const app = new Hono();
  app.route(
    '/',
    buildRouter({}, {
      orm,
      authenticate: grantAuthenticator({ orm, inner: authenticate }),
      accessLog: accessLogOf(orm),
    })
  );
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
  return app;
}

/**
 * Lets a party reach one agreement until a date: its sites and their
 * tickets, by the role's reaches. The token is the party's alone; the
 * database keeps its hash.
 */
export async function inviteParty(pool: pg.Pool, party: string, agreement: string, until: Date): Promise<string> {
  const orm = createOrm(pool);
  // The role is data: what a member may do, and what it reaches from the
  // agreement a grant targets.
  const roles = orm.asSystem().role;
  if ((await roles.findOne({ name: { eq: 'member' } } as never)) === null) {
    await roles.createOne({
      name: 'member',
      permissions: ['tickets.read', 'tickets.write', 'agreements.read'],
      reaches: ['Site:agreement', 'Ticket:site.agreement'],
    } as never);
  }
  const { token } = await issueGrant(orm.asSystem(), {
    holder: party,
    holderKind: 'party',
    role: 'member',
    targetType: 'Agreement',
    targetId: agreement,
    endsAt: until,
  });
  return token;
}
