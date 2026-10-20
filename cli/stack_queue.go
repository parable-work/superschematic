package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/queueops"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
)

// `superschematic stack queue <dead|retry|drop> <environment> <db>.<Queue>`
// (docs/stack-model.md, sections 8.8 and 11.1): an operator's view of a
// queue's dead messages, which it lists, makes ready again or deletes. The
// migration runner runs each operation on the queue's database, since it
// holds the drivers (D27), from a document that carries the statements
// sqlutil writes for the queue's table (internal/queueops). On the local
// target it runs against the Postgres container stack dev runs; a cloud
// target's databases are not reached yet.

func init() { registerStackCommands(newStackQueueCmd) }

// localQueueRunner is the local provisioner's run of a queue document
// (local.Provisioner.Queue).
type localQueueRunner interface {
	Queue(ctx context.Context, req registry.ProvisionRequest, service string, document []byte) ([]byte, error)
}

// queueFlags are the queue commands' flags.
type queueFlags struct {
	stackDir   string
	namingPath string
	format     string
	limit      int
	all        bool
	yes        bool
}

func (f *queueFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.stackDir, "stack", "", "the Stack service directory (default: the working directory when it is one, else the one Stack service under ./schemas/services)")
	cmd.Flags().StringVar(&f.namingPath, "naming", "", "naming config file (default <stack>/../../superschematic.toml)")
	cmd.Flags().StringVar(&f.format, "format", "text", "print what the command did as text or json")
}

const stackQueueLong = `The queue commands read and repair the dead messages of a queue a DB
service declares (docs/stack-model.md, section 8.8): a message is dead once
its handler has failed on every try its queue's retries allow, or its last
attempt's claim expired, and it stays in the queue's table, with its last
error, until an operator retries or drops it.

<queue> is the DB service and its @queue class, <db>.<Queue>:
shop-db.OrderPlaced. Each command resolves the environment from the
schemas, as stack plan does, without a build, and runs the operation with
the migration runner, superschematic-migrate, which holds the database
drivers: on the $SUPERSCHEMATIC_MIGRATE runner, else the one on PATH. A
queue's messages are claimed on Postgres alone, so a queue whose database
the environment places on another dialect is refused.

On the local target the operation runs against the Postgres container
stack dev runs the environment in, at the URL the environment resolves for
the queue's database, from another terminal. A cloud target's databases
are not reached yet: an environment on any other target is refused.`

func newStackQueueCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "List, retry and drop the dead messages of a queue",
		Long:  stackQueueLong,
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newStackQueueDeadCmd(a), newStackQueueRetryCmd(a), newStackQueueDropCmd(a))
	return cmd
}

func newStackQueueDeadCmd(a *app) *cobra.Command {
	flags := &queueFlags{}
	cmd := &cobra.Command{
		Use:   "dead <environment> <queue>",
		Short: "List the dead messages of a queue",
		Long: stackQueueLong + `

dead lists the queue's dead messages, the oldest death first, up to
--limit: each one's ID, how many times it was tried, when it died, the
error its last try failed with, and the message, as a JSON object of its
columns. It says how many there are in all.

Examples:
  superschematic stack queue dead Dev shop-db.OrderPlaced
  superschematic stack queue dead Dev shop-db.OrderPlaced --limit 10 --format json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.limit < 1 {
				return fmt.Errorf("--limit %d: list one or more", flags.limit)
			}
			return runStackQueue(cmd, a, flags, args[0], args[1], func(service, queue string) queueops.Request {
				return queueops.Request{Service: service, Queue: queue, Operation: queueops.Dead, Limit: flags.limit}
			})
		},
	}
	flags.register(cmd)
	cmd.Flags().IntVar(&flags.limit, "limit", queueops.DefaultLimit, "list at most this many dead messages")
	return cmd
}

func newStackQueueRetryCmd(a *app) *cobra.Command {
	flags := &queueFlags{}
	cmd := &cobra.Command{
		Use:   "retry <environment> <queue> [<id>...] [--all]",
		Short: "Make dead messages of a queue ready again",
		Long: stackQueueLong + `

retry makes the dead messages it names ready again, or every dead message
with --all: each is due at once, with no attempts, so its retries start
over, and its last error stays until its next failure replaces it. A
worker claims it as it claims a new message. When one of the IDs named is
no dead message, retry changes none of them and says what each such ID is.

Examples:
  superschematic stack queue retry Dev shop-db.OrderPlaced 3f2c8a10-5b1e-4c7a-9d2e-8f6b4a1c0e93
  superschematic stack queue retry Dev shop-db.OrderPlaced --all`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := queueIDs(args[2:], flags.all, "retry", "retries")
			if err != nil {
				return err
			}
			return runStackQueue(cmd, a, flags, args[0], args[1], func(service, queue string) queueops.Request {
				return queueops.Request{Service: service, Queue: queue, Operation: queueops.Retry, IDs: ids, All: flags.all}
			})
		},
	}
	flags.register(cmd)
	cmd.Flags().BoolVar(&flags.all, "all", false, "retry every dead message of the queue")
	return cmd
}

func newStackQueueDropCmd(a *app) *cobra.Command {
	flags := &queueFlags{}
	cmd := &cobra.Command{
		Use:   "drop <environment> <queue> [<id>...] [--all --yes]",
		Short: "Delete dead messages of a queue",
		Long: stackQueueLong + `

drop deletes the dead messages it names, or every dead message with --all,
which takes --yes as well, since the messages cannot be brought back. When
one of the IDs named is no dead message, drop deletes none of them and
says what each such ID is.

Examples:
  superschematic stack queue drop Dev shop-db.OrderPlaced 3f2c8a10-5b1e-4c7a-9d2e-8f6b4a1c0e93
  superschematic stack queue drop Dev shop-db.OrderPlaced --all --yes`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := queueIDs(args[2:], flags.all, "drop", "drops")
			if err != nil {
				return err
			}
			if flags.all && !flags.yes {
				return fmt.Errorf("drop --all deletes every dead message of %s in %s, which cannot be brought back: pass --yes with it to confirm", args[1], args[0])
			}
			return runStackQueue(cmd, a, flags, args[0], args[1], func(service, queue string) queueops.Request {
				return queueops.Request{Service: service, Queue: queue, Operation: queueops.Drop, IDs: ids, All: flags.all}
			})
		},
	}
	flags.register(cmd)
	cmd.Flags().BoolVar(&flags.all, "all", false, "drop every dead message of the queue (with --yes)")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm --all")
	return cmd
}

// queueIDs reads the IDs a retry or a drop names, in lower case and each
// once: some IDs, or none with --all.
func queueIDs(args []string, all bool, verb, verbs string) ([]string, error) {
	switch {
	case all && len(args) > 0:
		return nil, fmt.Errorf("%s --all %s every dead message; name no ID with it", verb, verbs)
	case !all && len(args) == 0:
		return nil, fmt.Errorf("name the IDs of the dead messages to %s, as stack queue dead lists them, or pass --all", verb)
	}
	var ids []string
	for _, arg := range args {
		id, err := queueops.NormalizeID(arg)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// queueRef splits <db>.<Queue>.
func queueRef(ref string) (service, queue string, err error) {
	i := strings.LastIndex(ref, ".")
	if i <= 0 || i == len(ref)-1 {
		return "", "", fmt.Errorf("queue %q: name it <db>.<Queue>, its DB service and its @queue class, such as shop-db.OrderPlaced", ref)
	}
	return ref[:i], ref[i+1:], nil
}

// runStackQueue resolves the environment, finds the queue and its
// database, and runs the request request builds with the migration runner
// on the database's target.
func runStackQueue(cmd *cobra.Command, a *app, flags *queueFlags, environment, ref string, request func(service, queue string) queueops.Request) error {
	if flags.format != "text" && flags.format != "json" {
		return fmt.Errorf("--format %q: want text or json", flags.format)
	}
	service, queue, err := queueRef(ref)
	if err != nil {
		return err
	}
	c, err := openDeployContext(cmd, a, &stackFlags{stackDir: flags.stackDir, namingPath: flags.namingPath}, environment, true)
	if err != nil {
		return err
	}
	defer c.close()
	if err := c.checkQueue(service, queue); err != nil {
		return err
	}
	req := request(service, queue)
	doc, err := queueops.Document(req)
	if err != nil {
		return err
	}
	spec, ok := c.reg.Provisioner(c.env.Provisioner)
	if !ok {
		return fmt.Errorf("environment %s names provisioner %q, which is not registered", c.env.Environment, c.env.Provisioner)
	}
	runner, ok := spec.Provisioner.(localQueueRunner)
	if !ok {
		return fmt.Errorf("provisioner %s runs no queue operation", c.env.Provisioner)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	out, err := runner.Queue(ctx, registry.ProvisionRequest{Environment: c.env, Dir: c.dir, OutputRoot: c.project.outputRoot}, service, doc)
	if err != nil {
		return err
	}
	result, err := queueops.ReadResult(req, out)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	if flags.format == "json" {
		_, err = w.Write(out)
		return err
	}
	return writeQueueText(w, environment, ref, result)
}

// checkQueue refuses a queue the environment cannot run the operation on:
// one no DB service of the stack declares, one on a database of another
// dialect than Postgres, and one in an environment on a target other than
// local, whose databases the command does not reach yet.
func (c *deployContext) checkQueue(service, queue string) error {
	var refs []string
	var declared []string
	found := false
	for _, svc := range c.services {
		if svc.Kind != ir.SchemaKindDB {
			continue
		}
		for _, q := range svc.Queues {
			refs = append(refs, svc.Name+"."+q)
		}
		if svc.Name == service {
			found, declared = true, svc.Queues
		}
	}
	queues := "none"
	if len(refs) > 0 {
		queues = strings.Join(refs, ", ")
	}
	switch {
	case !found:
		return fmt.Errorf("stack %s reaches no DB service %s (its queues: %s)", c.project.stack.Name, service, queues)
	case !slices.Contains(declared, queue):
		return fmt.Errorf("%s declares no queue %s (the stack's queues: %s)", service, queue, queues)
	}
	var host *ir.ResolvedDeployable
	for _, d := range c.env.Deployables {
		if d.Kind == ir.DeployableDatabase && slices.ContainsFunc(d.Services, func(ref ir.ServiceRef) bool { return ref.Name == service }) {
			host = d
		}
	}
	switch {
	case host == nil:
		return fmt.Errorf("environment %s hosts no database of %s", c.env.Environment, service)
	case host.Dialect != "" && host.Dialect != registry.SQLDialectPostgres:
		return fmt.Errorf("environment %s places %s on database %s, a %s database; a queue's messages are claimed on Postgres alone, and so are its dead ones read, retried and dropped (D53, amended)",
			c.env.Environment, service, host.Name, host.Dialect)
	case c.env.Target != local.Target:
		return fmt.Errorf("environment %s is on the %s target, whose databases stack queue does not reach yet: it runs on the %s target alone, against the Postgres stack dev runs. Reaching a cloud database, through the migration runner's job as a deploy's migrations reach it, is not built",
			c.env.Environment, c.env.Target, local.Target)
	}
	return nil
}

// writeQueueText prints what a queue operation did for a person.
func writeQueueText(w io.Writer, environment, ref string, r *queueops.Result) error {
	var b strings.Builder
	switch r.Operation {
	case queueops.Dead:
		if r.Total == 0 {
			fmt.Fprintf(&b, "Queue %s of %s has no dead messages.\n", ref, environment)
			break
		}
		fmt.Fprintf(&b, "Queue %s of %s: %s, the oldest death first.\n", ref, environment, messages(int(r.Total), "dead"))
		for _, m := range r.Messages {
			fmt.Fprintf(&b, "\n%s\n", m.ID)
			fmt.Fprintf(&b, "  attempts:   %d\n", m.Attempts)
			fmt.Fprintf(&b, "  died at:    %s\n", m.DiedAt)
			lastError := m.LastError
			if lastError == "" {
				lastError = "(none)"
			}
			fmt.Fprintf(&b, "  last error: %s\n", strings.ReplaceAll(lastError, "\n", "\n              "))
			var message bytes.Buffer
			if err := json.Compact(&message, m.Message); err != nil {
				return fmt.Errorf("message %s: %w", m.ID, err)
			}
			fmt.Fprintf(&b, "  message:    %s\n", message.String())
		}
		if shown := len(r.Messages); int64(shown) < r.Total {
			fmt.Fprintf(&b, "\nShowing %d of %d; --limit shows more.\n", shown, r.Total)
		}
	case queueops.Retry, queueops.Drop:
		if len(r.IDs) == 0 {
			fmt.Fprintf(&b, "Queue %s of %s has no dead messages to %s.\n", ref, environment, r.Operation)
			break
		}
		if r.Operation == queueops.Retry {
			fmt.Fprintf(&b, "Retried %s of %s in %s: each is ready, due now, with no attempts.\n", messages(len(r.IDs), "dead"), ref, environment)
		} else {
			fmt.Fprintf(&b, "Dropped %s of %s in %s.\n", messages(len(r.IDs), "dead"), ref, environment)
		}
		for _, id := range r.IDs {
			fmt.Fprintf(&b, "  %s\n", id)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// messages counts n messages: "1 dead message", "2 dead messages".
func messages(n int, adjective string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s message", adjective)
	}
	return fmt.Sprintf("%d %s messages", n, adjective)
}
