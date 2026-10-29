package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/queueops"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack/local"
	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
)

// `superschematic stack queue <dead|retry|drop> <environment> <db>.<Queue>`
// (docs/stack-model.md, sections 8.8 and 11.1): an operator's view of a
// queue's dead messages, which it lists, makes ready again or deletes. The
// migration runner runs each operation on the queue's database, since it
// holds the drivers (D27), from a document that carries the statements
// sqlutil writes for the queue's table (internal/queueops). On the local
// target it runs against the Postgres container stack dev runs; on a cloud
// target, through the target's queue runner, on the deployed run's
// database (D68, amended): on gcp an execution of the migration job, which
// hands the result back through the state bucket; on cloudflare the
// migration runner on this machine, on the origin of a database on
// Hyperdrive, as the database's migrations run (D96).

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
	params     []string
	timeout    time.Duration
	limit      int
	all        bool
	yes        bool
}

// defaultQueueTimeout is how long a queue command waits for its operation:
// on a cloud target, an execution of the migration job, which may first
// build the runner's image.
const defaultQueueTimeout = 30 * time.Minute

func (f *queueFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.stackDir, "stack", "", "the Stack service directory (default: the working directory when it is one, else the one Stack service under ./schemas/services)")
	cmd.Flags().StringVar(&f.namingPath, "naming", "", "naming config file (default <stack>/../../superschematic.toml)")
	cmd.Flags().StringVar(&f.format, "format", "text", "print what the command did as text or json")
	cmd.Flags().StringArrayVar(&f.params, "param", nil, "a parameter's value for this run of a parameterized environment: <name>=<value> (repeatable)")
	cmd.Flags().DurationVar(&f.timeout, "timeout", defaultQueueTimeout, "give up waiting for the operation after this long")
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
drivers: on the $SUPERSCHEMATIC_MIGRATE runner, else the one on PATH. The
operator's statements run on Postgres alone: a queue whose database the
environment places on SQLite, where workers claim too (D53, amended), is
refused, since its dead messages are not read there yet.

On the local target the operation runs against the Postgres container
stack dev runs the environment in, at the URL the environment resolves for
the queue's database, from another terminal.

On a cloud target it runs on the database of the deployed run, --param
naming a member of a parameterized environment, through the target's
queue runner. On gcp that is an execution of the stack's migration job,
as the last deploy left it, which the command does not change: deploy
with this release first. The runner reaches Cloud SQL as the migrator and
writes what it did to an object of the state bucket under a name no one
can guess, which it reserves before the queue changes, which the command
reads once and deletes, and which the bucket's lifecycle deletes after a
day when the command does not. A deleted result stays restorable for the
bucket's soft-delete window, seven days by default. A listing's messages
may hold personal data, so they are never written to the execution's
logs. On cloudflare the runner runs on this machine, on the origin of
the queue's database, a Postgres outside Cloudflare reached through
Hyperdrive, with the connection string $HYPERDRIVE_ORIGIN_<DB> holds
(HYPERDRIVE_ORIGIN_SHOP_DB for shop-db), as the deploy's migrations do:
it first reads the database's Hyperdrive config, with the token in
$CLOUDFLARE_API_TOKEN, and refuses a connection string whose host, port,
database or user is not the config's. A target with no queue runner is
refused, as is a run that was never deployed. --timeout bounds the wait;
an execution it stops waiting for may still finish.`

func newStackQueueCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "List, retry and drop the dead messages of a queue, and backfill it",
		Long:  stackQueueLong,
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newStackQueueDeadCmd(a), newStackQueueRetryCmd(a), newStackQueueDropCmd(a), newStackQueueBackfillCmd(a))
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
	if flags.timeout <= 0 {
		return fmt.Errorf("--timeout %s: wait for some time", flags.timeout)
	}
	service, queue, err := queueRef(ref)
	if err != nil {
		return err
	}
	params, err := parseParams(flags.params)
	if err != nil {
		return err
	}
	c, err := openDeployContext(cmd, a, &stackFlags{stackDir: flags.stackDir, namingPath: flags.namingPath}, environment, true)
	if err != nil {
		return err
	}
	defer c.close()
	database, err := c.checkQueue(service, queue)
	if err != nil {
		return err
	}
	req := request(service, queue)
	req.Key, req.Table = c.queueKey(service, queue), c.queueTable(service, queue)
	if req.Operation == queueops.Backfill {
		if req.Backfills, err = c.queueBackfills(service, queue); err != nil {
			return err
		}
	}
	doc, err := queueops.Document(req)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, flags.timeout)
	defer cancel()
	var out []byte
	if c.env.Target == local.Target {
		out, err = runLocalQueue(ctx, c, service, doc)
	} else {
		out, err = stackdeploy.RunQueue(ctx, stackdeploy.RunQueueOptions{
			Options:  c.options(cmd, params),
			Database: database,
			Service:  service,
			Document: doc,
		})
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("stack queue stopped waiting after --timeout %s: %w", flags.timeout, err)
		}
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

// runLocalQueue runs a queue document with the local provisioner, against
// the Postgres container stack dev runs.
func runLocalQueue(ctx context.Context, c *deployContext, service string, doc []byte) ([]byte, error) {
	spec, ok := c.reg.Provisioner(c.env.Provisioner)
	if !ok {
		return nil, fmt.Errorf("environment %s names provisioner %q, which is not registered", c.env.Environment, c.env.Provisioner)
	}
	runner, ok := spec.Provisioner.(localQueueRunner)
	if !ok {
		return nil, fmt.Errorf("provisioner %s runs no queue operation", c.env.Provisioner)
	}
	return runner.Queue(ctx, registry.ProvisionRequest{Environment: c.env, Dir: c.dir, OutputRoot: c.project.outputRoot}, service, doc)
}

// checkQueue refuses a queue the environment cannot run the operation on:
// one no DB service of the stack declares, and one on a database of
// another dialect than Postgres. It returns the database deployable that
// hosts the queue's DB service.
func (c *deployContext) checkQueue(service, queue string) (string, error) {
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
		return "", fmt.Errorf("stack %s reaches no DB service %s (its queues: %s)", c.project.stack.Name, service, queues)
	case !slices.Contains(declared, queue):
		return "", fmt.Errorf("%s declares no queue %s (the stack's queues: %s)", service, queue, queues)
	}
	var host *ir.ResolvedDeployable
	for _, d := range c.env.Deployables {
		if d.Kind == ir.DeployableDatabase && slices.ContainsFunc(d.Services, func(ref ir.ServiceRef) bool { return ref.Name == service }) {
			host = d
		}
	}
	switch {
	case host == nil:
		return "", fmt.Errorf("environment %s hosts no database of %s", c.env.Environment, service)
	case host.Dialect != "" && host.Dialect != registry.SQLDialectPostgres:
		return "", fmt.Errorf("environment %s places %s on database %s, a %s database; stack queue reads, retries and drops a queue's dead messages on Postgres alone, and on SQLite it is not built (D68)",
			c.env.Environment, service, host.Name, host.Dialect)
	}
	return host.Name, nil
}

// queueKey is the quoted key column of the queue of the DB service when it
// keeps one ready message per key (D80), else "".
func (c *deployContext) queueKey(service, queue string) string {
	for _, svc := range c.services {
		if svc.Name == service {
			return svc.QueueKeys[queue]
		}
	}
	return ""
}

// queueTable is the table of service's queue, as its class names it
// (D85), or "" when the service is not among the stack's.
func (c *deployContext) queueTable(service, queue string) string {
	for _, svc := range c.services {
		if svc.Name == service {
			return svc.QueueTables[queue]
		}
	}
	return ""
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
	case queueops.Backfill:
		writeBackfillText(&b, environment, ref, r)
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
