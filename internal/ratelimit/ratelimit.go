// Package ratelimit provides connection and byte-rate limiting for sftp2s3.
package ratelimit

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/matthewgall/sftp2s3/internal/config"
	"golang.org/x/time/rate"
)

// NewAcceptRateLimiter returns a token bucket that caps the rate at which new
// TCP connections are accepted, or nil if no limit is configured.
func NewAcceptRateLimiter(perSec int) *rate.Limiter {
	if perSec <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Limit(perSec), perSec)
}

// UserConnLimiter tracks active SFTP connections per user and enforces a
// per-user maximum. A zero or negative limit means unlimited.
type UserConnLimiter struct {
	mu     sync.Mutex
	limits map[string]int
	counts map[string]int
}

// NewUserConnLimiter creates a UserConnLimiter populated from users.
func NewUserConnLimiter(users []config.UserConfig) *UserConnLimiter {
	l := &UserConnLimiter{
		limits: make(map[string]int),
		counts: make(map[string]int),
	}
	l.Update(users)
	return l
}

// Update replaces the configured limits. Existing counts are preserved.
func (l *UserConnLimiter) Update(users []config.UserConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limits = make(map[string]int, len(users))
	for _, u := range users {
		if u.MaxConnections > 0 {
			l.limits[u.Username] = u.MaxConnections
		}
	}
}

// Acquire tries to add a connection for user. It returns false if the user is
// already at their configured limit.
func (l *UserConnLimiter) Acquire(user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	max, ok := l.limits[user]
	if !ok {
		return true
	}
	if l.counts[user] >= max {
		return false
	}
	l.counts[user]++
	return true
}

// Release removes a connection for user.
func (l *UserConnLimiter) Release(user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[user] > 0 {
		l.counts[user]--
	}
}

// UserRateRegistry holds shared token-bucket limiters keyed by username. All
// connections for a user share the same limiter, so the configured bytes/sec
// is a global cap for that user.
type UserRateRegistry struct {
	mu       sync.Mutex
	limits   map[string]int64
	limiters map[string]*rate.Limiter
}

// NewUserRateRegistry creates a UserRateRegistry populated from users.
func NewUserRateRegistry(users []config.UserConfig) *UserRateRegistry {
	r := &UserRateRegistry{
		limits:   make(map[string]int64),
		limiters: make(map[string]*rate.Limiter),
	}
	r.Update(users)
	return r
}

// Update applies new configured limits. Existing limiters are reused when
// possible to avoid resetting a user's bucket on reload.
func (r *UserRateRegistry) Update(users []config.UserConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	newLimits := make(map[string]int64, len(users))
	for _, u := range users {
		if u.RateLimitBytesPerSec > 0 {
			newLimits[u.Username] = u.RateLimitBytesPerSec
		}
	}

	// Remove limiters for users that are no longer rate-limited.
	for user := range r.limiters {
		if _, ok := newLimits[user]; !ok {
			delete(r.limiters, user)
		}
	}

	// Create or update limiters for configured users.
	for user, bytesPerSec := range newLimits {
		if lim, ok := r.limiters[user]; ok {
			lim.SetLimit(rate.Limit(bytesPerSec))
			burst := limiterBurst(bytesPerSec)
			lim.SetBurst(burst)
		} else {
			r.limiters[user] = rate.NewLimiter(rate.Limit(bytesPerSec), limiterBurst(bytesPerSec))
		}
	}

	r.limits = newLimits
}

// Limiter returns the shared rate limiter for user, or nil if unlimited.
func (r *UserRateRegistry) Limiter(user string) *rate.Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.limiters[user]
}

// MaxDuration returns the larger of a and b.
func MaxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func limiterBurst(bytesPerSec int64) int {
	burst := int(bytesPerSec)
	if burst < 64*1024 {
		burst = 64 * 1024
	}
	return burst
}

// NewUserRateLimiter returns a token-bucket limiter for a user's configured
// bytes-per-second limit, or nil if unlimited. The bucket allows a one-second
// burst.
func NewUserRateLimiter(bytesPerSec int64) *rate.Limiter {
	if bytesPerSec <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Limit(bytesPerSec), limiterBurst(bytesPerSec))
}

// RateLimitedReader wraps an io.ReaderAt with a per-read token bucket wait.
type RateLimitedReader struct {
	io.ReaderAt
	Lim *rate.Limiter
}

func (r *RateLimitedReader) ReadAt(p []byte, off int64) (int, error) {
	if r.Lim != nil {
		if err := r.Lim.WaitN(context.Background(), len(p)); err != nil {
			return 0, fmt.Errorf("rate limit: %w", err)
		}
	}
	return r.ReaderAt.ReadAt(p, off)
}

// RateLimitedWriter wraps an io.WriterAt with a per-write token bucket wait.
type RateLimitedWriter struct {
	io.WriterAt
	Lim *rate.Limiter
}

func (w *RateLimitedWriter) WriteAt(p []byte, off int64) (int, error) {
	if w.Lim != nil {
		if err := w.Lim.WaitN(context.Background(), len(p)); err != nil {
			return 0, fmt.Errorf("rate limit: %w", err)
		}
	}
	return w.WriterAt.WriteAt(p, off)
}
