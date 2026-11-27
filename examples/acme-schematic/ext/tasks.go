package ext

import (
	"encoding/json"
	"sort"

	"github.com/parable-work/superschematic/registry"
)

// TaskSection is the name of the task section acme registers: the part of
// the task file acme's forge reads and the core does not (D203). The core
// computes a task's key and the plan's order; when a task runs, on what
// and within how long is the forge's, so it is a section.
const TaskSection = "acme"

// taskSectionFile is the JSON Schema of the section's part of the file:
// the tiers, each with the seconds a run of it may take.
var taskSectionFile = json.RawMessage(`{
	"type": "object",
	"required": ["tiers"],
	"additionalProperties": false,
	"properties": {
		"tiers": {"type": "array", "minItems": 1, "items": {
			"type": "object",
			"required": ["name", "budgetSeconds"],
			"additionalProperties": false,
			"properties": {
				"name": {"type": "string", "pattern": "^[a-z][a-z0-9-]*$"},
				"budgetSeconds": {"type": "integer", "minimum": 1}
			}
		}}
	}
}`)

// taskSectionTask is the JSON Schema of the section's part of one task:
// the tier the task runs in, and its timeout when it has one.
var taskSectionTask = json.RawMessage(`{
	"type": "object",
	"required": ["tier"],
	"additionalProperties": false,
	"properties": {
		"tier": {"type": "string", "minLength": 1},
		"timeoutSeconds": {"type": "integer", "minimum": 1}
	}
}`)

// TaskTier is one tier of the section's part of the file.
type TaskTier struct {
	Name          string `json:"name"`
	BudgetSeconds int    `json:"budgetSeconds"`
}

// TaskPlacement is the section's part of one task.
type TaskPlacement struct {
	Tier           string `json:"tier"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

// registerTaskSection adds the acme section. Its schemas hold the shape of
// each part; Verify holds what a schema cannot say: each tier has a name
// of its own, every task names a tier, each names a tier the file
// declares, and no task's timeout is longer than its tier's budget.
func registerTaskSection(r *registry.Registry) error {
	return r.RegisterTaskSection(registry.TaskSectionSpec{
		Name:      TaskSection,
		Extension: Name,
		File:      taskSectionFile,
		Task:      taskSectionTask,
		Verify:    verifyTaskSection,
	})
}

func verifyTaskSection(req registry.TaskSectionRequest, r registry.VerifyReporter) {
	var file struct {
		Tiers []TaskTier `json:"tiers"`
	}
	if req.File == nil {
		r.Errorf("", "section %s: the tasks name tiers and @tasks declares none; give @tasks sections.%s.tiers", TaskSection, TaskSection)
		return
	}
	if err := json.Unmarshal(req.File, &file); err != nil {
		r.Errorf("", "section %s: %v", TaskSection, err)
		return
	}
	budgets := map[string]int{}
	for _, tier := range file.Tiers {
		if _, dup := budgets[tier.Name]; dup {
			r.Errorf("", "section %s: tier %s is declared twice", TaskSection, tier.Name)
		}
		budgets[tier.Name] = tier.BudgetSeconds
	}
	declared := make([]string, 0, len(budgets))
	for name := range budgets {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	for _, name := range req.Names {
		raw, ok := req.Tasks[name]
		if !ok {
			r.Errorf("", "section %s: task %s names no tier, so acme's forge would never run it; give it sections.%s.tier", TaskSection, name, TaskSection)
			continue
		}
		var placement TaskPlacement
		if err := json.Unmarshal(raw, &placement); err != nil {
			r.Errorf("", "section %s: task %s: %v", TaskSection, name, err)
			continue
		}
		budget, ok := budgets[placement.Tier]
		switch {
		case !ok:
			r.Errorf("", "section %s: task %s runs in tier %s, which @tasks does not declare (declared: %v)", TaskSection, name, placement.Tier, declared)
		case placement.TimeoutSeconds > budget:
			r.Errorf("", "section %s: task %s may run %d seconds, longer than the %d its tier %s allows a whole run", TaskSection, name, placement.TimeoutSeconds, budget, placement.Tier)
		}
	}
}
