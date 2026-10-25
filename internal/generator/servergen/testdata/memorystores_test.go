package main

// This test is copied into the entrypoint module of a server whose APIs
// list a memory store, beside the memorystores.go the build wrote, by
// TestMemoryStoresOnRedis (D75). The tests against a real server read
// SUPERSCHEMATIC_TEST_MEMORY_STORE, a redis:// URL of a loopback host and
// a database other than 0, so they never touch a running stack dev's keys,
// and skip when it is unset. The others run against servers of their own:
// a fake one that refuses CLIENT, over TLS with auth iam and a fake
// metadata server, and a proxy that goes away.

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/memorystore"
	"github.com/parable-work/superschematic/runtime/http/go/stackconfig"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// testServerURL is SUPERSCHEMATIC_TEST_MEMORY_STORE, checked: a redis URL
// of a loopback host and a database other than 0.
func testServerURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("SUPERSCHEMATIC_TEST_MEMORY_STORE")
	if raw == "" {
		t.Skip("SUPERSCHEMATIC_TEST_MEMORY_STORE is unset")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "redis" {
		t.Fatalf("SUPERSCHEMATIC_TEST_MEMORY_STORE is no redis:// URL")
	}
	if ip := net.ParseIP(u.Hostname()); u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		t.Fatalf("SUPERSCHEMATIC_TEST_MEMORY_STORE names %s, which is no loopback host", u.Hostname())
	}
	db, err := strconv.Atoi(strings.TrimPrefix(u.Path, "/"))
	if err != nil || db == 0 {
		t.Fatalf("SUPERSCHEMATIC_TEST_MEMORY_STORE names database %q; a test uses a database other than 0", u.Path)
	}
	return raw
}

var fast = memorystore.Timings{Interval: 50 * time.Millisecond, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, ReceiveIdle: 200 * time.Millisecond}

func observed() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.DebugLevel)
	return zap.New(core), logs
}

func ref(t *testing.T, declaration string, parts ...string) memorystore.Ref {
	t.Helper()
	name, err := memorystore.Name(declaration, parts...)
	if err != nil {
		t.Fatal(err)
	}
	return memorystore.Ref{Declaration: declaration, Name: name}
}

// TestMemoryStoreOnRedis: on a real server, keys count with their TTL,
// claim once and delete; a message reaches every local handler over one
// subscription; a member's names carry its prefix; over 64 KiB is
// refused; and a closed store refuses at once.
func TestMemoryStoreOnRedis(t *testing.T) {
	raw := testServerURL(t)
	ctx := context.Background()
	store := "shop-cache-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	logger, logs := observed()
	s, err := openRedisStore(ctx, logger, stackconfig.MemoryStore{Store: store, URL: raw, Auth: stackconfig.AuthNone, Member: "pr-12"}, fast, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Available() || s.Name() != store+"@pr-12" {
		t.Fatalf("Available = %v, Name = %s", s.Available(), s.Name())
	}
	opts, _ := redis.ParseURL(raw)
	raw2 := redis.NewClient(opts)
	defer raw2.Close()

	views := ref(t, "views", "p1")
	for want := int64(1); want <= 2; want++ {
		if n, err := s.Increment(ctx, views, time.Minute); err != nil || n != want {
			t.Fatalf("Increment = %d, %v", n, err)
		}
	}
	full := store + "@pr-12:views:p1"
	if ttl := raw2.PTTL(ctx, full).Val(); ttl <= 50*time.Second || ttl > time.Minute {
		t.Errorf("PTTL of %s = %s, want about a minute, set by the first increment", full, ttl)
	}
	if n, err := s.Count(ctx, views); err != nil || n != 2 {
		t.Errorf("Count = %d, %v", n, err)
	}
	if n, err := s.Count(ctx, ref(t, "views", "absent")); err != nil || n != 0 {
		t.Errorf("Count of an absent key = %d, %v", n, err)
	}
	claim := ref(t, "receipt-sent", "o1")
	first, err1 := s.Claim(ctx, claim, time.Minute)
	second, err2 := s.Claim(ctx, claim, time.Minute)
	if !first || second || err1 != nil || err2 != nil {
		t.Errorf("claims = %v %v, %v %v", first, second, err1, err2)
	}
	if err := s.Delete(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Claim(ctx, claim, time.Minute); !again {
		t.Error("a claim after Delete is false")
	}

	changed := ref(t, "stock-changed", "p1")
	heard := make(chan string, 4)
	a, err := s.Subscribe(ctx, changed, func(_ context.Context, b []byte) { heard <- "a" + string(b) })
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Subscribe(ctx, changed, func(_ context.Context, b []byte) { heard <- "b" + string(b) })
	if err != nil {
		t.Fatal(err)
	}
	if n := raw2.PubSubNumSub(ctx, store+"@pr-12:stock-changed:p1").Val()[store+"@pr-12:stock-changed:p1"]; n != 1 {
		t.Errorf("subscribers on the server = %d, want the process's one", n)
	}
	if err := s.Publish(ctx, changed, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	got := []string{receive(t, heard), receive(t, heard)}
	if got[0] != "a{}" || got[1] != "b{}" {
		t.Errorf("handlers heard %v", got)
	}
	_ = a.Close()
	_ = b.Close()
	if err := s.Publish(ctx, changed, make([]byte, memorystore.MaxMessageBytes+1)); err == nil || errors.Is(err, memorystore.ErrUnavailable) {
		t.Errorf("a message over 64 KiB: %v", err)
	}
	_ = raw2.Del(ctx, full, store+"@pr-12:receipt-sent:o1")
	_ = s.Close()
	start := time.Now()
	if _, err := s.Increment(ctx, views, time.Minute); !errors.Is(err, memorystore.ErrUnavailable) || time.Since(start) > 50*time.Millisecond {
		t.Errorf("Increment after Close = %v in %s", err, time.Since(start))
	}
	checkNoSecret(t, logs, raw)
}

// TestMemoryStoreReconnects: a store whose server goes away goes
// unavailable and refuses at once, but holds a subscription made then;
// it comes back with both subscriptions, which go-redis subscribes again.
func TestMemoryStoreReconnects(t *testing.T) {
	raw := testServerURL(t)
	ctx := context.Background()
	u, _ := url.Parse(raw)
	p := newProxy(t, u.Host)
	defer p.close()
	proxied := "redis://" + p.addr() + u.Path
	store := "shop-cache-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	logger, logs := observed()
	s, err := openRedisStore(ctx, logger, stackconfig.MemoryStore{Store: store, URL: proxied, Auth: stackconfig.AuthNone}, fast, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	heard := make(chan string, 8)
	sub, err := s.Subscribe(ctx, ref(t, "stock-changed", "p1"), func(_ context.Context, b []byte) { heard <- "p1" + string(b) })
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	p.stop()
	waitFor(t, func() bool { return !s.Available() })
	start := time.Now()
	if _, err := s.Increment(ctx, ref(t, "views", "p1"), time.Minute); !errors.Is(err, memorystore.ErrUnavailable) || time.Since(start) > 50*time.Millisecond {
		t.Errorf("Increment while down = %v in %s, want ErrUnavailable at once", err, time.Since(start))
	}
	// A subscription made while the server is down is held, at once, and
	// subscribed when it returns, as the TypeScript and Rust runtimes hold
	// one.
	start = time.Now()
	held, err := s.Subscribe(ctx, ref(t, "stock-changed", "p2"), func(_ context.Context, b []byte) { heard <- "p2" + string(b) })
	if err != nil || time.Since(start) > 50*time.Millisecond {
		t.Fatalf("Subscribe while down = %v in %s, want a held subscription at once", err, time.Since(start))
	}
	defer held.Close()
	p.start()
	waitFor(t, func() bool { return s.Available() })
	// The subscriber reconnects on its own schedule: publish until both
	// subscriptions, the one made while the server was up and the one
	// made while it was down, hear one.
	deadline := time.Now().Add(10 * time.Second)
	got := map[string]bool{}
	for !got["p1"] || !got["p2"] {
		for _, part := range []string{"p1", "p2"} {
			if err := s.Publish(ctx, ref(t, "stock-changed", part), []byte(`{"n":1}`)); err != nil && !errors.Is(err, memorystore.ErrUnavailable) {
				t.Fatal(err)
			}
		}
		select {
		case m := <-heard:
			got[strings.TrimSuffix(m, `{"n":1}`)] = true
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("in 10s after the server came back the subscriptions heard %v", got)
		}
	}
	if logs.FilterMessage("the memory store is unavailable; it reconnects in the background").Len() == 0 || logs.FilterMessage("the memory store is connected").Len() == 0 {
		t.Errorf("the state's changes were not logged: %v", logs.All())
	}
	checkNoSecret(t, logs, proxied)
}

// TestMemoryStoreDownAtOpen: a store whose server is not there opens
// within the ping's second, unavailable, and refuses every call at once
// but Subscribe, which holds the subscription; once closed it refuses
// that too.
func TestMemoryStoreDownAtOpen(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	start := time.Now()
	s, err := openRedisStore(context.Background(), zap.NewNop(), stackconfig.MemoryStore{Store: "shop-cache", URL: "redis://" + addr + "/3", Auth: stackconfig.AuthNone}, fast, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Available() || time.Since(start) > 1500*time.Millisecond {
		t.Errorf("Available = %v after %s", s.Available(), time.Since(start))
	}
	start = time.Now()
	if err := s.Publish(context.Background(), ref(t, "stock-changed", "p1"), []byte("{}")); !errors.Is(err, memorystore.ErrUnavailable) || time.Since(start) > 50*time.Millisecond {
		t.Errorf("Publish = %v in %s", err, time.Since(start))
	}
	// Subscribe holds the subscription for when the server comes, at once.
	start = time.Now()
	sub, err := s.Subscribe(context.Background(), ref(t, "stock-changed", "p1"), func(context.Context, []byte) {})
	if err != nil || time.Since(start) > 50*time.Millisecond {
		t.Errorf("Subscribe = %v in %s, want a held subscription at once", err, time.Since(start))
	} else {
		_ = sub.Close()
	}
	_ = s.Close()
	if _, err := s.Subscribe(context.Background(), ref(t, "stock-changed", "p1"), func(context.Context, []byte) {}); !errors.Is(err, memorystore.ErrUnavailable) {
		t.Errorf("Subscribe after Close = %v", err)
	}
}

// TestMemoryStoreSendsNoClientCommand: against a server that refuses
// CLIENT and HELLO, as a managed one may, the store connects, pings and
// counts, and never sends CLIENT; with auth iam over TLS each new
// connection sends the metadata server's token as user default's password,
// verified against the connection's CA, and no log line holds the token.
func TestMemoryStoreSendsNoClientCommand(t *testing.T) {
	ctx := context.Background()
	for _, iam := range []bool{false, true} {
		t.Run(fmt.Sprintf("iam=%v", iam), func(t *testing.T) {
			f := newFakeServer(t, iam)
			defer f.close()
			conn := stackconfig.MemoryStore{Store: "shop-cache", URL: "redis://" + f.addr() + "/0", Auth: stackconfig.AuthNone}
			metadataHost := ""
			const token = "ya29.a-token-no-log-holds"
			if iam {
				meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != memorystore.MetadataTokenPath || r.Header.Get("Metadata-Flavor") != "Google" {
						http.Error(w, "no", http.StatusForbidden)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "expires_in": 3600, "token_type": "Bearer"})
				}))
				defer meta.Close()
				metadataHost = strings.TrimPrefix(meta.URL, "http://")
				conn = stackconfig.MemoryStore{Store: "shop-cache", URL: "rediss://" + f.addr() + "/0", CA: f.caPEM, Auth: stackconfig.AuthIAM}
			}
			logger, logs := observed()
			s, err := openRedisStore(ctx, logger, conn, fast, metadataHost)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if !s.Available() {
				t.Fatalf("not available against the fake server: %v", logs.All())
			}
			if n, err := s.Increment(ctx, ref(t, "views", "p1"), time.Minute); err != nil || n != 1 {
				t.Errorf("Increment = %d, %v", n, err)
			}
			if f.saw("CLIENT") {
				t.Errorf("the store sent CLIENT: %v", f.commands())
			}
			if iam {
				if auth := f.auths(); len(auth) == 0 || auth[0] != "default "+token {
					t.Errorf("AUTH = %v, want default and the token", auth)
				}
				checkNoSecret(t, logs, token)
			} else if len(f.auths()) != 0 {
				t.Errorf("auth none sent AUTH %v", f.auths())
			}
		})
	}
}

// TestMemoryStoreRefusals: a connection the store cannot use is refused at
// open, and openMemoryStore runs disabled after one error line; an unset
// field runs disabled after one info line.
func TestMemoryStoreRefusals(t *testing.T) {
	for name, conn := range map[string]stackconfig.MemoryStore{
		"a CA that is no PEM":        {Store: "shop-cache", URL: "rediss://127.0.0.1:1/0", CA: "-----BEGIN CERTIFICATE-----\nnot one\n-----END CERTIFICATE-----\n", Auth: stackconfig.AuthNone},
		"iam over redis":             {Store: "shop-cache", URL: "redis://127.0.0.1:1/0", Auth: stackconfig.AuthIAM},
		"a store that is no segment": {Store: "shop:cache", URL: "redis://127.0.0.1:1/0", Auth: stackconfig.AuthNone},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := openRedisStore(context.Background(), zap.NewNop(), conn, fast, ""); err == nil {
				t.Error("opened")
			}
			logger, logs := observed()
			s := openMemoryStore(context.Background(), logger, "shop-cache", "SHOP_CACHE_MEMORY_STORE", conn)
			if !memorystore.IsDisabled(s) || logs.FilterLevelExact(zap.ErrorLevel).Len() != 1 {
				t.Errorf("store %T, logs %v", s, logs.All())
			}
		})
	}
	logger, logs := observed()
	if s := openMemoryStore(context.Background(), logger, "shop-cache", "SHOP_CACHE_MEMORY_STORE", stackconfig.MemoryStore{}); !memorystore.IsDisabled(s) || s.Name() != "shop-cache" {
		t.Errorf("an unset field opened %T %s", s, s.Name())
	}
	if logs.FilterMessage("the memory store is disabled: its field is unset").Len() != 1 {
		t.Errorf("logs = %v", logs.All())
	}
}

func receive(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no message in 5s")
		return ""
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("the condition did not hold in 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// checkNoSecret fails when a log line, or one of its fields, holds secret.
func checkNoSecret(t *testing.T, logs *observer.ObservedLogs, secret string) {
	t.Helper()
	for _, e := range logs.All() {
		if strings.Contains(e.Message, secret) || strings.Contains(fmt.Sprint(e.ContextMap()), secret) {
			t.Errorf("a log line holds a secret: %s %v", e.Message, e.ContextMap())
		}
	}
}

// proxy forwards TCP connections to a server until stopped, and again once
// started.
type proxy struct {
	t      *testing.T
	target string
	port   string
	mu     sync.Mutex
	l      net.Listener
	conns  []net.Conn
}

func newProxy(t *testing.T, target string) *proxy {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	p := &proxy{t: t, target: target, port: port}
	p.serve(l)
	return p
}

func (p *proxy) addr() string { return "127.0.0.1:" + p.port }

func (p *proxy) serve(l net.Listener) {
	p.mu.Lock()
	p.l = l
	p.mu.Unlock()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", p.target)
			if err != nil {
				_ = c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, up)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
			go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
		}
	}()
}

func (p *proxy) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.l.Close()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

func (p *proxy) start() {
	l, err := net.Listen("tcp", p.addr())
	if err != nil {
		p.t.Fatal(err)
	}
	p.serve(l)
}

func (p *proxy) close() { p.stop() }

// fakeServer speaks enough RESP for a store: it refuses HELLO and CLIENT,
// takes AUTH, PING, PUBLISH, MULTI, INCR, PEXPIRE, EXEC, GET, SET and DEL,
// and records every command.
type fakeServer struct {
	l     net.Listener
	caPEM string
	mu    sync.Mutex
	cmds  [][]string
	count map[string]int64
}

func newFakeServer(t *testing.T, useTLS bool) *fakeServer {
	t.Helper()
	f := &fakeServer{count: map[string]int64{}}
	var err error
	if useTLS {
		cert, caPEM := selfSigned(t)
		f.caPEM = caPEM
		f.l, err = tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	} else {
		f.l, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := f.l.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeServer) addr() string { return f.l.Addr().String() }
func (f *fakeServer) close()       { _ = f.l.Close() }

func (f *fakeServer) commands() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.cmds...)
}

func (f *fakeServer) saw(name string) bool {
	for _, c := range f.commands() {
		if strings.EqualFold(c[0], name) {
			return true
		}
	}
	return false
}

func (f *fakeServer) auths() []string {
	var out []string
	for _, c := range f.commands() {
		if strings.EqualFold(c[0], "AUTH") {
			out = append(out, strings.Join(c[1:], " "))
		}
	}
	return out
}

func (f *fakeServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	var queued []string
	inMulti := false
	for {
		cmd, err := readCommand(r)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.cmds = append(f.cmds, cmd)
		f.mu.Unlock()
		name := strings.ToUpper(cmd[0])
		reply := ""
		switch name {
		case "HELLO", "CLIENT":
			reply = "-ERR unknown command '" + cmd[0] + "'\r\n"
		case "AUTH", "SELECT":
			reply = "+OK\r\n"
		case "PING":
			reply = "+PONG\r\n"
		case "PUBLISH":
			reply = ":0\r\n"
		case "MULTI":
			inMulti, queued = true, nil
			reply = "+OK\r\n"
		case "EXEC":
			inMulti = false
			reply = fmt.Sprintf("*%d\r\n%s", len(queued), strings.Join(queued, ""))
		case "INCR":
			f.mu.Lock()
			f.count[cmd[1]]++
			n := f.count[cmd[1]]
			f.mu.Unlock()
			reply = fmt.Sprintf(":%d\r\n", n)
		case "PEXPIRE":
			reply = ":1\r\n"
		case "GET", "SET":
			reply = "$-1\r\n"
		case "DEL":
			reply = ":1\r\n"
		default:
			reply = "-ERR unknown command '" + cmd[0] + "'\r\n"
		}
		if inMulti && name != "MULTI" {
			queued = append(queued, reply)
			reply = "+QUEUED\r\n"
		}
		if _, err := io.WriteString(c, reply); err != nil {
			return
		}
	}
}

// readCommand reads one RESP array of bulk strings.
func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("not an array: %q", line)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, err
	}
	out := make([]string, n)
	for i := range out {
		head, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(head[1:]))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		out[i] = string(buf[:size])
	}
	return out, nil
}

// selfSigned is a certificate for 127.0.0.1 that is its own CA, and its
// PEM.
func selfSigned(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake memory store"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
