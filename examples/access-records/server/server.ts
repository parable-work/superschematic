/*
What records-api's implementation adds to its generated TypeScript server
(D156). The generated server wires the rest itself: it runs a request with
a grant's token as the grant's holder, through grantAuthenticator in front
of the implementation's authenticator, which establishes every other
caller, and gives the router the access log (accessLogOf), which records
each call an API key or a grant makes and each read an agent run makes.
The repositories keep every read of an operation to its caller's audience
and grants, so the router needs nothing more.

Left to the app: giving a party its grant.

The imports name the package `superschematic build-all` writes for
records-db.
*/
import pg from 'pg';
import { issueGrant } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@schemas/records-db-types/orm';

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
