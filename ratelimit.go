package main

import (
	"context"
	"fmt"
	"io"
	"sync"

	"golang.org/x/time/rate"
)

// userConnLimiter tracks active SFTP connections per user and enforces a
// per-user maximum. A zero or negative limit means unlimited.
type userConnLimiter struct {
	mu     sync.Mutex
	limits map[string]int
	counts map[string]int
}

func newUserConnLimiter(users []UserConfig) *userConnLimiter {
	l := &userConnLimiter{
		limits: make(map[string]int),
		counts: make(map[string]int),
	}
	l.Update(users)
	return l
}

// Update replaces the configured limits. Existing counts are preserved.
func (l *userConnLimiter) Update(users []UserConfig) {
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
func (l *userConnLimiter) Acquire(user string) bool {
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
func (l *userConnLimiter) Release(user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[user] > 0 {
		l.counts[user]--
	}
}

// newUserRateLimiter returns a token-bucket limiter for a user's configured
// bytes-per-second limit, or nil if unlimited. The bucket allows a one-second
// burst.
func newUserRateLimiter(bytesPerSec int64) *rate.Limiter {
	if bytesPerSec <= 0 {
		return nil
	}
	burst := int(bytesPerSec)
	if burst < 64*1024 {
		burst = 64 * 1024
	}
	return rate.NewLimiter(rate.Limit(bytesPerSec), burst)
}

// rateLimitedReader wraps an io.ReaderAt with a per-read token bucket wait.
type rateLimitedReader struct {
	io.ReaderAt
	lim *rate.Limiter
}

func (r *rateLimitedReader) ReadAt(p []byte, off int64) (int, error) {
	if r.lim != nil {
		if err := r.lim.WaitN(context.Background(), len(p)); err != nil {
			return 0, fmt.Errorf("rate limit: %w", err)
		}
	}
	return r.ReaderAt.ReadAt(p, off)
}

// rateLimitedWriter wraps an io.WriterAt with a per-write token bucket wait.
type rateLimitedWriter struct {
	io.WriterAt
	lim *rate.Limiter
}

func (w *rateLimitedWriter) WriteAt(p []byte, off int64) (int, error) {
	if w.lim != nil {
		if err := w.lim.WaitN(context.Background(), len(p)); err != nil {
			return 0, fmt.Errorf("rate limit: %w", err)
		}
	}
	return w.WriterAt.WriteAt(p, off)
}
