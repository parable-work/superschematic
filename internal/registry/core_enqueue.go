package registry

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// enqueueOnChangeDecorator returns @enqueueOnChange, from @superschematic/db
// and only in DB schemas: a trigger on the class's table that enqueues a
// message of a queue of the schema after a write to its rows (D80). Apply
// reads the declaration's shape; verification checks it against the table
// and the queue, which it reads in full.
func enqueueOnChangeDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "enqueueOnChange", Packages: []string{pkgDB}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindDB)},
		Apply: func(n Node, args []any, _ Site) error {
			def, err := enqueueOnChangeOf(args)
			if err != nil {
				return err
			}
			for _, other := range n.Type.EnqueueOnChange {
				if other.Queue == def.Queue {
					return fmt.Errorf("@enqueueOnChange of %s names queue %s twice; declare one per queue, whose on lists every write that enqueues", n.Type.Name, def.Queue)
				}
			}
			n.Type.EnqueueOnChange = append(n.Type.EnqueueOnChange, def)
			return nil
		},
	}
}

// enqueueOnChangeOf reads @enqueueOnChange(Queue, { on, fields, when,
// message }).
func enqueueOnChangeOf(args []any) (*ir.EnqueueOnChange, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("@enqueueOnChange takes the @queue class and an options object: @enqueueOnChange(Queue, { on, fields, when, message })")
	}
	queue, err := DecodeClassRef(args[0])
	if err != nil {
		return nil, ArgErrorf(0, "@enqueueOnChange takes the @queue class of the schema whose message it enqueues: %v", err)
	}
	obj, ok := args[1].(map[string]any)
	if !ok {
		return nil, ArgErrorf(1, "@enqueueOnChange takes an options object: { on, fields, when, message }")
	}
	def := &ir.EnqueueOnChange{Queue: queue}
	for key, value := range obj {
		switch key {
		case "on":
			if def.On, err = stringList(value); err != nil {
				return nil, ArgErrorf(1, "@enqueueOnChange on is a list of the writes that enqueue, \"insert\", \"update\" and \"delete\": %v", err)
			}
		case "fields":
			if def.Fields, err = stringList(value); err != nil {
				return nil, ArgErrorf(1, "@enqueueOnChange fields is a list of the table's fields whose change enqueues: %v", err)
			}
		case "when":
			conditions, err := enqueueConditions(value)
			if err != nil {
				return nil, ArgErrorf(1, "@enqueueOnChange when: %v", err)
			}
			def.When = conditions
		case "message":
			m, ok := value.(map[string]any)
			if !ok {
				return nil, ArgErrorf(1, "@enqueueOnChange message maps each field of the message to a field of the table, as { lineItemId: \"id\" }")
			}
			def.Message = map[string]string{}
			for field, from := range m {
				s, ok := from.(string)
				if !ok {
					return nil, ArgErrorf(1, "@enqueueOnChange message field %s maps to %v; name a field of the table, as \"id\"", field, from)
				}
				def.Message[field] = s
			}
		default:
			return nil, ArgErrorf(1, "@enqueueOnChange has no option %q; it takes on, fields, when and message", key)
		}
	}
	if err := CheckEnqueueOnChange(def); err != nil {
		return nil, ArgErrorf(1, "@enqueueOnChange(%s): %v", queue, err)
	}
	// The writes in their one order, so every form writes them alike.
	sort.SliceStable(def.On, func(i, j int) bool {
		return slices.Index(ir.EnqueueEvents(), def.On[i]) < slices.Index(ir.EnqueueEvents(), def.On[j])
	})
	return def, nil
}

// CheckEnqueueOnChange checks an @enqueueOnChange's shape in every form: a
// queue, writes that are insert, update or delete, each once, fields only
// with an update, each condition one of isNull, notNull and equals, and a
// message that maps at least one field. Whether the fields name the
// table's, and the message's fields line up with the queue's, is
// verification's to check.
func CheckEnqueueOnChange(def *ir.EnqueueOnChange) error {
	if def.Queue == "" {
		return fmt.Errorf("names no queue")
	}
	if len(def.On) == 0 {
		return fmt.Errorf("on names no write; list the writes that enqueue, as on: [\"insert\", \"update\"]")
	}
	seen := map[string]bool{}
	for _, event := range def.On {
		if !slices.Contains(ir.EnqueueEvents(), event) {
			return fmt.Errorf("on names %q; it lists %s", event, strings.Join(ir.EnqueueEvents(), ", "))
		}
		if seen[event] {
			return fmt.Errorf("on names %q twice", event)
		}
		seen[event] = true
	}
	if len(def.Fields) > 0 && !seen[ir.EnqueueOnUpdate] {
		return fmt.Errorf("fields names the fields whose change makes an update enqueue, and on names no update")
	}
	fields := map[string]bool{}
	for _, f := range def.Fields {
		if f == "" || fields[f] {
			return fmt.Errorf("fields names %q twice or empty", f)
		}
		fields[f] = true
	}
	for i, c := range def.When {
		set := 0
		if c.IsNull {
			set++
		}
		if c.NotNull {
			set++
		}
		if c.Equals != nil {
			set++
			switch c.Equals.(type) {
			case string, float64, bool:
			default:
				return fmt.Errorf("when[%d] equals %v; it compares with a string, a number or a boolean", i, c.Equals)
			}
		}
		switch {
		case c.Field == "":
			return fmt.Errorf("when[%d] names no field", i)
		case set != 1:
			return fmt.Errorf("when[%d] on %s sets %d of isNull, notNull and equals; a condition sets one", i, c.Field, set)
		}
	}
	if len(def.Message) == 0 {
		return fmt.Errorf("message maps no field; map each field of the message to a field of the table, as message: { lineItemId: \"id\" }")
	}
	for field, from := range def.Message {
		if field == "" || from == "" {
			return fmt.Errorf("message maps %q to %q; both name a field", field, from)
		}
	}
	return nil
}

// stringList reads a list of strings.
func stringList(value any) ([]string, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("want a list of strings, got %v", value)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("want a list of strings, got %v in it", v)
		}
		out = append(out, s)
	}
	return out, nil
}

// enqueueConditions reads @enqueueOnChange's when: a list of
// { field, isNull: true }, { field, notNull: true } and
// { field, equals: literal }.
func enqueueConditions(value any) ([]*ir.EnqueueCondition, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("want a list of conditions, as [{ field: \"category\", notNull: true }]")
	}
	var out []*ir.EnqueueCondition
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("condition %d is %v; write { field, isNull: true }, { field, notNull: true } or { field, equals: literal }", i, item)
		}
		c := &ir.EnqueueCondition{}
		for key, v := range obj {
			switch key {
			case "field":
				s, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("condition %d's field is %v; name a field of the table", i, v)
				}
				c.Field = s
			case "isNull", "notNull":
				b, ok := v.(bool)
				if !ok || !b {
					return nil, fmt.Errorf("condition %d's %s is %v; write %s: true", i, key, v, key)
				}
				if key == "isNull" {
					c.IsNull = true
				} else {
					c.NotNull = true
				}
			case "equals":
				if v == nil {
					return nil, fmt.Errorf("condition %d equals null; write isNull: true", i)
				}
				c.Equals = v
			default:
				return nil, fmt.Errorf("condition %d has no key %q; it takes field and one of isNull, notNull and equals", i, key)
			}
		}
		out = append(out, c)
	}
	return out, nil
}
