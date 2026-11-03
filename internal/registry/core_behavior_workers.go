package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// behaviorWorkerDecorator registers @behaviorWorker(Type, options?) from
// @superschematic/api, only in API schemas (D103): on a class with no
// members, a worker of the API that runs the table's behavior queues (its
// deadlines) through the behaviors' core. The class is no type: Apply
// records the worker in Schema.Workers, as @worker's does, with
// BehaviorWorker set and no queue, which the loader fills from the table's
// database. The rules that read the API's database are the verification
// pass's (verify.CheckBehaviorWorkers).
func behaviorWorkerDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "behaviorWorker", Packages: []string{pkgAPI}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindAPI)},
		Apply: func(n Node, args []any, _ Site) error {
			worker, err := behaviorWorkerOf(n.Type, args)
			if err != nil {
				return err
			}
			if n.Schema.Worker(worker.Name) != nil {
				return fmt.Errorf("@worker class %s is declared twice", worker.Name)
			}
			n.Schema.Workers = append(n.Schema.Workers, worker)
			return nil
		},
	}
}

// behaviorWorkerOf reads @behaviorWorker(Type, { concurrency, grace,
// permissions }) on td.
func behaviorWorkerOf(td *ir.TypeDef, args []any) (*ir.Worker, error) {
	if len(td.Fields) > 0 {
		return nil, fmt.Errorf("@behaviorWorker class %s has fields; it runs its table's behaviors, so its class holds none", td.Name)
	}
	if td.Extends != "" || len(td.Implements) > 0 {
		return nil, fmt.Errorf("@behaviorWorker class %s extends or implements another class; a worker's class declares the worker and nothing else", td.Name)
	}
	if len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("@behaviorWorker takes the table and an optional options object: { concurrency, grace, permissions }")
	}
	table, err := DecodeClassRef(args[0])
	if err != nil {
		return nil, ArgErrorf(0, "@behaviorWorker's table is a class of the API's database, imported from it: %v", err)
	}
	worker := &ir.Worker{Name: td.Name, Comment: td.Comment, BehaviorWorker: &ir.BehaviorWorkerConfig{Type: table}}
	if len(args) == 2 {
		obj, ok := args[1].(map[string]any)
		if !ok {
			return nil, ArgErrorf(1, "@behaviorWorker takes the table and an optional options object: { concurrency, grace, permissions }")
		}
		for _, key := range sortedKeys(obj) {
			value := obj[key]
			switch key {
			case "concurrency":
				n, ok := value.(float64)
				if !ok || n != float64(int(n)) || n < 1 {
					return nil, ArgErrorf(1, "@behaviorWorker concurrency is %v; it is a whole number, one or more", value)
				}
				worker.Concurrency = int(n)
			case "grace":
				grace, ok := value.(string)
				if !ok {
					return nil, ArgErrorf(1, "@behaviorWorker grace is %v; it is a duration such as \"8s\"", value)
				}
				if _, err := TimeoutSeconds(grace); err != nil {
					return nil, ArgErrorf(1, "@behaviorWorker grace is %v; it is a duration such as \"8s\"", value)
				}
				worker.Grace = grace
			case "permissions":
				list, ok := value.([]any)
				if !ok {
					return nil, ArgErrorf(1, "@behaviorWorker permissions is a list of permissions, each a non-empty string")
				}
				permissions := make([]string, 0, len(list))
				for _, p := range list {
					s, ok := p.(string)
					if !ok || s == "" {
						return nil, ArgErrorf(1, "@behaviorWorker permissions is a list of permissions, each a non-empty string")
					}
					permissions = append(permissions, s)
				}
				if len(permissions) > 0 {
					worker.BehaviorWorker.Permissions = permissions
				}
			default:
				return nil, ArgErrorf(1, "@behaviorWorker has no option %q; it takes concurrency, grace and permissions", key)
			}
		}
	}
	if err := CheckWorker(worker); err != nil {
		return nil, fmt.Errorf("@behaviorWorker %s: %v", worker.Name, err)
	}
	return worker, nil
}
