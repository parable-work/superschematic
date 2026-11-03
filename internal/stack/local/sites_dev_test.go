package local

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestADevSiteHotReloadsBehindItsProxy runs a site's dev server as stack
// dev does (D82): Bun's own, `bun --port=$PORT ./index.html`, the site's
// dev script, in its package after the workspace's install, with PORT, the
// site's origin and the URL of the API it calls in its environment. The
// provisioner waits until it answers on localhost, where Bun listens on
// IPv6 alone, and serves the site's port from the proxy: the page Bun
// serves, the config with the API on the site's origin, and the API under
// /api/<service>. Through the proxy the page's hot-reload socket upgrades,
// and an edit of the page's script reaches it as Bun's hot update, which
// carries the edited code. Destroy stops the dev server, and a dev server
// that exits stops the environment.
//
// The socket speaks Bun's hot-reload protocol as the pinned Bun's client
// does (tools.env's BUN_VERSION): a version message on connect, "she" to
// subscribe to the hot updates and "n" with the page's path, then a 'u'
// message for each update.
func TestADevSiteHotReloadsBehindItsProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: a site's dev server runs on Bun")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skipf("bun is not available: %v", err)
	}
	repo := t.TempDir()
	outputRoot := filepath.Join(repo, "schemas", "dist")
	siteDir := filepath.Join(repo, "web", "shop-web")
	for name, content := range map[string]string{
		filepath.Join(outputRoot, "package.json"): `{"name": "workspace", "private": true, "workspaces": []}` + "\n",
		filepath.Join(siteDir, "package.json"): `{
  "name": "shop-web-site",
  "private": true,
  "scripts": {
    "dev": "echo dev PORT=$PORT SITE=$SUPERSCHEMATIC_SITE_URL API=$SHOP_API_URL && bun --port=$PORT ./index.html"
  }
}
`,
		filepath.Join(siteDir, "index.html"):     `<!doctype html><html><head><title>acme shop</title></head><body><main id="app"></main><script type="module" src="./src/main.ts"></script></body></html>` + "\n",
		filepath.Join(siteDir, "src", "main.ts"): `document.getElementById("app")!.textContent = "first edit";` + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"path": %q}`, r.URL.Path)
	}))
	t.Cleanup(api.Close)

	site := &Site{
		ID: "shop-web.site", Deployable: "shop-web", Name: "shop-web",
		Dir: "web/shop-web", Build: "build", Output: "dist",
		Port:   freeLoopbackPort(t),
		Config: map[string]any{"apis": map[string]any{"shop-api": map[string]any{"url": api.URL}}},
		Dev:    &SiteDev{Command: "dev", Port: freeLoopbackPort(t)},
	}
	site.URL = ServerURL(site.Port)
	out := &syncBuffer{}
	p := &Provisioner{Out: out, ReadyTimeout: time.Minute}
	env := &ir.ResolvedEnvironment{Stack: "site-stack", Environment: "Dev"}
	req := registry.ProvisionRequest{Environment: env, OutputRoot: outputRoot, RepositoryRoot: repo}
	ctx := context.Background()
	if err := p.startSites(ctx, req, []*Site{site}); err != nil {
		t.Fatalf("start the site: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = p.stopSites(key(env)) })
	for _, want := range []string{
		fmt.Sprintf("dev PORT=%d SITE=%s API=%s", site.Dev.Port, site.URL, api.URL),
		fmt.Sprintf("serve site shop-web at %s: its dev server, and shop-api under /api/<service>", site.URL),
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("stack dev printed no %q:\n%s", want, out)
		}
	}

	// The page, the config and the API, on the site's one origin.
	page := httpGet(t, site.URL+"/")
	if !strings.Contains(page, "<title>acme shop</title>") || !strings.Contains(page, "/_bun/client/") {
		t.Fatalf("the page through the proxy is not Bun's dev page:\n%s", page)
	}
	if got, want := httpGet(t, site.URL+ir.SiteConfigPath), `{"apis":{"shop-api":{"url":"`+site.URL+`/api/shop-api"}}}`; got != want {
		t.Fatalf("the config = %s, want %s", got, want)
	}
	if got := httpGet(t, site.URL+"/api/shop-api/api/products"); got != `{"path": "/api/products"}` {
		t.Fatalf("shop-api through the proxy answered %s", got)
	}

	// The hot-reload socket, through the proxy, and an edit.
	ws := dialWebSocket(t, site.URL, "/_bun/hmr")
	if first := ws.read(t); len(first) == 0 || first[0] != 'V' {
		t.Fatalf("Bun's hot-reload socket opened with %q, not its version", first)
	}
	ws.write(t, "she")
	ws.write(t, "n/")
	time.Sleep(500 * time.Millisecond) // Bun registers the subscription
	if err := os.WriteFile(filepath.Join(siteDir, "src", "main.ts"), []byte(`document.getElementById("app")!.textContent = "second edit";`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		message := ws.read(t)
		if len(message) > 0 && message[0] == 'u' && bytes.Contains(message, []byte("second edit")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no hot update with the edit reached the page's socket; the last message was %q", message)
		}
	}

	// A dev server that exits stops the environment, as a server does.
	p.mu.Lock()
	proc := p.sites[key(env)][0].proc
	p.mu.Unlock()
	waited := make(chan error, 1)
	go func() { waited <- p.Wait(ctx, req) }()
	if err := proc.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waited:
		if err == nil || !strings.Contains(err.Error(), "site shop-web's dev server exited") {
			t.Fatalf("Wait after the dev server exited: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Wait did not return when the dev server exited")
	}
}

// TestADevServerThatNeverAnswersIsRefused: a dev command that exits before
// it answers fails the site's start, naming the site.
func TestADevServerThatNeverAnswersIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: a site's dev server runs on Bun")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skipf("bun is not available: %v", err)
	}
	repo := t.TempDir()
	outputRoot := filepath.Join(repo, "schemas", "dist")
	siteDir := filepath.Join(repo, "web", "shop-web")
	for name, content := range map[string]string{
		filepath.Join(outputRoot, "package.json"): `{"name": "workspace", "private": true, "workspaces": []}` + "\n",
		filepath.Join(siteDir, "package.json"):    `{"name": "shop-web-site", "private": true, "scripts": {"dev": "echo listening nowhere && exit 3"}}` + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	site := &Site{
		ID: "shop-web.site", Deployable: "shop-web", Name: "shop-web",
		Dir: "web/shop-web", Build: "build", Output: "dist",
		Port:   freeLoopbackPort(t),
		Config: map[string]any{"apis": map[string]any{}},
		Dev:    &SiteDev{Command: "dev", Port: freeLoopbackPort(t)},
	}
	site.URL = ServerURL(site.Port)
	out := &syncBuffer{}
	p := &Provisioner{Out: out, ReadyTimeout: time.Minute}
	env := &ir.ResolvedEnvironment{Stack: "site-stack", Environment: "Dev"}
	err := p.startSites(context.Background(), registry.ProvisionRequest{Environment: env, OutputRoot: outputRoot, RepositoryRoot: repo}, []*Site{site})
	if err == nil || !strings.Contains(err.Error(), "site shop-web's dev server exited before it answered") {
		t.Fatalf("start a dev server that exits: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "[shop-web] listening nowhere") {
		t.Fatalf("the dev server's output is not printed:\n%s", out)
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s %s %v", url, resp.Status, body, err)
	}
	return string(body)
}

// syncBuffer is a buffer the provisioner's output goroutines and the test
// share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// webSocket is the client end of a WebSocket, enough of RFC 6455 to send
// text frames and read a server's unfragmented frames.
type webSocket struct {
	conn net.Conn
	r    *bufio.Reader
}

// dialWebSocket opens a WebSocket at path on the origin base.
func dialWebSocket(t *testing.T, base, path string) *webSocket {
	t.Helper()
	host := strings.TrimPrefix(base, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	_, _ = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n", path, host, base, base64.StdEncoding.EncodeToString(nonce))
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("the WebSocket upgrade at %s%s answered %s", base, path, resp.Status)
	}
	return &webSocket{conn: conn, r: r}
}

// write sends a masked text frame.
func (ws *webSocket) write(t *testing.T, text string) {
	t.Helper()
	payload := []byte(text)
	frame := []byte{0x81, 0x80 | byte(len(payload))}
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	if _, err := ws.conn.Write(frame); err != nil {
		t.Fatal(err)
	}
}

// read returns the payload of the next data frame.
func (ws *webSocket) read(t *testing.T) []byte {
	t.Helper()
	for {
		_ = ws.conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		header := make([]byte, 2)
		if _, err := io.ReadFull(ws.r, header); err != nil {
			t.Fatalf("read a frame: %v", err)
		}
		length := uint64(header[1] & 0x7f)
		switch length {
		case 126:
			var n uint16
			if err := binary.Read(ws.r, binary.BigEndian, &n); err != nil {
				t.Fatal(err)
			}
			length = uint64(n)
		case 127:
			if err := binary.Read(ws.r, binary.BigEndian, &length); err != nil {
				t.Fatal(err)
			}
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(ws.r, payload); err != nil {
			t.Fatal(err)
		}
		if opcode := header[0] & 0x0f; opcode == 1 || opcode == 2 {
			return payload
		}
	}
}
