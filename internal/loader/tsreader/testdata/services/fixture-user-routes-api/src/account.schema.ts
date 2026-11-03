import { HttpMethod, auth, rest, userAdministration, userSessions } from "@superschematic/api";

// Signs users in and out, and lets anyone register: a new user waits for
// an administrator's approval as a member, and the first is the
// administrator (D81).
@userSessions({
  register: true,
  registration: "approval",
  defaultRoles: ["member"],
  firstUser: { role: "admin", permissions: ["identity"] },
})
export class Account {}

// Manages users, roles and the grants between them.
@userAdministration()
export class AccountAdmin {}

// A greeting for the signed-in user.
export abstract class Greeting {
  message: string;
}

// An operation the project implements, beside the routes the identity
// runtime serves.
export class GreetingQueries {
  @auth
  @rest(HttpMethod.GET, "greeting")
  greet(): Greeting {
    throw new Error("schema declaration only");
  }
}
