package serviceauth_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
)

// loopbackFor builds the loopback key of Monolith, which serves shop-api
// and shop-orders, on clock, its seed and jti from a reader of their own
// so that two keys built with different fill bytes differ.
func loopbackFor(t *testing.T, clock *fakeClock, fill byte) *serviceauth.Loopback {
	t.Helper()
	random := bytes.Repeat([]byte{fill}, 32+16*8)
	l, err := serviceauth.NewLoopback("Monolith", []string{"shop-api", "shop-orders"},
		serviceauth.WithClock(clock.now), serviceauth.WithRandom(bytes.NewReader(random)))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// verifierOf is the service authenticator of an API of Monolith whose
// callers field is cfg, with the loopback key accepted.
func verifierOf(t *testing.T, l *serviceauth.Loopback, cfg serviceauth.Config, clock *fakeClock) *serviceauth.Verifier {
	t.Helper()
	accepted, err := l.Accept(cfg)
	if err != nil {
		t.Fatal(err)
	}
	v, err := serviceauth.New(accepted, serviceauth.WithClock(clock.now))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestALoopbackTokenIsTheServerItself: a token the loopback key signs is
// the generic connector's token, iss, sub and aud the server, and the
// authenticator that accepts the key admits it as the server, serving
// every API it serves, beside the callers its field lists; past its five
// minutes and the leeway it is refused.
func TestALoopbackTokenIsTheServerItself(t *testing.T) {
	keys := newParityKeys(t)
	clock := newClock(parityNow)
	l := loopbackFor(t, clock, 7)
	v := verifierOf(t, l, parityConfigs(keys)["keypair"], clock)

	token, err := l.TokenSource()(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	issuer := l.Issuer()
	if header := string(unb64(t, segment(token, 0))); header != `{"alg":"EdDSA","kid":"`+issuer.Keys[0].Kid+`","typ":"JWT"}` {
		t.Errorf("header %s", header)
	}
	claims := string(unb64(t, segment(token, 1)))
	if want := `{"iss":"Monolith","sub":"Monolith","aud":"Monolith","iat":1767225600,"exp":1767225900,"jti":`; !strings.HasPrefix(claims, want) {
		t.Errorf("claims %s, want them to begin %s", claims, want)
	}
	caller, err := v.Authenticate(request(token))
	if err != nil {
		t.Fatal(err)
	}
	if want := (&serviceauth.Caller{Deployable: "Monolith", Serves: []string{"shop-api", "shop-orders"}, Subject: "Monolith"}); !reflect.DeepEqual(caller, want) {
		t.Errorf("caller = %+v, want %+v", caller, want)
	}

	// The field's own callers still verify.
	edge := signedToken(t, keys.edgeOrders, "orders", parityNow)
	if caller, err := v.Authenticate(request(edge)); err != nil || caller.Deployable != "orders" {
		t.Errorf("the field's caller = %+v, %v; want orders", caller, err)
	}

	clock.advance(359 * time.Second)
	if _, err := v.Authenticate(request(token)); err != nil {
		t.Errorf("within the leeway: %v", err)
	}
	clock.advance(time.Second)
	if _, err := v.Authenticate(request(token)); !isCode(err, serviceauth.CodeUnauthorized) {
		t.Errorf("an expired loopback token = %v, want 401 service_unauthorized", err)
	}
}

// TestALoopbackKeyIsItsProcesssAlone: a token another key signed under the
// loopback issuer's name, as an earlier process or another instance of the
// server would, is refused, its kid unknown, and so is one that names the
// key's kid but is signed with another key; a field that already names the
// loopback issuer is refused.
func TestALoopbackKeyIsItsProcesssAlone(t *testing.T) {
	clock := newClock(parityNow)
	l := loopbackFor(t, clock, 7)
	other := loopbackFor(t, clock, 8)
	v := verifierOf(t, l, serviceauth.Config{}, clock)

	earlier, err := other.TokenSource()(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Authenticate(request(earlier)); !isCode(err, serviceauth.CodeUnauthorized) {
		t.Errorf("another process's token = %v, want 401 service_unauthorized", err)
	}
	own, err := l.TokenSource()(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	forged := segment(own, 0) + "." + segment(earlier, 1) + "." + segment(earlier, 2)
	if _, err := v.Authenticate(request(forged)); !isCode(err, serviceauth.CodeUnauthorized) {
		t.Errorf("a token under the key's kid signed by another = %v, want 401 service_unauthorized", err)
	}
	if l.Issuer().Keys[0].Kid == other.Issuer().Keys[0].Kid {
		t.Error("two loopback keys share a kid")
	}

	for _, cfg := range []serviceauth.Config{
		{Issuers: []serviceauth.IssuerConfig{{Issuer: "Monolith", Audience: "Monolith", Algorithms: []string{serviceauth.AlgEdDSA}, Keys: l.Issuer().Keys}}},
		{Issuers: []serviceauth.IssuerConfig{{Issuer: "orders", IssuerAliases: []string{"Monolith"}, Audience: "Monolith", Algorithms: []string{serviceauth.AlgEdDSA}, Keys: l.Issuer().Keys}}},
	} {
		if _, err := l.Accept(cfg); err == nil || !strings.Contains(err.Error(), "the server's own loopback issuer") {
			t.Errorf("Accept(%+v) = %v, want the issuer's name refused", cfg.Issuers[0], err)
		}
	}
	if _, err := serviceauth.NewLoopback("", nil); err == nil {
		t.Error("a loopback key without a deployable")
	}
}

// TestALoopbackTokenGoesOnlyToALoopbackURL: CredentialFor gives the token
// for a URL only this host answers, and nothing for any other; the
// parity vectors hold IsLoopbackURL's rule.
func TestALoopbackTokenGoesOnlyToALoopbackURL(t *testing.T) {
	l := loopbackFor(t, newClock(parityNow), 7)
	for raw, want := range map[string]bool{
		"http://127.0.0.1:8080/api":      true,
		"http://[::1]:8080":              true,
		"http://shop-api:8080":           false,
		"https://shop-api-abc.a.run.app": false,
	} {
		source, headers := l.CredentialFor(raw)
		if (source != nil) != want || (want && !reflect.DeepEqual(headers, []string{serviceauth.HeaderName})) {
			t.Errorf("CredentialFor(%q) = %v, %v; want a token %t", raw, source != nil, headers, want)
		}
	}
}

// isCode reports whether err is a service refusal with code.
func isCode(err error, code string) bool {
	var refusal *serviceauth.Error
	return errors.As(err, &refusal) && refusal.Code == code && refusal.Status == map[string]int{
		serviceauth.CodeUnauthorized: http.StatusUnauthorized,
		serviceauth.CodeForbidden:    http.StatusForbidden,
	}[code]
}
