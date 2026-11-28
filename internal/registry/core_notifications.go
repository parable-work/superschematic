package registry

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// notificationDecorators returns the decorators of events, notifications
// and channels (D153), each from @superschematic/db and only in DB schemas:
// @event on a table, repeated per kind; @notifier("<part>") on each class
// that is one of the notifier's tables; and @channelIdentities on the class
// that is the table of channel identities. Apply reads each declaration's
// shape; ir.Schema.Validate checks what the declaration alone decides, and
// the loader's verification pass checks it against the fields and the
// tables.
func notificationDecorators() []DecoratorSpec {
	return []DecoratorSpec{
		{
			Name: "event", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				event, err := eventOf(n.Type.Name, args)
				if err != nil {
					return err
				}
				n.Type.Events = append(n.Type.Events, event)
				return nil
			},
		},
		{
			Name: "notifier", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if n.Type.Notifier != nil {
					return fmt.Errorf("a class takes one @notifier")
				}
				shape := fmt.Sprintf("@notifier(part), part one of %s", strings.Join(ir.NotifierParts, ", "))
				if len(args) != 1 {
					return fmt.Errorf("@notifier on %s takes one argument: %s", n.Type.Name, shape)
				}
				part, ok := args[0].(string)
				if !ok || !slices.Contains(ir.NotifierParts, part) {
					return ArgErrorf(0, "@notifier on %s: %v is no part of the notifier: %s", n.Type.Name, args[0], shape)
				}
				n.Type.Notifier = &ir.NotifierDef{Part: part}
				return nil
			},
		},
		{
			Name: "channelIdentities", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if len(args) != 0 {
					return fmt.Errorf("@channelIdentities on %s takes no arguments: the loader fills the class's fields", n.Type.Name)
				}
				n.Type.ChannelIdentities = &ir.ChannelIdentitiesDef{}
				return nil
			},
		},
	}
}

// eventOf reads @event<T>({ kind, payload?, title?, urgency?, description? })
// on the class named typeName.
func eventOf(typeName string, args []any) (*ir.EventDef, error) {
	const shape = "@event<T>({ kind, payload?, title?, urgency?, description? })"
	if len(args) != 1 {
		return nil, fmt.Errorf("@event on %s takes one options object: %s", typeName, shape)
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@event on %s takes an options object: %s", typeName, shape)
	}
	event := &ir.EventDef{}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		value := obj[key]
		if key == "payload" {
			list, ok := value.([]any)
			if !ok {
				return nil, ArgErrorf(0, "@event on %s: payload is a list of the table's fields, as [\"title\", \"priority\"]", typeName)
			}
			for _, item := range list {
				name, ok := item.(string)
				if !ok || name == "" {
					return nil, ArgErrorf(0, "@event on %s: payload lists fields by name; got %v", typeName, item)
				}
				event.Payload = append(event.Payload, name)
			}
			continue
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, ArgErrorf(0, "@event on %s: %s is a non-empty string; got %v", typeName, key, value)
		}
		switch key {
		case "kind":
			event.Kind = text
		case "title":
			event.Title = text
		case "urgency":
			event.Urgency = text
		case "description":
			event.Description = text
		default:
			return nil, ArgErrorf(0, "@event on %s has no option %q; it takes kind, payload, title, urgency and description", typeName, key)
		}
	}
	if event.Kind == "" {
		return nil, ArgErrorf(0, "@event on %s needs a kind: %s", typeName, shape)
	}
	return event, nil
}

// notificationsFeature is the notifier as a runtime feature (D159): an API
// whose databases use it (ir.Schema.UsesNotifications) needs a channel
// adapter, so the stack refuses an environment that runs such an API with
// no channel slot set.
func notificationsFeature() RuntimeFeature {
	return RuntimeFeature{
		Name:      "notifications",
		Interface: ir.RuntimeChannel,
		Uses: func(in RuntimeFeatureInput) bool {
			if in.API.UsesNotifications() {
				return true
			}
			for _, db := range in.Databases {
				if db.UsesNotifications() {
					return true
				}
			}
			return false
		},
	}
}
