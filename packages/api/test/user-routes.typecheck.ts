// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: @userSessions and @userAdministration go on a class, each with an
// optional config, and register needs the login.
import { userAdministration, userSessions } from "@superschematic/api";

@userSessions()
export class Sessions {}

@userSessions({ path: "account", register: true })
export class SessionsWithRegister {}

@userSessions({ login: false })
export class MeAlone {}

@userAdministration()
export class Administration {}

@userAdministration({ path: "admin" })
export class AdministrationAtAPath {}

// @ts-expect-error register needs the login
@userSessions({ login: false, register: true })
export class RegisterWithoutLogin {}

// @ts-expect-error an unknown key
@userSessions({ roles: true })
export class UnknownSessionsKey {}

// @ts-expect-error administration takes a path alone
@userAdministration({ register: true })
export class UnknownAdministrationKey {}

export class Methods {
  // @ts-expect-error a class decorator, not a method's
  @userSessions()
  login(): void {}
}
