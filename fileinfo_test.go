package main

import (
	"io"
	"os"
	"testing"
	"time"
)

func TestFileInfo(t *testing.T) {
	now := time.Now()
	fi := newFileInfo("test.bin", 42, now)
	if fi.Name() != "test.bin" {
		t.Fatalf("Name()=%q, want test.bin", fi.Name())
	}
	if fi.Size() != 42 {
		t.Fatalf("Size()=%d, want 42", fi.Size())
	}
	if fi.IsDir() {
		t.Fatal("file should not be a directory")
	}

	di := newDirInfo("mydir")
	if di.Name() != "mydir" {
		t.Fatalf("Name()=%q, want mydir", di.Name())
	}
	if !di.IsDir() {
		t.Fatal("directory should report IsDir=true")
	}
}

func TestListerAt(t *testing.T) {
	infos := listerAt{
		newFileInfo("a", 1, time.Time{}),
		newFileInfo("b", 2, time.Time{}),
		newFileInfo("c", 3, time.Time{}),
	}

	buf := make([]os.FileInfo, 2)
	n, err := infos.ListAt(buf, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 || buf[0].Name() != "a" || buf[1].Name() != "b" {
		t.Fatalf("unexpected result: n=%d, buf=%v", n, buf)
	}

	n, err = infos.ListAt(buf, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 || buf[0].Name() != "b" || buf[1].Name() != "c" {
		t.Fatalf("unexpected result: n=%d, buf=%v", n, buf)
	}

	// Request more than available.
	buf = make([]os.FileInfo, 5)
	n, err = infos.ListAt(buf, 0)
	if err != io.EOF {
		t.Fatalf("expected EOF, got err=%v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 entries, got %d", n)
	}
}
