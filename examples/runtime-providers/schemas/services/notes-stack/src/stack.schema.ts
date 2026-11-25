import { NotesApi } from "@notes/notes-api";
import { NotesDb } from "@notes/notes-db";
import { environment, stack } from "@superschematic/stack";

// notes-api over notes-db. Each environment binds notes-api's provider
// slots in its env, so environment.json shows the provider every slot of
// every deployable uses (D159). The two environments send SMS through two
// relays, and only their env says so. Each secret setting, SMS_TOKEN and
// VAULT_TOKEN, is entered per environment with `stack secrets set`.
@stack({ deploy: [NotesApi] })
export abstract class Notes {}

// This machine: sms-relay-a, the vault broker and the local sandbox, each
// in front of a stub on loopback.
@environment({
  target: "local",
  settings: [
    {
      of: NotesApi,
      env: {
        SMS: "sms-relay-a",
        SMS_URL: "http://127.0.0.1:4310",
        VAULT: "vault-broker",
        VAULT_URL: "http://127.0.0.1:4320",
        VAULT_VAULTS: "ops",
        RUNNER: "local-sandbox",
        MODEL: "stub-model",
        PREVIEWS: "text-preview",
      },
    },
  ],
})
export abstract class Dev {}

// Cloudflare: notes-api is a Worker and notes-db a Postgres reached
// through Hyperdrive (D92). It sends SMS through sms-relay-b, which reads
// an account as well, and runs no agents, so it names no sandbox or model.
// The account, its subdomain and Secrets Store, and the config are
// placeholders, which resolve offline.
@environment({
  target: "cloudflare",
  cloudflare: {
    accountId: "0123456789abcdef0123456789abcdef",
    workersSubdomain: "notes-subdomain",
    secretsStoreId: "fedcba9876543210fedcba9876543210",
  },
  settings: [
    { of: NotesDb, hyperdrive: "00000000000000000000000000000000" },
    {
      of: NotesApi,
      env: {
        SMS: "sms-relay-b",
        SMS_URL: "https://sms-relay-b.example.test",
        SMS_ACCOUNT: "AC100",
        PREVIEWS: "text-preview",
      },
    },
  ],
})
export abstract class Cloudflare {}
