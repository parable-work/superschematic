import { userAdministration, userSessions } from "@superschematic/api";

// Signs staff and shoppers in and out: login, logout, me, capabilities and
// changePassword, which the identity runtime serves (D50). shop-orders
// authenticates the sessions they start, since both APIs read shop-db's
// users.
@userSessions()
export class Account {}

// Manages users, roles and the grants between them, for staff who hold
// identity.users.write and identity.roles.write.
@userAdministration()
export class AccountAdmin {}
