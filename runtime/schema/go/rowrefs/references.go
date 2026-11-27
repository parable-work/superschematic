package rowrefs

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Reference is a reference a row holds.
type Reference struct {
	ID   string `json:"id"`
	From RowRef `json:"from"`
	To   RowRef `json:"to"`
	// Role is its role, "" for none.
	Role string `json:"role"`
	// AddedBy is the user who added it, in its wire form; "" for none.
	AddedBy string    `json:"addedBy,omitempty"`
	AddedAt time.Time `json:"addedAt"`
}

// The ways a backlink points at a row.
const (
	ViaReference = "reference"
	ViaComment   = "comment"
)

// Backlink is a row that points at another: through a reference it
// holds, with its role, or through a ref block of a message of its
// conversation.
type Backlink struct {
	Table string `json:"table"`
	Key   string `json:"key"`
	Via   string `json:"via"`
	// Role is the reference's role, for a reference.
	Role *string `json:"role,omitempty"`
	// Message is the message, for a ref block.
	Message *int64 `json:"message,omitempty"`
}

// Backlinks are the rows that point at a row, as its reader may see them.
type Backlinks struct {
	Items []Backlink `json:"items"`
	// Hidden is how many rows the reader cannot see point at the row:
	// set only for the owner of a row whose audience is not everyone
	// (D156). Anyone else hears of the rows they see, and of no others.
	Hidden *int `json:"hidden,omitempty"`
}

// AddInput is what Add makes: From pointing at To with Role.
type AddInput struct {
	From RowRef
	To   RowRef
	Role string
}

// RemoveInput is what Remove stops: From pointing at To with Role, or with
// any role when Role is nil.
type RemoveInput struct {
	From RowRef
	To   RowRef
	Role *string
}

// ReferencedByOptions bound ReferencedBy: Limit backlinks, 100 unless set,
// at most 1000.
type ReferencedByOptions struct {
	Limit int
}

// References are the reference operations of an ORM whose database
// declares @references or Comments (D154). A nil References, a no-op
// database's, answers ErrNoDatabase.
type References struct {
	host Host
	def  *ReferencesDefinition
}

// NewReferences is the references of def over host.
func NewReferences(host Host, def *ReferencesDefinition) *References {
	if host == nil || def == nil {
		return nil
	}
	return &References{host: host, def: def}
}

func (r *References) holderOf(table string) (ReferencesHolder, error) {
	holder, ok := r.def.Holders[table]
	if !ok {
		return ReferencesHolder{}, refuse(CodeNotAHolder, map[string]any{"table": table}, "%s keeps no references: it is no @references table", table)
	}
	return holder, nil
}

func (r *References) targetOf(table string) (ReferenceTarget, error) {
	target, ok := r.def.Targets[table]
	if !ok {
		return ReferenceTarget{}, refuse(CodeNotATarget, map[string]any{"table": table}, "%s is no table a reference may point at", table)
	}
	return target, nil
}

const referenceColumns = `id::text AS id, "target_table", "target_key", "role", added_by::text AS added_by, "added_at"`

func (r *References) reference(from RowRef, row Row) Reference {
	table := text(row, "target_table")
	to := r.def.Targets[table]
	out := Reference{
		ID:      UUIDFromDB(text(row, "id")),
		From:    from,
		To:      RowRef{Table: table, Key: wireKey(to.UUID, text(row, "target_key"))},
		Role:    text(row, "role"),
		AddedAt: timeOf(row, "added_at"),
	}
	if added := text(row, "added_by"); added != "" {
		out.AddedBy = UUIDFromDB(added)
	}
	return out
}

// keyText is the target's key as its column's text, or not found.
func keyText(ctx context.Context, h Host, target ReferenceTarget, key string, row RowRef) (string, error) {
	rows, err := h.Query(ctx, fmt.Sprintf(`SELECT %s::text AS key FROM %s WHERE %s = $1`, target.Key, target.Table, target.Key), key)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", h.NotFound(fmt.Sprintf("%s %s is not there", row.Table, row.Key))
	}
	return text(rows[0], "key"), nil
}

func userArg(ctx context.Context, h Host) any {
	if id, ok := h.ActingUser(ctx); ok && id != "" {
		if text := UUIDToDB(id); text != "" {
			return text
		}
	}
	return nil
}

// Add makes in.From point at in.To with in.Role, once per target and
// role, and answers the reference, the one there when it was there. A
// reader who sees neither row is told the same of each: it is not there,
// so a reference never shows whether a hidden row exists.
func (r *References) Add(ctx context.Context, in AddInput) (Reference, error) {
	if r == nil {
		return Reference{}, ErrNoDatabase
	}
	holder, err := r.holderOf(in.From.Table)
	if err != nil {
		return Reference{}, err
	}
	target, err := r.targetOf(in.To.Table)
	if err != nil {
		return Reference{}, err
	}
	if len(holder.Tables) > 0 && !slices.Contains(holder.Tables, in.To.Table) {
		return Reference{}, refuse(CodeTableNotAllowed, map[string]any{"table": in.To.Table, "allowed": holder.Tables}, "%s points only at %s, and not at %s", in.From.Table, strings.Join(holder.Tables, ", "), in.To.Table)
	}
	if len([]rune(in.Role)) > 128 {
		return Reference{}, refuse(CodeInvalid, nil, "a reference's role is a string of at most 128 characters")
	}
	fromKey, err := dbKey(holder.UUID, in.From.Key)
	if err != nil {
		return Reference{}, err
	}
	toKey, err := dbKey(target.UUID, in.To.Key)
	if err != nil {
		return Reference{}, err
	}
	var out Reference
	err = r.host.Transaction(ctx, func(h Host) error {
		if err := mustSee(ctx, h, in.From); err != nil {
			return err
		}
		if err := mustSee(ctx, h, in.To); err != nil {
			return err
		}
		if err := lock(ctx, h, fmt.Sprintf("references:%s:%s", holder.Table, fromKey)); err != nil {
			return err
		}
		key, err := keyText(ctx, h, target, toKey, in.To)
		if err != nil {
			return err
		}
		held, err := h.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s = $1 AND "target_table" = $2 AND "target_key" = $3 AND "role" = $4`, referenceColumns, holder.Table, holder.Row), fromKey, in.To.Table, key, in.Role)
		if err != nil {
			return err
		}
		if len(held) > 0 {
			out = r.reference(in.From, held[0])
			return nil
		}
		counted, err := h.Query(ctx, fmt.Sprintf(`SELECT count(*)::int AS n FROM %s WHERE %s = $1`, holder.Table, holder.Row), fromKey)
		if err != nil {
			return err
		}
		if n := integer(counted[0], "n"); n >= int64(holder.MaxPerRow) {
			return refuse(CodeTooMany, map[string]any{"max": holder.MaxPerRow}, "%s %s holds %d references, the most it may", in.From.Table, in.From.Key, n)
		}
		inserted, err := h.Query(ctx, fmt.Sprintf(`INSERT INTO %s (%s, "target_table", "target_key", "role", "added_by") VALUES ($1, $2, $3, $4, $5) RETURNING %s`, holder.Table, holder.Row, referenceColumns), fromKey, in.To.Table, key, in.Role, userArg(ctx, h))
		if err != nil {
			return err
		}
		out = r.reference(in.From, inserted[0])
		return nil
	})
	return out, err
}

// Remove stops in.From pointing at in.To with in.Role, or with any role
// when it is nil, and answers how many it removed.
func (r *References) Remove(ctx context.Context, in RemoveInput) (int64, error) {
	if r == nil {
		return 0, ErrNoDatabase
	}
	holder, err := r.holderOf(in.From.Table)
	if err != nil {
		return 0, err
	}
	target, err := r.targetOf(in.To.Table)
	if err != nil {
		return 0, err
	}
	fromKey, err := dbKey(holder.UUID, in.From.Key)
	if err != nil {
		return 0, err
	}
	toKey, err := dbKey(target.UUID, in.To.Key)
	if err != nil {
		return 0, err
	}
	var removed int64
	err = r.host.Transaction(ctx, func(h Host) error {
		if err := mustSee(ctx, h, in.From); err != nil {
			return err
		}
		args := []any{fromKey, in.To.Table, toKey}
		role := ""
		if in.Role != nil {
			args = append(args, *in.Role)
			role = ` AND "role" = $4`
		}
		var err error
		removed, err = h.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = $1 AND "target_table" = $2 AND "target_key" = $3::text%s`, holder.Table, holder.Row, role), args...)
		return err
	})
	return removed, err
}

// Of is the references row holds, oldest first, each to a row the reader
// can read: each target the reader cannot read is left out, so its
// existence does not show.
func (r *References) Of(ctx context.Context, row RowRef) ([]Reference, error) {
	if r == nil {
		return nil, ErrNoDatabase
	}
	holder, err := r.holderOf(row.Table)
	if err != nil {
		return nil, err
	}
	fromKey, err := dbKey(holder.UUID, row.Key)
	if err != nil {
		return nil, err
	}
	if err := mustSee(ctx, r.host, row); err != nil {
		return nil, err
	}
	rows, err := r.host.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s = $1 ORDER BY "added_at", id`, referenceColumns, holder.Table, holder.Row), fromKey)
	if err != nil {
		return nil, err
	}
	references := make([]Reference, 0, len(rows))
	byTable := map[string][]string{}
	for _, one := range rows {
		ref := r.reference(row, one)
		references = append(references, ref)
		byTable[ref.To.Table] = append(byTable[ref.To.Table], ref.To.Key)
	}
	seen := map[string]map[string]bool{}
	for _, table := range sortedKeys(byTable) {
		if _, ok := r.def.Targets[table]; !ok {
			seen[table] = map[string]bool{}
			continue
		}
		if seen[table], err = visible(ctx, r.host, table, byTable[table]); err != nil {
			return nil, err
		}
	}
	out := []Reference{}
	for _, ref := range references {
		if seen[ref.To.Table][ref.To.Key] {
			out = append(out, ref)
		}
	}
	return out, nil
}

// ReferencedBy is the rows that point at row, across every table, each
// one the reader can read, most recent first: the references held to it,
// then the ref blocks of messages that name it. Only the owner of a row
// whose audience is not everyone hears how many rows they cannot see
// point at it (D156).
func (r *References) ReferencedBy(ctx context.Context, row RowRef, opts ReferencedByOptions) (Backlinks, error) {
	if r == nil {
		return Backlinks{}, ErrNoDatabase
	}
	limit, err := limitOf(opts.Limit)
	if err != nil {
		return Backlinks{}, err
	}
	target, err := r.targetOf(row.Table)
	if err != nil {
		return Backlinks{}, err
	}
	key, err := dbKey(target.UUID, row.Key)
	if err != nil {
		return Backlinks{}, err
	}
	if err := mustSee(ctx, r.host, row); err != nil {
		return Backlinks{}, err
	}
	keyText, err := keyText(ctx, r.host, target, key, row)
	if err != nil {
		return Backlinks{}, err
	}
	type found struct {
		table, key, via string
		role            *string
		message         *int64
		at              int64
	}
	var all []found
	for _, table := range sortedKeys(r.def.Holders) {
		holder := r.def.Holders[table]
		if len(holder.Tables) > 0 && !slices.Contains(holder.Tables, row.Table) {
			continue
		}
		rows, err := r.host.Query(ctx, fmt.Sprintf(`SELECT %s::text AS holder, "role", "added_at" FROM %s WHERE "target_table" = $1 AND "target_key" = $2 ORDER BY "added_at" DESC LIMIT %d`, holder.Row, holder.Table, maxScan), row.Table, keyText)
		if err != nil {
			return Backlinks{}, err
		}
		for _, one := range rows {
			role := text(one, "role")
			all = append(all, found{table: table, key: wireKey(holder.UUID, text(one, "holder")), via: ViaReference, role: &role, at: timeOf(one, "added_at").UnixMilli()})
		}
	}
	ids := []string{keyText}
	if target.UUID {
		ids = []string{UUIDFromDB(keyText), keyText}
	}
	for _, table := range sortedKeys(r.def.Comments) {
		source := r.def.Comments[table]
		probes := make([]any, len(ids))
		conditions := make([]string, len(ids))
		for i, id := range ids {
			probe, _ := json.Marshal([]map[string]string{{"kind": "ref", "schema": row.Table, "id": id}})
			probes[i] = string(probe)
			conditions[i] = fmt.Sprintf(`%s @> $%d::jsonb`, source.Blocks, i+1)
		}
		rows, err := r.host.Query(ctx, fmt.Sprintf(`SELECT %s::text AS holder, %s::bigint AS message FROM %s WHERE (%s) AND %s IN ('posted', 'interrupted') ORDER BY %s DESC LIMIT %d`, source.Row, source.ID, source.Table, strings.Join(conditions, " OR "), source.State, source.ID, maxScan), probes...)
		if err != nil {
			return Backlinks{}, err
		}
		for _, one := range rows {
			message := integer(one, "message")
			all = append(all, found{table: table, key: wireKey(source.UUID, text(one, "holder")), via: ViaComment, message: &message, at: message})
		}
	}
	// Which of them the reader sees, through each holder's repository.
	byTable := map[string][]string{}
	for _, f := range all {
		if !slices.Contains(byTable[f.table], f.key) {
			byTable[f.table] = append(byTable[f.table], f.key)
		}
	}
	seen := map[string]map[string]bool{}
	holders, shown := 0, 0
	for _, table := range sortedKeys(byTable) {
		keys := byTable[table]
		keep, err := visible(ctx, r.host, table, keys)
		if err != nil {
			return Backlinks{}, err
		}
		seen[table] = keep
		holders += len(keys)
		for _, k := range keys {
			if keep[k] {
				shown++
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at > all[j].at })
	out := Backlinks{Items: []Backlink{}}
	for _, f := range all {
		if len(out.Items) >= limit {
			break
		}
		if !seen[f.table][f.key] {
			continue
		}
		out.Items = append(out.Items, Backlink{Table: f.table, Key: f.key, Via: f.via, Role: f.role, Message: f.message})
	}
	caller, held := r.host.Caller(ctx)
	if held && target.Owner != "" && target.Restricted != "" && holders > shown {
		own, err := r.host.Query(ctx, fmt.Sprintf(`SELECT %s::text AS owner, %s IS NOT NULL AS restricted FROM %s WHERE %s = $1`, target.Owner, target.Restricted, target.Table, target.Key), key)
		if err != nil {
			return Backlinks{}, err
		}
		if len(own) > 0 && boolean(own[0], "restricted") && sameSubject(text(own[0], "owner"), caller.Subject) {
			hidden := holders - shown
			out.Hidden = &hidden
		}
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
