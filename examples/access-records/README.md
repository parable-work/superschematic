# access-records

A records app with per-record access (D156): rows each user sees or does
not, a row rule that follows relations, a party outside the user table who
reaches only what a grant allows, and a log of sensitive reads. The
guide's [Per-record access](https://parable-work.github.io/superschematic/guides/api-routes/#per-record-access)
section covers it in full.

| Path | What it is |
|---|---|
| `schemas/services/records-db` | DB, Postgres: `Person`, the user table; `Workspace` and `Page` are `@audience` tables, a page inheriting its workspace's audience; `Agreement`, `AgreementMember`, `Site` and `Ticket`; `Role`, whose `reaches` say what a grant of the role reaches; `Grant`, the `@accessGrants` table; and `AccessEntry`, the `@accessLog` table |
| `schemas/services/records-api` | API over `records-db`, every operation served by the build (`@tableOperation`): workspaces and pages, which keep to the caller's audience; `listMyTickets`, whose row rule is the path `site.agreement.members.member`, beside `listTickets` and `openTicket`; and `getAgreement`, whose `terms` open only for a holder of `agreements.reveal` |
| `server/server.ts` | the server's wiring: `grantAuthenticator` in front of the project's authenticator, and `accessLogOf` as the router's access log; and `inviteParty`, which gives a party a grant on one agreement until a date |

## What it shows

- A page in a private workspace is not in another user's list, read or
  search. Making the workspace private hides its pages at once: a trigger
  resolves each page's audience again when its workspace's changes.
- An agent run sees what whom it acts for sees, and writes a page only
  when the page lets agent runs follow their role.
- `GET my/tickets` holds a user to the tickets of the agreements that list
  them, one `EXISTS`-style condition per step of the path.
- A party's token reaches the agreement it targets, its sites and their
  tickets, by the role's reaches, and nothing else. The next call after
  the grant ends, by its date, its agreement's status or a revocation, is
  refused.
- A reveal of `terms` writes a row of `AccessEntry`, as does each call
  made with a grant or an API key, and each read an agent run makes.

## Where it runs

`TestAccessRecordsExampleLoads` (`internal/loader/access`) loads these
schemas. The same scenario, with folders, documents and contracts in
place of workspaces, pages and agreements (`fixture-access-db` and
`fixture-access-api`), runs end to end against Postgres in
`TestGeneratedAccessEndToEnd` (`internal/generator/tsrestgen`).

Per-record access is the TypeScript server's, on Postgres: the Go and
Rust servers do not apply audiences, row rules through relations, grants
or reveal yet, and an audience needs Postgres.
