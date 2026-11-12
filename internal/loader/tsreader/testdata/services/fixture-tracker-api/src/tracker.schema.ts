import { Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import {
  Authenticated,
  FilterBy,
  HttpMethod,
  SearchQuery,
  SortBy,
  paginated,
  publicRoute,
  requirePermission,
  rest,
  source,
  tableOperation
} from "@superschematic/api";
import { Comment, Issue, IssueStatus, Member, Note, Team } from "@schemas/fixture-tracker-db";

// A team as the API shows it.
@source(Team)
export abstract class TeamView {
  id: Identity.UUID;
  key: Identity.Slug;
  name: Identity.Name;
}

// What a caller sets to create a team.
@source(Team)
export abstract class CreateTeamInput {
  key: Identity.Slug;
  name: Identity.Name;
}

// A person an issue is assigned to.
@source(Member)
export abstract class MemberView {
  id: Identity.UUID;
  name: Identity.Name;
}

// What a caller sets to add a member.
@source(Member)
export abstract class CreateMemberInput {
  name: Identity.Name;
}

// An issue as a caller who may read issues sees it: its team and its
// assignee by their keys, and when it was created.
@source(Issue)
export abstract class IssueView {
  id: Identity.UUID;
  teamId: Identity.UUID;
  title: string;
  body: Nullable<string>;
  status: IssueStatus;
  priority: Generic.Int64;
  estimate: Nullable<number>;
  dueOn: Nullable<Temporal.Date>;
  assigneeId: Nullable<Identity.UUID>;
  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
  updatedAt: Nullable<Temporal.DateTime>;
}

// An issue as anyone sees it: fewer of the same columns.
@source(Issue)
export abstract class IssueGuestView {
  id: Identity.UUID;
  title: string;
  status: IssueStatus;
}

// What a caller sets to create an issue under a team: the team is the
// path's, and status and priority take the table's defaults when unsaid.
@source(Issue)
export abstract class CreateIssueInput {
  title: Validate<string, { minLength: 1; maxLength: 200 }>;
  body: Nullable<string>;
  priority: Nullable<Generic.Int64>;
  estimate: Nullable<number>;
  dueOn: Nullable<Temporal.Date>;
  assigneeId: Nullable<Identity.UUID>;
}

// What a caller may change of an issue: each member left out leaves its
// column as it is, and null clears one that may be null.
@source(Issue)
export abstract class UpdateIssueInput {
  title: Nullable<Validate<string, { minLength: 1; maxLength: 200 }>>;
  body: Nullable<string>;
  status: Nullable<IssueStatus>;
  priority: Nullable<Generic.Int64>;
  estimate: Nullable<number>;
  dueOn: Nullable<Temporal.Date>;
  assigneeId: Nullable<Identity.UUID>;
}

// A comment on an issue.
@source(Comment)
export abstract class CommentView {
  id: Identity.UUID;
  issueId: Identity.UUID;
  body: string;
  createdAt: Temporal.DateTime;
}

// What a caller sets to comment on an issue, which the path names.
@source(Comment)
export abstract class CreateCommentInput {
  body: Validate<string, { minLength: 1 }>;
}

// A person's note on an issue.
@source(Note)
export abstract class NoteView {
  id: Identity.UUID;
  issueId: Identity.UUID;
  owner: Identity.UUID;
  sharedWith: Nullable<Identity.UUID>;
  text: string;
  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
}

// What a caller sets to write a note: the issue is the path's, and the
// row rule makes the caller its owner.
@source(Note)
export abstract class CreateNoteInput {
  text: Validate<string, { minLength: 1 }>;
  sharedWith: Nullable<Identity.UUID>;
}

// What a note's owner may change.
@source(Note)
export abstract class UpdateNoteInput {
  text: Nullable<Validate<string, { minLength: 1 }>>;
  sharedWith: Nullable<Identity.UUID>;
}

// How many issues a team has open: no view of a table, so the operation
// that answers it is written by hand.
export abstract class TeamSummary {
  teamId: Identity.UUID;
  openIssues: number;
}

export class Teams extends Authenticated {
  // The teams, a page at a time.
  @rest(HttpMethod.GET, "teams")
  @paginated()
  @tableOperation()
  listTeams(): TeamView[] {
    throw new Error("schema declaration only");
  }

  // One team.
  @rest(HttpMethod.GET, "teams/{id}")
  @tableOperation()
  getTeam(id: Identity.UUID): TeamView {
    throw new Error("schema declaration only");
  }

  // A new team; a key another team holds is a conflict.
  @rest(HttpMethod.POST, "teams")
  @requirePermission(["teams.write"])
  @tableOperation()
  createTeam(input: CreateTeamInput): TeamView {
    throw new Error("schema declaration only");
  }

  // A team's open issues, counted: a set holds operations the build
  // serves beside ones the implementation writes.
  @rest(HttpMethod.GET, "teams/{id}/summary")
  summarizeTeam(id: Identity.UUID): TeamSummary {
    throw new Error("schema declaration only");
  }
}

export class Members extends Authenticated {
  // The members, a page at a time.
  @rest(HttpMethod.GET, "members")
  @paginated()
  @tableOperation()
  listMembers(): MemberView[] {
    throw new Error("schema declaration only");
  }

  // A new member.
  @rest(HttpMethod.POST, "members")
  @requirePermission(["members.write"])
  @tableOperation()
  createMember(input: CreateMemberInput): MemberView {
    throw new Error("schema declaration only");
  }
}

export class Issues extends Authenticated {
  // A team's issues, newest first unless the caller sorts them, searched
  // by their text and filtered by their fields.
  @rest(HttpMethod.GET, "teams/{teamId}/issues")
  @requirePermission(["issues.read"])
  @paginated({ defaultLimit: 2, maxLimit: 5, defaultSort: "-createdAt" })
  @tableOperation()
  listIssues(
    teamId: Identity.UUID,
    q: SearchQuery<Validate<string, { maxLength: 100 }>>,
    sort: SortBy<IssueView, ["createdAt", "priority", "title"]>,
    filter: FilterBy<IssueView, ["status", "priority", "title", "estimate", "dueOn", "assigneeId", "createdAt", "createdBy"]>
  ): IssueView[] {
    throw new Error("schema declaration only");
  }

  // One issue.
  @rest(HttpMethod.GET, "issues/{id}")
  @requirePermission(["issues.read"])
  @tableOperation()
  getIssue(id: Identity.UUID): IssueView {
    throw new Error("schema declaration only");
  }

  // One issue as anyone sees it: another view of the same table behind
  // another guard.
  @rest(HttpMethod.GET, "public/issues/{id}")
  @publicRoute
  @tableOperation()
  getIssueForGuest(id: Identity.UUID): IssueGuestView {
    throw new Error("schema declaration only");
  }

  // A new issue of the team the path names.
  @rest(HttpMethod.POST, "teams/{teamId}/issues")
  @requirePermission(["issues.write"])
  @tableOperation()
  createIssue(teamId: Identity.UUID, input: CreateIssueInput): IssueView {
    throw new Error("schema declaration only");
  }

  // Changes the members the body sets.
  @rest(HttpMethod.PATCH, "issues/{id}")
  @requirePermission(["issues.write"])
  @tableOperation()
  updateIssue(id: Identity.UUID, input: UpdateIssueInput): IssueView {
    throw new Error("schema declaration only");
  }

  // Deletes the issue, which is kept, and answers it as it was.
  @rest(HttpMethod.DELETE, "issues/{id}")
  @requirePermission(["issues.write"])
  @tableOperation()
  deleteIssue(id: Identity.UUID): IssueView {
    throw new Error("schema declaration only");
  }
}

export class Comments extends Authenticated {
  // An issue's comments, a page at a time.
  @rest(HttpMethod.GET, "issues/{issueId}/comments")
  @requirePermission(["issues.read"])
  @paginated()
  @tableOperation()
  listComments(issueId: Identity.UUID): CommentView[] {
    throw new Error("schema declaration only");
  }

  // A new comment on the issue the path names.
  @rest(HttpMethod.POST, "issues/{issueId}/comments")
  @requirePermission(["issues.write"])
  @tableOperation()
  createComment(issueId: Identity.UUID, input: CreateCommentInput): CommentView {
    throw new Error("schema declaration only");
  }

  // Deletes a comment of the issue the path names; one of another issue
  // is not found.
  @rest(HttpMethod.DELETE, "issues/{issueId}/comments/{id}")
  @requirePermission(["issues.write"])
  @tableOperation()
  deleteComment(issueId: Identity.UUID, id: Identity.UUID): CommentView {
    throw new Error("schema declaration only");
  }
}

// Notes are their owner's (D108): each operation's row rule names the
// row's members that hold who may reach it, and a row that names another
// person is not found.
export class Notes extends Authenticated {
  // The caller's notes, their own and the ones shared with them. A holder
  // of notes.admin lists everyone's.
  @rest(HttpMethod.GET, "notes")
  @paginated({ defaultSort: "createdAt" })
  @tableOperation({ by: ["owner", "sharedWith"], overridePermission: "notes.admin" })
  listNotes(sort: SortBy<NoteView, ["createdAt"]>, filter: FilterBy<NoteView, ["issueId", "owner"]>): NoteView[] {
    throw new Error("schema declaration only");
  }

  // One note, its owner's or one shared with the caller.
  @rest(HttpMethod.GET, "notes/{id}")
  @tableOperation({ by: ["owner", "sharedWith"], overridePermission: "notes.admin" })
  getNote(id: Identity.UUID): NoteView {
    throw new Error("schema declaration only");
  }

  // A new note on the issue the path names, the caller's own.
  @rest(HttpMethod.POST, "issues/{issueId}/notes")
  @tableOperation({ by: ["owner"] })
  createNote(issueId: Identity.UUID, input: CreateNoteInput): NoteView {
    throw new Error("schema declaration only");
  }

  // Changes a note of the caller's own: one shared with them is not theirs
  // to change.
  @rest(HttpMethod.PATCH, "notes/{id}")
  @tableOperation({ by: ["owner"] })
  updateNote(id: Identity.UUID, input: UpdateNoteInput): NoteView {
    throw new Error("schema declaration only");
  }

  // Deletes a note the caller wrote.
  @rest(HttpMethod.DELETE, "notes/{id}")
  @tableOperation({ by: ["createdBy"], overridePermission: "notes.admin" })
  deleteNote(id: Identity.UUID): NoteView {
    throw new Error("schema declaration only");
  }
}
