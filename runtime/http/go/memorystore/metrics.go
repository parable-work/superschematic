package memorystore

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The metrics a store records (D75), through the process's OpenTelemetry
// meter provider: otel's global one unless a store is given its own. Each
// carries the store's prefix and a declaration's name, never a part or a
// full name. A Disabled store records nothing.
const (
	// MetricConnectionUp is a gauge, 1 while the store is connected and 0
	// while it is not, by store.
	MetricConnectionUp = "memorystore.connection.up"

	// MetricPublish counts publishes, by store, declaration and outcome.
	MetricPublish = "memorystore.publish"

	// MetricReceive counts the messages the process received, by store and
	// declaration, each once however many local handlers it woke.
	MetricReceive = "memorystore.receive"

	// MetricDecodeFailure counts the messages a typed channel's handler
	// dropped because they did not decode, by store and declaration.
	MetricDecodeFailure = "memorystore.decode_failure"

	// MetricKey counts key operations, by store, declaration, op and
	// outcome.
	MetricKey = "memorystore.key"
)

// The attributes of the metrics.
const (
	AttrStore       = "store"
	AttrDeclaration = "declaration"
	AttrOutcome     = "outcome"
	AttrOp          = "op"
)

// The outcomes of a publish or a key operation.
const (
	// OutcomeOK is a call the server answered.
	OutcomeOK = "ok"
	// OutcomeUnavailable is a call refused while the store is unavailable,
	// or one whose connection failed.
	OutcomeUnavailable = "unavailable"
	// OutcomeTooLarge is a message over MaxMessageBytes, refused before it
	// was sent.
	OutcomeTooLarge = "too_large"
	// OutcomeError is a reply the server refused.
	OutcomeError = "error"
	// OutcomeCanceled is a call whose context ended first.
	OutcomeCanceled = "canceled"
)

// The ops of a key operation.
const (
	OpIncrement = "increment"
	OpCount     = "count"
	OpClaim     = "claim"
	OpDelete    = "delete"
)

// meterScope is the instrumentation scope of the store's metrics.
const meterScope = "github.com/parable-work/superschematic/runtime/http/go/memorystore"

// Metrics records one store's metrics. Its methods are safe for
// concurrent use; a nil *Metrics records nothing.
type Metrics struct {
	store        attribute.KeyValue
	publish      metric.Int64Counter
	receive      metric.Int64Counter
	decode       metric.Int64Counter
	key          metric.Int64Counter
	registration metric.Registration
	mu           sync.Mutex
}

// NewMetrics registers the metrics of the store whose prefix is store on
// provider's meter, nil for otel's global provider, and observes up for
// the connection gauge until Close.
func NewMetrics(provider metric.MeterProvider, store string, up func() bool) *Metrics {
	if provider == nil {
		provider = otel.GetMeterProvider()
	}
	meter := provider.Meter(meterScope)
	m := &Metrics{store: attribute.String(AttrStore, store)}
	// An instrument the meter cannot make is otel's no-op one, which the
	// error comes with: a metric is never a reason to refuse a store.
	m.publish, _ = meter.Int64Counter(MetricPublish,
		metric.WithDescription("Messages published to the memory store, by store, declaration and outcome"),
		metric.WithUnit("{message}"))
	m.receive, _ = meter.Int64Counter(MetricReceive,
		metric.WithDescription("Messages this process received from the memory store, by store and declaration"),
		metric.WithUnit("{message}"))
	m.decode, _ = meter.Int64Counter(MetricDecodeFailure,
		metric.WithDescription("Received messages a typed channel dropped because they did not decode, by store and declaration"),
		metric.WithUnit("{message}"))
	m.key, _ = meter.Int64Counter(MetricKey,
		metric.WithDescription("Key operations on the memory store, by store, declaration, op and outcome"),
		metric.WithUnit("{operation}"))
	gauge, err := meter.Int64ObservableGauge(MetricConnectionUp,
		metric.WithDescription("1 while this process is connected to the memory store, 0 while it is not"),
		metric.WithUnit("1"))
	if err == nil && up != nil {
		m.registration, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			value := int64(0)
			if up() {
				value = 1
			}
			o.ObserveInt64(gauge, value, metric.WithAttributes(m.store))
			return nil
		}, gauge)
	}
	return m
}

// Publish counts a publish of declaration with its outcome.
func (m *Metrics) Publish(ctx context.Context, declaration, outcome string) {
	if m == nil || m.publish == nil {
		return
	}
	m.publish.Add(ctx, 1, metric.WithAttributes(m.store, attribute.String(AttrDeclaration, declaration), attribute.String(AttrOutcome, outcome)))
}

// Receive counts a message of declaration the process received.
func (m *Metrics) Receive(ctx context.Context, declaration string) {
	if m == nil || m.receive == nil {
		return
	}
	m.receive.Add(ctx, 1, metric.WithAttributes(m.store, attribute.String(AttrDeclaration, declaration)))
}

// DecodeFailure counts a message of declaration that did not decode.
func (m *Metrics) DecodeFailure(ctx context.Context, declaration string) {
	if m == nil || m.decode == nil {
		return
	}
	m.decode.Add(ctx, 1, metric.WithAttributes(m.store, attribute.String(AttrDeclaration, declaration)))
}

// Key counts a key operation op on declaration with its outcome.
func (m *Metrics) Key(ctx context.Context, declaration, op, outcome string) {
	if m == nil || m.key == nil {
		return
	}
	m.key.Add(ctx, 1, metric.WithAttributes(m.store, attribute.String(AttrDeclaration, declaration), attribute.String(AttrOp, op), attribute.String(AttrOutcome, outcome)))
}

// Close stops observing the connection gauge.
func (m *Metrics) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.registration != nil {
		_ = m.registration.Unregister()
		m.registration = nil
	}
}

// decodeFailureRecorder is a store that counts its typed channels' decode
// failures: the in-process store and a networked one built on Metrics.
type decodeFailureRecorder interface {
	DecodeFailed(ctx context.Context, declaration string)
}
