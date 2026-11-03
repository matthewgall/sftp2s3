package ratelimit

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/matthewgall/sftp2s3/internal/config"
)

func TestUserConnLimiter(t *testing.T) {
	users := []config.UserConfig{
		{Username: "alice", MaxConnections: 2},
		{Username: "bob"},
	}
	l := NewUserConnLimiter(users)

	if !l.Acquire("alice") {
		t.Fatal("expected first alice acquire to succeed")
	}
	if !l.Acquire("alice") {
		t.Fatal("expected second alice acquire to succeed")
	}
	if l.Acquire("alice") {
		t.Fatal("expected third alice acquire to fail")
	}
	l.Release("alice")
	if !l.Acquire("alice") {
		t.Fatal("expected acquire after release to succeed")
	}

	for i := 0; i < 10; i++ {
		if !l.Acquire("bob") {
			t.Fatalf("expected unlimited bob acquire %d to succeed", i)
		}
	}
}

func TestUserConnLimiterUpdate(t *testing.T) {
	l := NewUserConnLimiter([]config.UserConfig{{Username: "alice", MaxConnections: 1}})
	if !l.Acquire("alice") {
		t.Fatal("expected acquire to succeed")
	}
	l.Update([]config.UserConfig{{Username: "alice", MaxConnections: 2}})
	if !l.Acquire("alice") {
		t.Fatal("expected acquire after limit increase to succeed")
	}
}

func TestRateLimitedReaderPassesData(t *testing.T) {
	lim := NewUserRateLimiter(1024)
	r := &RateLimitedReader{ReaderAt: strings.NewReader("hello world"), Lim: lim}

	buf := make([]byte, 11)
	if _, err := r.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello world" {
		t.Fatalf("got %q, want %q", buf, "hello world")
	}
}

func TestUserRateRegistrySharedLimiter(t *testing.T) {
	users := []config.UserConfig{
		{Username: "alice", RateLimitBytesPerSec: 1024},
		{Username: "bob"},
	}
	r := NewUserRateRegistry(users)

	lim1 := r.Limiter("alice")
	lim2 := r.Limiter("alice")
	if lim1 == nil || lim1 != lim2 {
		t.Fatal("expected a single shared limiter for alice")
	}
	if r.Limiter("bob") != nil {
		t.Fatal("expected no limiter for bob")
	}
}

func TestUserRateRegistryUpdate(t *testing.T) {
	users := []config.UserConfig{{Username: "alice", RateLimitBytesPerSec: 1024}}
	r := NewUserRateRegistry(users)
	lim := r.Limiter("alice")

	r.Update([]config.UserConfig{{Username: "alice", RateLimitBytesPerSec: 2048}})
	if lim.Limit() != 2048 {
		t.Fatalf("expected limit to update to 2048, got %v", lim.Limit())
	}

	r.Update(nil)
	if r.Limiter("alice") != nil {
		t.Fatal("expected limiter to be removed when config cleared")
	}
}

func TestNewUserRateLimiter(t *testing.T) {
	if lim := NewUserRateLimiter(0); lim != nil {
		t.Fatal("expected nil limiter for 0")
	}
	if lim := NewUserRateLimiter(-1); lim != nil {
		t.Fatal("expected nil limiter for negative")
	}
	lim := NewUserRateLimiter(1024)
	if lim.Limit() != 1024 {
		t.Fatalf("expected limit 1024, got %v", lim.Limit())
	}
	if lim.Burst() < 64*1024 {
		t.Fatalf("expected burst at least 64KiB, got %v", lim.Burst())
	}
}

func TestMaxDuration(t *testing.T) {
	if MaxDuration(1*time.Second, 2*time.Second) != 2*time.Second {
		t.Fatal("expected max duration to be 2s")
	}
	if MaxDuration(3*time.Second, 1*time.Second) != 3*time.Second {
		t.Fatal("expected max duration to be 3s")
	}
}

func TestNewAcceptRateLimiter(t *testing.T) {
	if lim := NewAcceptRateLimiter(0); lim != nil {
		t.Fatal("expected nil limiter for 0")
	}
	if lim := NewAcceptRateLimiter(-1); lim != nil {
		t.Fatal("expected nil limiter for negative")
	}
	lim := NewAcceptRateLimiter(10)
	if lim.Limit() != 10 {
		t.Fatalf("expected limit 10, got %v", lim.Limit())
	}
}

func TestRateLimitedWriterPassesData(t *testing.T) {
	lim := NewUserRateLimiter(1024)
	var wb strings.Builder
	w := &RateLimitedWriter{WriterAt: &writerAt{&wb}, Lim: lim}

	if _, err := w.WriteAt([]byte("hello world"), 0); err != nil {
		t.Fatal(err)
	}
	if wb.String() != "hello world" {
		t.Fatalf("got %q, want %q", wb.String(), "hello world")
	}
}

// writerAt wraps a strings.Builder so it implements io.WriterAt.
type writerAt struct {
	w *strings.Builder
}

func (w *writerAt) WriteAt(p []byte, off int64) (int, error) {
	if off != 0 {
		return 0, io.ErrShortWrite
	}
	return w.w.Write(p)
}
