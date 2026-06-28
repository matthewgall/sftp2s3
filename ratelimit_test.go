package main

import (
	"io"
	"strings"
	"testing"
)

func TestUserConnLimiter(t *testing.T) {
	users := []UserConfig{
		{Username: "alice", MaxConnections: 2},
		{Username: "bob"},
	}
	l := newUserConnLimiter(users)

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
	l := newUserConnLimiter([]UserConfig{{Username: "alice", MaxConnections: 1}})
	if !l.Acquire("alice") {
		t.Fatal("expected acquire to succeed")
	}
	l.Update([]UserConfig{{Username: "alice", MaxConnections: 2}})
	if !l.Acquire("alice") {
		t.Fatal("expected acquire after limit increase to succeed")
	}
}

func TestRateLimitedReaderPassesData(t *testing.T) {
	lim := newUserRateLimiter(1024)
	r := &rateLimitedReader{ReaderAt: strings.NewReader("hello world"), lim: lim}

	buf := make([]byte, 11)
	if _, err := r.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello world" {
		t.Fatalf("got %q, want %q", buf, "hello world")
	}
}

func TestRateLimitedWriterPassesData(t *testing.T) {
	lim := newUserRateLimiter(1024)
	var wb strings.Builder
	w := &rateLimitedWriter{WriterAt: &writerAt{&wb}, lim: lim}

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
