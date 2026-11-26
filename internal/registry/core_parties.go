package registry

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// partyDecorators returns the decorators of outside parties and inbound
// messages (D157), each from @superschematic/db and only in DB schemas:
// @party on an app table whose rows are parties; @conversations("<part>")
// on each class that is one of the conversations' tables; and
// @runRequests on the @queue class an inbound message from a party with
// an active grant puts a run request on. Apply reads each declaration's
// shape; ir.Schema.Validate checks what the declaration alone decides, and
// the loader's verification pass checks it against the fields and the
// tables.
func partyDecorators() []DecoratorSpec {
	return []DecoratorSpec{
		{
			Name: "party", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if n.Type.Party != nil {
					return fmt.Errorf("a class takes one @party")
				}
				party, err := partyOf(n.Type.Name, args)
				if err != nil {
					return err
				}
				n.Type.Party = party
				return nil
			},
		},
		{
			Name: "conversations", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if n.Type.Conversations != nil {
					return fmt.Errorf("a class takes one @conversations")
				}
				shape := fmt.Sprintf("@conversations(part), part one of %s", strings.Join(ir.ConversationParts, ", "))
				if len(args) != 1 {
					return fmt.Errorf("@conversations on %s takes one argument: %s", n.Type.Name, shape)
				}
				part, ok := args[0].(string)
				if !ok || !slices.Contains(ir.ConversationParts, part) {
					return ArgErrorf(0, "@conversations on %s: %v is no part of the conversations: %s", n.Type.Name, args[0], shape)
				}
				n.Type.Conversations = &ir.ConversationsDef{Part: part}
				return nil
			},
		},
		{
			Name: "runRequests", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if len(args) != 0 {
					return fmt.Errorf("@runRequests on %s takes no arguments: the loader fills the queue's fields", n.Type.Name)
				}
				n.Type.RunRequests = &ir.RunRequestsDef{}
				return nil
			},
		},
	}
}

// partyOf reads @party<T>({ title? }) on the class named typeName. No
// argument names a party by its key.
func partyOf(typeName string, args []any) (*ir.PartyDef, error) {
	const shape = "@party<T>({ title? })"
	party := &ir.PartyDef{}
	if len(args) == 0 {
		return party, nil
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("@party on %s takes at most one options object: %s", typeName, shape)
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@party on %s takes an options object: %s", typeName, shape)
	}
	for _, key := range sortedKeys(obj) {
		text, ok := obj[key].(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, ArgErrorf(0, "@party on %s: %s is a non-empty string; got %v", typeName, key, obj[key])
		}
		switch key {
		case "title":
			party.Title = text
		default:
			return nil, ArgErrorf(0, "@party on %s has no option %q; it takes title", typeName, key)
		}
	}
	return party, nil
}
