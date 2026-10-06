package serviceauth_test

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
)

// The test keys. Every one is for tests only and signs nothing else. The
// RSA keys were generated once and are embedded, since RSA key generation
// is not reproducible across Go versions; the P-256 and Ed25519 keys come
// from fixed seeds.

// rsaGoogleJWK and rsaKubeJWK are private RSA-2048 JWK members.
var (
	rsaGoogleJWK = map[string]string{
		"n": "xBGelI48_ZWsrd7eC6D4hgVDhbvDT8HEOwLzOU3pGqqR-x5RHY5vP8-qyqbi5HjMn_Cshhaxy0CGaGerGvLgoSlwOIxxd7dmPVbyD7NviTQ_iil_EcfuZuebr8iqg1x3i60BuSS-pgJeiDIg8zqgfU0l3RW6EEipngiA-32HooVn_L8KKby4fasqjZ58Mvguk5XCj8dAH4H2Q90DZQJG0qSu7wEy-3VG4wBUlp2lRj1c3Ccbyt9wjTNvOO-muaQIWgbnUON_sHrcjoB6ZGc9MyB0JeOEF8fsOoKLm2TglvrhIdPr9GVL8DoOoARE2PmBrEOAKdblsr_k2Iv6nCxMxQ",
		"e": "AQAB",
		"d": "BYiwxuUf3BFwKwUmE8JymUfHQNxMljD0Iq1B461rBaVoUuPnarPlOHIaUjd1Ink1X1NJ70vvLzsuP_6jEfLme64BfJscLcKXqGYOlXpdTMxUecgTjdMsi6uAVa0OgQAoYKEcshbTKbjZ5bKyguL1-itmBPAyzPhcDzmSuGZx4Fiib4JAXVOvLgV5S5jsEaLZqcSnwfkIJgA6-dnKgX6HFVjXzkRMiZInu4pAdJv71xhUGdBlfmm6fPaJBoxrCRuH_soEZ0Yyy_fS48raINN1oRIISMlBymJsJNi2Osnk5QrY1yzaiP7V0p11z--zFtcXdICdGfC2b3Ah6nmqM8yYEQ",
		"p": "16xNYBok8qa5uRWG5NBQHS3fhZmT32EA6qsAlKTRhHQSghgwjTn-uhak2TjhT9JDVhXelqBeZtbaU_GnlOg-ayF_dmHh8kVtw4-CyRaaVl-FuV6XXNumksXtOV1c9rd6Lhad9dfM0rXkhYGeDJpKlqDEPLONHdKIQuLqDOmqz1U",
		"q": "6LrqKA-EU7zDzC21TnEqMGHCEUebeGfQqVLu_2fI4aochDjARkecxlgs17sXyWJge34k6NaayyFcQbTmWYEMVXPC5AFLhA2mqzspBZkBz2YTSyba3ViKmLNDNObta3TD325723270xbLzlTkOgncGTjQCdjIkdh1K512YMeJJ7E",
	}
	rsaKubeJWK = map[string]string{
		"n": "zYiaZfW2fzE5YM_RqNHYPQ3Qi-JGFp3Ks1B8j-kA-ErRrCIZEtRP9S15gQN6lMJq71EOFPtgzDfnOXpuB60-p8nOueV6vhw9tjZg2lJvUNR738jANbXqCusEeGJeTMAjPMsCeXdWLsBK7ajgW1UAk6W3Or4vDUFUR9V4KI3LGIesgOO5LtBf5vm_aWZ2LRT4dqvajoBawDQ7ok_BZThJ5hufmiqK4eUnUZFL9HA27BEnD3e77-QqLk7VYkbMvki3buU-_h3_X9Ke5tSv629x-p51po5zTpyvsHQUv-OnEzl8HuRC_g__3tKSyadGtYOkr99_Uz4mdmjgAQUOcYWQpw",
		"e": "AQAB",
		"d": "Bz6XYpykkBsmEJmCpFaxLoW8IhIZslZhfKyLl275D8djWJPjGlzNbLDrpXZ_7Zpktoa-3lJ1-PzHc6kzE6YxnSxp0veZufW43yFPjKJ3NfwnWZ3z2HDPDQ93mt6swDvNgikNr0ZbjU5N1c6sCcwXNx0SNknZ0rfIxrdpgtRsojKl3oRfw1aQ6wfGtNfGD7CT1sgkym97F9zHWImtWjRk4UhuW9bb3hFLVP6rq4jcY_FbSnUCnO_14i5Gqnqtzra5hxq6yMYxnZBw2rqdyVDhu118eD_ZhNUjMvIXj7Hzye9wuRCFKsefExFxV0OT9yneVlm5HV36HP2ovkD1czhwMQ",
		"p": "3snkPr-ujUd50Y7lUfqdxU7WhQKwQXGUXuu1_of-5euXNzr032TILtfml1L_F0QxrWehhvXuvraW4jxM--DS44FWPR3vGKNg3G1G-2CFpLjRcNjunyl-Vz5xBV4pBbWUl4LycmCYZPwEfTQ0nNhlZjVv5WbSupFgz_BNhqm-lqs",
		"q": "7Cw4obJ5yZSP6q6QxypqZYo-4nK8pXWl7y-BZHTbg7Wpdk46zNq9ghW0YmCppsUoezpPjMDurfIMhjyn_0GVt8xIiVsWeigoWUkSadsTIh6lXNNaCUOh_8czxAvI33BZBHhX97PEFrWIOIZ-8Hx_0rwJQrI6RhuU1jnBGA0qHfU",
	}
)

// testKey is a private test key and the kid its public JWK carries.
type testKey struct {
	kid string
	rsa *rsa.PrivateKey
	ec  *ecdsa.PrivateKey
	ed  ed25519.PrivateKey
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(t testing.TB, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

func seed(label string) []byte {
	sum := sha256.Sum256([]byte("serviceauth test key: " + label))
	return sum[:]
}

func rsaTestKey(t testing.TB, kid string, members map[string]string) *testKey {
	t.Helper()
	n := new(big.Int).SetBytes(unb64(t, members["n"]))
	key := &rsa.PrivateKey{
		PublicKey: rsa.PublicKey{N: n, E: int(new(big.Int).SetBytes(unb64(t, members["e"])).Int64())},
		D:         new(big.Int).SetBytes(unb64(t, members["d"])),
		Primes: []*big.Int{
			new(big.Int).SetBytes(unb64(t, members["p"])),
			new(big.Int).SetBytes(unb64(t, members["q"])),
		},
	}
	if err := key.Validate(); err != nil {
		t.Fatalf("rsa key %s: %v", kid, err)
	}
	key.Precompute()
	return &testKey{kid: kid, rsa: key}
}

func ecTestKey(t testing.TB, kid, label string) *testKey {
	t.Helper()
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), seed(label))
	if err != nil {
		t.Fatalf("ec key %s: %v", kid, err)
	}
	return &testKey{kid: kid, ec: key}
}

// edTestKey derives an Ed25519 key; an empty kid becomes its RFC 7638
// thumbprint, as the generic connector names an edge's key.
func edTestKey(t testing.TB, kid, label string) *testKey {
	t.Helper()
	k := &testKey{ed: ed25519.NewKeyFromSeed(seed(label))}
	if kid == "" {
		tp, err := serviceauth.Thumbprint(k.public())
		if err != nil {
			t.Fatal(err)
		}
		kid = tp
	}
	k.kid = kid
	return k
}

// public returns the key's public JWK, with kid.
func (k *testKey) public() serviceauth.JWK {
	switch {
	case k.rsa != nil:
		return serviceauth.JWK{Kty: "RSA", Kid: k.kid, Use: "sig", Alg: "RS256", N: b64(k.rsa.N.Bytes()), E: b64(big.NewInt(int64(k.rsa.E)).Bytes())}
	case k.ec != nil:
		pub, err := k.ec.PublicKey.Bytes()
		if err != nil {
			panic(err)
		}
		return serviceauth.JWK{Kty: "EC", Kid: k.kid, Use: "sig", Alg: "ES256", Crv: "P-256", X: b64(pub[1:33]), Y: b64(pub[33:])}
	default:
		return serviceauth.JWK{Kty: "OKP", Kid: k.kid, Crv: "Ed25519", X: b64(k.ed.Public().(ed25519.PublicKey))}
	}
}

// privateJWK is an Ed25519 key's private JWK, as SignedToken reads it.
func (k *testKey) privateJWK(t testing.TB) []byte {
	t.Helper()
	j := k.public()
	j.D = b64(k.ed.Seed())
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// algES256DER signs ES256 but writes the signature in ASN.1 DER form, which
// a JWS must not use.
const algES256DER = "ES256-DER"

// sign signs input under alg. Every signature is deterministic: RS256
// (PKCS #1 v1.5), EdDSA, and ES256 with a nil random source (RFC 6979).
func (k *testKey) sign(t testing.TB, alg string, input []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(input)
	switch alg {
	case serviceauth.AlgRS256:
		sig, err := rsa.SignPKCS1v15(nil, k.rsa, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return sig
	case serviceauth.AlgES256, algES256DER:
		der, err := k.ec.Sign(nil, digest[:], crypto.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		if alg == algES256DER {
			return der
		}
		var rs struct{ R, S *big.Int }
		if _, err := asn1.Unmarshal(der, &rs); err != nil {
			t.Fatal(err)
		}
		raw := make([]byte, 64)
		rs.R.FillBytes(raw[:32])
		rs.S.FillBytes(raw[32:])
		return raw
	case serviceauth.AlgEdDSA, serviceauth.AlgEd25519:
		return ed25519.Sign(k.ed, input)
	}
	t.Fatalf("no signer for %s", alg)
	return nil
}

// header is the JWS header a key writes: alg, kid, typ.
func (k *testKey) header(alg string) object {
	return object{{"alg", alg}, {"kid", k.kid}, {"typ", "JWT"}}
}

// jwt signs header.claims with k under alg.
func (k *testKey) jwt(t testing.TB, alg string, header, claims object) string {
	t.Helper()
	input := b64(mustJSON(t, header)) + "." + b64(mustJSON(t, claims))
	return input + "." + b64(k.sign(t, alg, []byte(input)))
}

// member is one member of an object.
type member struct {
	name  string
	value any
}

// object is a JSON object that keeps its members' order, so a token's
// bytes are fixed.
type object []member

func (o object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(m.name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(m.value)
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// with returns a copy of o with name set to value, in place when o has it.
func (o object) with(name string, value any) object {
	out := make(object, 0, len(o)+1)
	found := false
	for _, m := range o {
		if m.name == name {
			m.value = value
			found = true
		}
		out = append(out, m)
	}
	if !found {
		out = append(out, member{name, value})
	}
	return out
}

// without returns a copy of o without name.
func (o object) without(name string) object {
	out := make(object, 0, len(o))
	for _, m := range o {
		if m.name != name {
			out = append(out, m)
		}
	}
	return out
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
