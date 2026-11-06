package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// schedulesBehavior is the name of the core's Schedules, which the
// behaviors' core alone implements, on a table (D104).
const schedulesBehavior = "Schedules"

// The bounds a Schedules config takes when it names none (D104): a minute
// and thirty days, the core's schedules::DEFAULT_MIN_MS and DEFAULT_MAX_MS.
const (
	schedulesDefaultMinMs int64 = 60_000
	schedulesDefaultMaxMs int64 = 2_592_000_000
)

// schedulesConfig is a Schedules config as the build reads it (D104).
type schedulesConfig struct {
	MinMs    *int64   `json:"minMs"`
	MaxMs    *int64   `json:"maxMs"`
	Kinds    []string `json:"kinds"`
	TimeZone *string  `json:"timeZone"`
	Outcomes []string `json:"outcomes"`
	Retry    *struct {
		Outcome string `json:"outcome"`
		AfterMs int64  `json:"afterMs"`
	} `json:"retry"`
	EndOn *struct {
		Enters []string `json:"enters"`
	} `json:"endOn"`
	Status *string `json:"status"`
}

// checkSchedulesConfig holds the config of a Schedules use the table td
// implements to the table (D104), in the core's words and order
// (superschematic_rules_behaviors::schedules): minMs is no more than maxMs;
// a timeZone needs cron among kinds; retry's outcome is one of outcomes and
// its afterMs lies within minMs..maxMs; a status needs an endOn; endOn
// reads the Workflow status names, or the table's one (flowFor), and each
// state it enters is a state of it. Each problem is `type <T>: behavior
// Schedules config: <reason>`, and like the core it reports the first. It
// does not hold timeZone to the IANA database, which the core bundles and
// the build does not link: the core refuses an unknown zone when the
// table's first command defines it (D104). A config the configSchema
// refuses is reported by checkTypeBehaviors and left alone here.
func checkSchedulesConfig(_ *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, _ map[string]registry.Behavior, r *Result) {
	var config schedulesConfig
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	if why := schedulesProblem(td, config); why != "" {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, why)
	}
}

// schedulesProblem is the first problem of a table's Schedules config, or
// "".
func schedulesProblem(td *ir.TypeDef, config schedulesConfig) string {
	minMs, maxMs := schedulesDefaultMinMs, schedulesDefaultMaxMs
	if config.MinMs != nil {
		minMs = *config.MinMs
	}
	if config.MaxMs != nil {
		maxMs = *config.MaxMs
	}
	if minMs > maxMs {
		return fmt.Sprintf("minMs (%d) is more than maxMs (%d): no delay or interval lies between them", minMs, maxMs)
	}
	if config.TimeZone != nil && !slices.Contains(config.Kinds, "cron") {
		return "timeZone names the zone a cron line is read in, and kinds leaves out cron"
	}
	if retry := config.Retry; retry != nil {
		if !slices.Contains(config.Outcomes, retry.Outcome) {
			return fmt.Sprintf("retry.outcome %q is not one of outcomes (%s)", retry.Outcome, strings.Join(config.Outcomes, ", "))
		}
		if retry.AfterMs < minMs || retry.AfterMs > maxMs {
			return fmt.Sprintf("retry.afterMs (%d) is outside minMs..maxMs (%d..%d): a retry is held to the bounds a schedule is", retry.AfterMs, minMs, maxMs)
		}
	}
	if config.EndOn == nil {
		if config.Status != nil {
			return "status names the Workflow endOn reads, and the config gives no endOn"
		}
		return ""
	}
	workflow, why := flowOf(td, config.Status)
	if why != "" {
		return why
	}
	if workflow.states == nil {
		return ""
	}
	for _, state := range config.EndOn.Enters {
		if !slices.Contains(workflow.states, state) {
			return fmt.Sprintf("endOn.enters names %q, which is not a state of the type's Workflow (%s)", state, strings.Join(workflow.states, ", "))
		}
	}
	return ""
}
