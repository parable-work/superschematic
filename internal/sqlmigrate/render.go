package sqlmigrate

import (
	"fmt"
	"strings"
)

// phaseNotes say when each phase runs, in the renderings.
var phaseNotes = map[Phase]string{
	Expand:   "Runs before the new servers roll out.",
	Contract: "Runs after the new servers roll out.",
}

// SQL renders the plan as one SQL script for a reader: a header comment
// with the service, dialect, hashes and renames, then each step's
// statements, each ending with a semicolon, under a comment naming its
// index, phase, operation, subject and hazard ids. A step that runs
// outside a transaction says so, with its recovery statements commented
// out. The script is for review: the runner applies the plan JSON, which
// carries what the script cannot, such as each step's transaction.
func (p *Plan) SQL() string {
	var b strings.Builder
	fmt.Fprintf(&b, "-- Migration plan for %s (%s)\n", p.Service, p.Dialect)
	fmt.Fprintf(&b, "-- from:    %s\n", hashOrEmpty(p.From))
	fmt.Fprintf(&b, "-- to:      %s\n", p.To)
	if p.Hash != "" {
		fmt.Fprintf(&b, "-- plan:    %s\n", p.Hash)
	}
	if len(p.Renames) > 0 {
		fmt.Fprintf(&b, "-- renames: %s\n", strings.Join(p.Renames, ", "))
	}
	fmt.Fprintf(&b, "-- %s\n", p.stepSummary())

	var phase Phase
	for _, step := range p.Steps {
		if step.Phase != phase {
			phase = step.Phase
			fmt.Fprintf(&b, "\n-- %s. %s\n", phaseTitle(phase), phaseNotes[phase])
		}
		fmt.Fprintf(&b, "\n-- Step %d, %s, %s %s\n", step.Index, step.Phase, step.Op, step.Subject)
		for _, hazard := range step.Hazards {
			fmt.Fprintf(&b, "-- hazard %s\n", hazard.ID)
		}
		if step.ForeignKeysOff {
			b.WriteString("-- foreign keys are off around this step and checked before its commit\n")
		}
		if !step.Transactional {
			b.WriteString("-- not in a transaction: each statement runs on its own\n")
			for _, statement := range step.Recovery {
				b.WriteString(commentLines("recovery: "+statement+";", "--   "))
			}
		}
		for _, statement := range step.Statements {
			b.WriteString(statement)
			b.WriteString(";\n")
		}
	}
	return b.String()
}

// Markdown renders the plan for a pull request: a summary line, a table of
// the hazards (class, subject, reader, reason, id), then the expand and
// contract steps, each with its SQL in a fenced block. A plan with no
// steps says so.
func (p *Plan) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Migration plan for `%s` (%s)\n\n", p.Service, p.Dialect)
	if len(p.Steps) == 0 {
		b.WriteString("No steps: the database needs no change.\n\n")
	} else {
		fmt.Fprintf(&b, "%s.\n\n", p.stepSummary())
	}
	if p.From == "" {
		b.WriteString("- From: an empty database\n")
	} else {
		fmt.Fprintf(&b, "- From: `%s`\n", p.From)
	}
	fmt.Fprintf(&b, "- To: `%s`\n", p.To)
	if p.Hash != "" {
		fmt.Fprintf(&b, "- Plan: `%s`\n", p.Hash)
	}
	if len(p.Renames) > 0 {
		renames := make([]string, 0, len(p.Renames))
		for _, rename := range p.Renames {
			renames = append(renames, "`"+rename+"`")
		}
		fmt.Fprintf(&b, "- Renames: %s\n", strings.Join(renames, ", "))
	}
	if len(p.Steps) == 0 {
		return b.String()
	}

	if hazards := p.Hazards(); len(hazards) > 0 {
		b.WriteString("\n| Class | Subject | Reader | Reason | ID |\n")
		b.WriteString("| --- | --- | --- | --- | --- |\n")
		for _, hazard := range hazards {
			reader := ""
			if hazard.Reader != "" {
				reader = "`" + hazard.Reader + "`"
			}
			fmt.Fprintf(&b, "| %s | `%s` | %s | %s | `%s` |\n",
				hazard.Class, hazard.Subject, reader, markdownCell(hazard.Reason), hazard.ID)
		}
	}

	for _, phase := range []Phase{Expand, Contract} {
		var steps []*Step
		for _, step := range p.Steps {
			if step.Phase == phase {
				steps = append(steps, step)
			}
		}
		if len(steps) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", phaseTitle(phase), phaseNotes[phase])
		for _, step := range steps {
			fmt.Fprintf(&b, "\n**%d. %s** `%s`", step.Index, step.Op, step.Subject)
			if len(step.Hazards) > 0 {
				classes := make([]string, 0, len(step.Hazards))
				for _, hazard := range step.Hazards {
					classes = append(classes, string(hazard.Class))
				}
				fmt.Fprintf(&b, ". Hazards: %s", strings.Join(classes, ", "))
			}
			b.WriteString(".")
			if !step.Transactional {
				b.WriteString(" Runs outside a transaction.")
			}
			if step.ForeignKeysOff {
				b.WriteString(" Runs with foreign keys off, checked before its commit.")
			}
			b.WriteString("\n\n```sql\n")
			for _, statement := range step.Statements {
				b.WriteString(statement)
				b.WriteString(";\n")
			}
			b.WriteString("```\n")
		}
	}
	return b.String()
}

// stepSummary counts the steps by phase and the hazards by class:
// "3 steps (2 expand, 1 contract), 2 hazards (1 destructive, 1 compat)".
func (p *Plan) stepSummary() string {
	phases := map[Phase]int{}
	for _, step := range p.Steps {
		phases[step.Phase]++
	}
	summary := counted(len(p.Steps), "step")
	if len(p.Steps) > 0 {
		summary += fmt.Sprintf(" (%d expand, %d contract)", phases[Expand], phases[Contract])
	}
	hazards := p.Hazards()
	summary += ", " + counted(len(hazards), "hazard")
	if len(hazards) == 0 {
		return summary
	}
	classes := map[HazardClass]int{}
	for _, hazard := range hazards {
		classes[hazard.Class]++
	}
	var parts []string
	for _, class := range HazardClasses {
		if classes[class] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", classes[class], class))
		}
	}
	return summary + " (" + strings.Join(parts, ", ") + ")"
}

// counted writes n with noun, plural unless n is 1; zero is "no".
func counted(n int, noun string) string {
	switch n {
	case 0:
		return "no " + noun + "s"
	case 1:
		return "1 " + noun
	default:
		return fmt.Sprintf("%d %ss", n, noun)
	}
}

func phaseTitle(phase Phase) string {
	switch phase {
	case Expand:
		return "Expand"
	case Contract:
		return "Contract"
	default:
		return string(phase)
	}
}

func hashOrEmpty(hash string) string {
	if hash == "" {
		return "empty database"
	}
	return hash
}

// commentLines prefixes the first line of text with "-- " and every other
// line with continuation, ending each with a newline.
func commentLines(text, continuation string) string {
	var b strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i == 0 {
			b.WriteString("-- ")
		} else {
			b.WriteString(continuation)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// markdownCell keeps text on one table row: pipes escaped, line breaks
// turned into spaces.
func markdownCell(text string) string {
	text = strings.ReplaceAll(text, "|", `\|`)
	return strings.Join(strings.Fields(text), " ")
}
