package cloudflare

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Credentials are the S3 credentials of an API token on R2: the access
// key id is the token's id, and the secret access key the hex SHA-256 of
// the token's value. A token with R2's permissions reaches the buckets it
// may through the S3 API with them, as Pulumi's S3 backend does.
func S3Credentials(tokenID, token string) (accessKeyID, secretAccessKey string) {
	sum := sha256.Sum256([]byte(token))
	return tokenID, hex.EncodeToString(sum[:])
}

// signV4 signs an S3 request with AWS Signature Version 4, the scheme R2's
// S3 API takes, over the host, the range, the content type and every
// x-amz- header, with the payload's SHA-256. It sets the request's
// x-amz-date, x-amz-content-sha256 and Authorization headers, and its
// path to the encoding it signed.
func signV4(req *http.Request, body []byte, accessKeyID, secretAccessKey, region string, now time.Time) {
	const service = "s3"
	now = now.UTC()
	stamp := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	payload := sha256.Sum256(body)
	payloadHex := hex.EncodeToString(payload[:])
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", payloadHex)

	path := canonicalURI(req.URL.Path)
	req.URL.RawPath = path

	headers := map[string]string{"host": req.URL.Host}
	for k, vs := range req.Header {
		name := strings.ToLower(k)
		if strings.HasPrefix(name, "x-amz-") || name == "content-type" || name == "range" || name == "content-md5" {
			headers[name] = strings.Join(vs, ",")
		}
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + strings.TrimSpace(headers[name]) + "\n")
	}
	signed := strings.Join(names, ";")

	request := strings.Join([]string{
		req.Method,
		path,
		canonicalQuery(req.URL.Query()),
		canonicalHeaders.String(),
		signed,
		payloadHex,
	}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	requestHash := sha256.Sum256([]byte(request))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])

	key := hmacSHA256([]byte("AWS4"+secretAccessKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+signature)
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// canonicalURI encodes a path as S3 signs it: each byte but the unreserved
// characters and the slashes percent-encoded, once.
func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	return uriEncode(path, false)
}

// canonicalQuery encodes a query as SigV4 signs it: each name and value
// encoded, the pairs sorted.
func canonicalQuery(q url.Values) string {
	var pairs []string
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// uriEncode percent-encodes every byte of s but A-Z, a-z, 0-9, `-`, `_`,
// `.` and `~`, and `/` unless slash is set.
func uriEncode(s string, slash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !slash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}
