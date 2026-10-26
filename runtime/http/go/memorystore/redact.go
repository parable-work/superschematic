package memorystore

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// The runtime logs no URL, no token and no part. A Redis client writes
// some lines of its own, such as a failed dial while the server is down,
// through one logger for the whole process whose lines name no client.
// ClientLog is that logger: the generated memorystores.go installs it once
// as the client's, and each open store adds its own logger, with Add, and
// removes it on Close.
//
//   - A line goes to the logger of the newest store still open, at Debug,
//     since the store logs each change of its connection's state itself
//     and the client writes its line again on every failed dial; with no
//     store open it is dropped.
//   - Each redis:// or rediss:// URL in it, and each secret a store added
//     (an access token, say), is replaced by [redacted].
var ClientLog = &RedactingLog{}

// redisURLPattern matches a Redis URL.
var redisURLPattern = regexp.MustCompile(`(?i)rediss?://\S*`)

// maxSecrets is how many secrets a RedactingLog keeps.
const maxSecrets = 32

// Redacted is what replaces a URL or a secret in a line.
const Redacted = "[redacted]"

// RedactingLog writes a Redis client's lines to the newest open store's
// logger, with every URL and secret removed. It has the Printf of the
// client's logging interface.
type RedactingLog struct {
	mu      sync.RWMutex
	loggers []*zap.Logger
	// secrets are kept oldest first, the newest 32, and stay after their
	// store closes, since one of the client's goroutines can still write a
	// line.
	secrets []string
}

// Add makes logger the target of the client's lines until the returned
// function is called.
func (r *RedactingLog) Add(logger *zap.Logger) (release func()) {
	target := logger.With(zap.String("source", "redis-client"))
	r.mu.Lock()
	r.loggers = append(r.loggers, target)
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.loggers = slices.DeleteFunc(r.loggers, func(l *zap.Logger) bool { return l == target })
		})
	}
}

// AddSecret adds a secret no line may hold.
func (r *RedactingLog) AddSecret(secret string) {
	if secret == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.secrets, secret) {
		return
	}
	r.secrets = append(r.secrets, secret)
	// A token per connection would grow the list without end: keep the
	// newest, which are the ones still in use.
	if len(r.secrets) > maxSecrets {
		r.secrets = slices.Delete(r.secrets, 0, len(r.secrets)-maxSecrets)
	}
}

// Printf writes one of the client's lines.
func (r *RedactingLog) Printf(_ context.Context, format string, v ...any) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.loggers) == 0 {
		return
	}
	logger := r.loggers[len(r.loggers)-1]
	if !logger.Core().Enabled(zapcore.DebugLevel) {
		return
	}
	logger.Debug(r.redact(fmt.Sprintf(format, v...)))
}

// Redact removes every URL and secret from line.
func (r *RedactingLog) Redact(line string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.redact(line)
}

func (r *RedactingLog) redact(line string) string {
	line = redisURLPattern.ReplaceAllString(line, Redacted)
	// Longest first, so a secret that holds another is removed whole.
	secrets := slices.Clone(r.secrets)
	slices.SortStableFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, secret := range secrets {
		line = strings.ReplaceAll(line, secret, Redacted)
	}
	return line
}

// RedactError is err's message with every URL and secret removed, for a
// log line or an error a store returns.
func RedactError(err error) string {
	if err == nil {
		return ""
	}
	return ClientLog.Redact(err.Error())
}
