import { Contact, Identity } from "superscalar";
import { Approvals, AutoGenerate, User, UserRole, Workflow, key, unique } from "@superschematic/db";

// A person who signs in.
export abstract class Account implements User<{ login: "email" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;
}

// A named set of permissions.
export abstract class Role implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];
}

// A change that is ready once a reviewer signs it off. The gate reads each
// signer's permission again at each guard (recheckPermission, D200), from
// the roles the identity tables grant them now, so a reviewer whose role is
// revoked after signing stops counting.
export abstract class Change
  implements
    Approvals<Change, {
      gates: {
        ready: { count: 1; permission: "changes.approve"; recheckPermission: true; excludeCreator: false; resetOn: [] };
      };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  status: Workflow<{
    states: ["open", "ready"];
    transitions: [{ from: "open"; to: "ready" }, { from: "ready"; to: "open" }];
  }>;
}
