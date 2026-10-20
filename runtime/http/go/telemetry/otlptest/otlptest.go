// Package otlptest is an OTLP/HTTP trace receiver in the test's process
// (D67): an httptest server that takes the export requests a server, job or
// worker sends to OTEL_EXPORTER_OTLP_ENDPOINT, in protobuf, and keeps their
// spans for the test to read. The runtime's own tests use it, and so do a
// stack's tests, which point the deployables they run at its URL.
package otlptest

import (
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// The kinds a Span has, as OTLP numbers them.
const (
	KindInternal = int(tracepb.Span_SPAN_KIND_INTERNAL)
	KindServer   = int(tracepb.Span_SPAN_KIND_SERVER)
	KindClient   = int(tracepb.Span_SPAN_KIND_CLIENT)
	KindProducer = int(tracepb.Span_SPAN_KIND_PRODUCER)
	KindConsumer = int(tracepb.Span_SPAN_KIND_CONSUMER)
)

// StatusError is the status code of a span that failed.
const StatusError = int(tracepb.Status_STATUS_CODE_ERROR)

// Span is a span the receiver took, with its IDs in lower-case hex, its
// attributes and its resource's as their values' text.
type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Kind         int
	Status       int
	Attributes   map[string]string
	Resource     map[string]string
}

// Service is the service.name of the span's resource.
func (s Span) Service() string {
	return s.Resource["service.name"]
}

func (s Span) String() string {
	return fmt.Sprintf("%s %q (kind %d, trace %s, span %s, parent %s)", s.Service(), s.Name, s.Kind, s.TraceID, s.SpanID, s.ParentSpanID)
}

// Receiver is the receiver; URL is what OTEL_EXPORTER_OTLP_ENDPOINT names.
type Receiver struct {
	URL string

	mu       sync.Mutex
	spans    []Span
	requests int
	failures []string
}

// Start starts a receiver, which the test's cleanup closes.
func Start(t testing.TB) *Receiver {
	t.Helper()
	r := &Receiver{}
	server := httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(server.Close)
	r.URL = server.URL
	return r
}

// Spans are the spans taken so far, in the order they arrived.
func (r *Receiver) Spans() []Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.spans)
}

// Requests is how many export requests arrived.
func (r *Receiver) Requests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests
}

// Failures are the requests the receiver could not read, which a test
// expects none of.
func (r *Receiver) Failures() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.failures)
}

// Wait waits up to timeout for a span that match reports, and returns it;
// it fails the test, listing the spans taken, when none arrives.
func (r *Receiver) Wait(t testing.TB, timeout time.Duration, what string, match func(Span) bool) Span {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, s := range r.Spans() {
			if match(s) {
				return s
			}
		}
		if time.Now().After(deadline) {
			var got []string
			for _, s := range r.Spans() {
				got = append(got, s.String())
			}
			t.Fatalf("no span %s arrived within %s; the receiver took %d requests, failed %v, and these spans:\n%s",
				what, timeout, r.Requests(), r.Failures(), strings.Join(got, "\n"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *Receiver) serve(w http.ResponseWriter, req *http.Request) {
	spans, err := read(req)
	r.mu.Lock()
	r.requests++
	if err != nil {
		r.failures = append(r.failures, err.Error())
	}
	r.spans = append(r.spans, spans...)
	r.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(body)
}

// read decodes an export request's spans.
func read(req *http.Request) ([]Span, error) {
	if req.Method != http.MethodPost || req.URL.Path != "/v1/traces" {
		return nil, fmt.Errorf("%s %s is no trace export", req.Method, req.URL.Path)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/x-protobuf" {
		return nil, fmt.Errorf("content type %q is not application/x-protobuf", ct)
	}
	var body io.Reader = req.Body
	if req.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(req.Body)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		body = gz
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	var export coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(data, &export); err != nil {
		return nil, err
	}
	var spans []Span
	for _, rs := range export.GetResourceSpans() {
		resource := attributes(rs.GetResource().GetAttributes())
		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				spans = append(spans, Span{
					TraceID:      hex.EncodeToString(s.GetTraceId()),
					SpanID:       hex.EncodeToString(s.GetSpanId()),
					ParentSpanID: hex.EncodeToString(s.GetParentSpanId()),
					Name:         s.GetName(),
					Kind:         int(s.GetKind()),
					Status:       int(s.GetStatus().GetCode()),
					Attributes:   attributes(s.GetAttributes()),
					Resource:     resource,
				})
			}
		}
	}
	return spans, nil
}

// attributes renders each attribute's value as text.
func attributes(kvs []*commonpb.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		v := kv.GetValue()
		switch v.GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			out[kv.GetKey()] = v.GetStringValue()
		case *commonpb.AnyValue_IntValue:
			out[kv.GetKey()] = fmt.Sprint(v.GetIntValue())
		case *commonpb.AnyValue_BoolValue:
			out[kv.GetKey()] = fmt.Sprint(v.GetBoolValue())
		case *commonpb.AnyValue_DoubleValue:
			out[kv.GetKey()] = fmt.Sprint(v.GetDoubleValue())
		default:
			out[kv.GetKey()] = v.String()
		}
	}
	return out
}
