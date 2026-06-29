package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/pkg/sftp"
)

func newTestHandlers(t *testing.T, objects map[string][]byte) *S3Handlers {
	t.Helper()
	b := newMockBackend(t, "bucket", "router/", objects)
	return NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)
}

func newTestListHandlers(t *testing.T, objects map[string][]byte) *S3Handlers {
	t.Helper()
	b := newMockBackend(t, "bucket", "router/", objects)
	return NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileList.(*S3Handlers)
}

func TestHandlersListDirectory(t *testing.T) {
	objects := map[string][]byte{
		"router/file1.bin":        make([]byte, 100),
		"router/file2.bin":        make([]byte, 200),
		"router/subdir/file3.bin": make([]byte, 300),
	}
	h := newTestListHandlers(t, objects)
	ctx := context.Background()

	infos, err := h.listDirectory(ctx, "/mock")
	if err != nil {
		t.Fatalf("list /mock: %v", err)
	}

	at := infos.(listerAt)
	var got []string
	buf := make([]os.FileInfo, 10)
	for {
		n, err := at.ListAt(buf, int64(len(got)))
		for i := 0; i < n; i++ {
			got = append(got, buf[i].Name())
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ListAt: %v", err)
		}
	}

	want := []string{"file1.bin", "file2.bin", "subdir"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestHandlersStatFile(t *testing.T) {
	objects := map[string][]byte{
		"router/file.bin": make([]byte, 1234),
	}
	h := newTestListHandlers(t, objects)
	ctx := context.Background()

	infos, err := h.statPath(ctx, "/mock/file.bin")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	at := infos.(listerAt)
	buf := make([]os.FileInfo, 1)
	if _, err := at.ListAt(buf, 0); err != nil {
		t.Fatalf("ListAt: %v", err)
	}
	if buf[0].Name() != "file.bin" || buf[0].Size() != 1234 {
		t.Fatalf("unexpected file info: %+v", buf[0])
	}
	if buf[0].IsDir() {
		t.Fatal("expected file, got directory")
	}
}

func TestHandlersStatDirectory(t *testing.T) {
	objects := map[string][]byte{
		"router/subdir/file.bin": make([]byte, 100),
	}
	h := newTestListHandlers(t, objects)
	ctx := context.Background()

	infos, err := h.statPath(ctx, "/mock/subdir")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	at := infos.(listerAt)
	buf := make([]os.FileInfo, 1)
	if _, err := at.ListAt(buf, 0); err != nil {
		t.Fatalf("ListAt: %v", err)
	}
	if buf[0].Name() != "subdir" || !buf[0].IsDir() {
		t.Fatalf("unexpected dir info: %+v", buf[0])
	}
}

func TestHandlersRemoveFile(t *testing.T) {
	objects := map[string][]byte{
		"router/file.bin": make([]byte, 100),
	}
	h := newTestHandlers(t, objects)

	err := h.Filecmd(&sftp.Request{Filepath: "/mock/file.bin", Method: "Remove"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := objects["router/file.bin"]; ok {
		t.Fatal("object was not deleted")
	}
}

func TestHandlersRemoveDirectory(t *testing.T) {
	objects := map[string][]byte{
		"router/dir/file1.bin": make([]byte, 100),
		"router/dir/file2.bin": make([]byte, 200),
	}
	h := newTestHandlers(t, objects)

	err := h.Filecmd(&sftp.Request{Filepath: "/mock/dir", Method: "Remove"})
	if err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	for k := range objects {
		if strings.HasPrefix(k, "router/dir/") {
			t.Fatalf("object %q was not deleted", k)
		}
	}
}

func TestHandlersRenameFile(t *testing.T) {
	objects := map[string][]byte{
		"router/old.bin": make([]byte, 100),
	}
	h := newTestHandlers(t, objects)

	err := h.Filecmd(&sftp.Request{
		Filepath: "/mock/old.bin",
		Target:   "/mock/new.bin",
		Method:   "Rename",
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, ok := objects["router/old.bin"]; ok {
		t.Fatal("old object still exists")
	}
	if len(objects["router/new.bin"]) != 100 {
		t.Fatal("new object missing or wrong size")
	}
}

func TestHandlersListRoot(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileList.(*S3Handlers)
	infos, err := h.listRoot()
	if err != nil {
		t.Fatal(err)
	}
	at := infos.(listerAt)
	buf := make([]os.FileInfo, 1)
	if _, err := at.ListAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if buf[0].Name() != "mock" || !buf[0].IsDir() {
		t.Fatalf("unexpected root entry: %+v", buf[0])
	}
}

func TestHandlersLstat(t *testing.T) {
	objects := map[string][]byte{
		"router/file.bin": make([]byte, 1234),
	}
	h := newTestListHandlers(t, objects)

	for _, method := range []string{"Stat", "Lstat"} {
		infos, err := h.Filelist(&sftp.Request{Filepath: "/mock/file.bin", Method: method})
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		at := infos.(listerAt)
		buf := make([]os.FileInfo, 1)
		if _, err := at.ListAt(buf, 0); err != nil {
			t.Fatalf("%s ListAt: %v", method, err)
		}
		if buf[0].Name() != "file.bin" || buf[0].Size() != 1234 {
			t.Fatalf("%s unexpected info: %+v", method, buf[0])
		}
	}
}

func TestHandlersStatNotFound(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileList.(*S3Handlers)
	ctx := context.Background()
	_, err := h.statPath(ctx, "/mock/missing")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestHandlersFilewrite(t *testing.T) {
	objects := map[string][]byte{}
	b := newMockBackend(t, "bucket", "", objects)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FilePut.(*S3Handlers)

	w, err := h.Filewrite(&sftp.Request{Filepath: "/mock/file.bin", Method: "Put"})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("hello sftp")
	if _, err := w.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}

	if len(objects["file.bin"]) != len(data) {
		t.Fatalf("object not uploaded: %v", objects)
	}
}

func TestHandlersMkdir(t *testing.T) {
	objects := map[string][]byte{}
	b := newMockBackend(t, "bucket", "", objects)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)

	if err := h.Filecmd(&sftp.Request{Filepath: "/mock/dir", Method: "Mkdir"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := objects["dir/"]; !ok {
		t.Fatalf("placeholder not created: %v", objects)
	}
}

func TestHandlersSetstat(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)
	if err := h.Filecmd(&sftp.Request{Filepath: "/mock/file.bin", Method: "Setstat"}); err != nil {
		t.Fatal(err)
	}
}

func TestHandlersRenameCrossBackend(t *testing.T) {
	b1 := newMockBackend(t, "bucket1", "", map[string][]byte{"file.bin": []byte("cross-backend")})
	b2 := newMockBackend(t, "bucket2", "", nil)
	b1.Name = "a"
	b2.Name = "b"
	vfs := &VFS{Backends: map[string]*Backend{"a": b1, "b": b2}}
	h := NewS3Handlers(context.Background(), vfs, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)

	if err := h.Filecmd(&sftp.Request{
		Filepath: "/a/file.bin",
		Target:   "/b/file.bin",
		Method:   "Rename",
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// Verify the file arrived in the destination backend.
	out, err := b2.Client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &b2.Bucket,
		Key:    aws.String("file.bin"),
	})
	if err != nil {
		t.Fatalf("get dest: %v", err)
	}
	body, _ := io.ReadAll(out.Body)
	out.Body.Close()
	if !bytes.Equal(body, []byte("cross-backend")) {
		t.Fatalf("dest content %q, want %q", body, "cross-backend")
	}
}

func TestSanitizePath(t *testing.T) {
	if _, err := sanitizePath("../other"); err == nil {
		t.Fatal("expected error for path traversal above root")
	}
	p, err := sanitizePath("/backend/file")
	if err != nil || p != "/backend/file" {
		t.Fatalf("got %q, err=%v", p, err)
	}
	p, err = sanitizePath("/backend/file..txt")
	if err != nil || p != "/backend/file..txt" {
		t.Fatalf("got %q, err=%v", p, err)
	}
	p, err = sanitizePath("backend/../other")
	if err != nil || p != "other" {
		t.Fatalf("got %q, err=%v", p, err)
	}
}

func TestS3ReaderReadAt(t *testing.T) {
	objects := map[string][]byte{"file.bin": make([]byte, 26)}
	b := newMockBackend(t, "bucket", "", objects)
	r := &s3Reader{backend: b, key: "file.bin", size: 26, ctx: context.Background()}

	buf := make([]byte, 5)
	n, err := r.ReadAt(buf, 0)
	if err != nil || n != 5 {
		t.Fatalf("read failed: n=%d err=%v", n, err)
	}

	n, err = r.ReadAt(buf, 22)
	if err != nil || n != 4 {
		t.Fatalf("expected partial read, got n=%d err=%v", n, err)
	}

	_, err = r.ReadAt(buf, 26)
	if err != io.EOF {
		t.Fatalf("expected EOF at end, got %v", err)
	}
}

func TestS3ReaderMaxReadSize(t *testing.T) {
	objects := map[string][]byte{"file.bin": make([]byte, 26)}
	b := newMockBackend(t, "bucket", "", objects)
	r := &s3Reader{backend: b, key: "file.bin", size: 26, ctx: context.Background(), maxReadSize: 5}

	buf := make([]byte, 10)
	if _, err := r.ReadAt(buf, 0); err == nil {
		t.Fatal("expected error for read exceeding max_read_size")
	}

	buf = make([]byte, 5)
	if _, err := r.ReadAt(buf, 0); err != nil {
		t.Fatalf("expected read of max size to succeed: %v", err)
	}
}

func TestS3WriterMaxFileSize(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 10, 0, "", nil).FilePut.(*S3Handlers)

	w, err := h.Filewrite(&sftp.Request{Filepath: "/mock/file.bin", Method: "Put"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.WriteAt([]byte("hello"), 0); err != nil {
		t.Fatalf("small write failed: %v", err)
	}
	if _, err := w.WriteAt([]byte("world"), 8); err == nil {
		t.Fatal("expected error for write exceeding max_file_size")
	}
}

func TestHandlersListDirectoryEmptyPrefix(t *testing.T) {
	objects := map[string][]byte{
		"file1.bin":     make([]byte, 100),
		"dir/file2.bin": make([]byte, 200),
	}
	b := newMockBackend(t, "bucket", "", objects)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileList.(*S3Handlers)
	ctx := context.Background()

	infos, err := h.listDirectory(ctx, "/mock")
	if err != nil {
		t.Fatalf("list /mock: %v", err)
	}
	at := infos.(listerAt)
	buf := make([]os.FileInfo, 10)
	n, err := at.ListAt(buf, 0)
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
	_ = n

	got := []string{buf[0].Name(), buf[1].Name()}
	want := []string{"dir", "file1.bin"}
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestHandlersRenameCrossBackendDirectory(t *testing.T) {
	b1 := newMockBackend(t, "bucket1", "", map[string][]byte{
		"dir/file1.bin": []byte("f1"),
		"dir/file2.bin": []byte("f2"),
	})
	b2 := newMockBackend(t, "bucket2", "", nil)
	b1.Name = "a"
	b2.Name = "b"
	vfs := &VFS{Backends: map[string]*Backend{"a": b1, "b": b2}}
	h := NewS3Handlers(context.Background(), vfs, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)

	if err := h.Filecmd(&sftp.Request{
		Filepath: "/a/dir",
		Target:   "/b/dir",
		Method:   "Rename",
	}); err != nil {
		t.Fatalf("rename dir: %v", err)
	}

	for name, want := range map[string]string{"dir/file1.bin": "f1", "dir/file2.bin": "f2"} {
		out, err := b2.Client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: &b2.Bucket,
			Key:    aws.String(name),
		})
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		body, _ := io.ReadAll(out.Body)
		out.Body.Close()
		if string(body) != want {
			t.Fatalf("%s content %q, want %q", name, body, want)
		}
	}
}

func TestHandlersCopyWithinBackend(t *testing.T) {
	objects := map[string][]byte{"router/file.bin": []byte("copyme")}
	h := newTestHandlers(t, objects)

	if err := h.Filecmd(&sftp.Request{
		Filepath: "/mock/file.bin",
		Target:   "/mock/file2.bin",
		Method:   "Copy",
	}); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if string(objects["router/file.bin"]) != "copyme" {
		t.Fatalf("source was modified: %v", objects)
	}
	if string(objects["router/file2.bin"]) != "copyme" {
		t.Fatalf("destination missing/wrong: %v", objects)
	}
}

func TestHandlersCopyDirectoryWithinBackend(t *testing.T) {
	objects := map[string][]byte{
		"router/dir/file1.bin": []byte("a"),
		"router/dir/file2.bin": []byte("b"),
	}
	h := newTestHandlers(t, objects)

	if err := h.Filecmd(&sftp.Request{
		Filepath: "/mock/dir",
		Target:   "/mock/dir2",
		Method:   "Copy",
	}); err != nil {
		t.Fatalf("copy dir: %v", err)
	}
	for _, name := range []string{"router/dir2/file1.bin", "router/dir2/file2.bin"} {
		if _, ok := objects[name]; !ok {
			t.Fatalf("missing %s: %v", name, objects)
		}
	}
	if _, ok := objects["router/dir/file1.bin"]; !ok {
		t.Fatal("source directory was removed")
	}
}

func TestHandlersCopyCrossBackend(t *testing.T) {
	b1 := newMockBackend(t, "bucket1", "", map[string][]byte{"file.bin": []byte("cross-copy")})
	b2 := newMockBackend(t, "bucket2", "", nil)
	b1.Name = "a"
	b2.Name = "b"
	vfs := &VFS{Backends: map[string]*Backend{"a": b1, "b": b2}}
	h := NewS3Handlers(context.Background(), vfs, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)

	if err := h.Filecmd(&sftp.Request{
		Filepath: "/a/file.bin",
		Target:   "/b/file.bin",
		Method:   "Copy",
	}); err != nil {
		t.Fatalf("copy: %v", err)
	}

	out, err := b2.Client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &b2.Bucket,
		Key:    aws.String("file.bin"),
	})
	if err != nil {
		t.Fatalf("get dest: %v", err)
	}
	body, _ := io.ReadAll(out.Body)
	out.Body.Close()
	if string(body) != "cross-copy" {
		t.Fatalf("dest content %q, want %q", body, "cross-copy")
	}
}

func TestFilereadDirectoryError(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileGet.(*S3Handlers)

	_, err := h.Fileread(&sftp.Request{Filepath: "/mock", Method: "Get"})
	if err == nil {
		t.Fatal("expected error reading directory")
	}
}

func TestFilereadNotFound(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileGet.(*S3Handlers)

	_, err := h.Fileread(&sftp.Request{Filepath: "/mock/missing.bin", Method: "Get"})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestFilewriteDirectoryError(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FilePut.(*S3Handlers)

	_, err := h.Filewrite(&sftp.Request{Filepath: "/mock", Method: "Put"})
	if err == nil {
		t.Fatal("expected error writing directory")
	}
}

func TestHandlersCopyDirectoryTreeWithinBackend(t *testing.T) {
	objects := map[string][]byte{
		"router/dir/file1.bin": []byte("a"),
		"router/dir/file2.bin": []byte("b"),
	}
	h := newTestHandlers(t, objects)

	if err := h.Filecmd(&sftp.Request{
		Filepath: "/mock/dir",
		Target:   "/mock/dir2",
		Method:   "Copy",
	}); err != nil {
		t.Fatalf("copy dir: %v", err)
	}
	for _, name := range []string{"router/dir2/file1.bin", "router/dir2/file2.bin"} {
		if _, ok := objects[name]; !ok {
			t.Fatalf("missing %s: %v", name, objects)
		}
	}
}

func TestHandlersCopyCrossBackendMissingFile(t *testing.T) {
	b1 := newMockBackend(t, "bucket1", "", nil)
	b2 := newMockBackend(t, "bucket2", "", nil)
	b1.Name = "a"
	b2.Name = "b"
	vfs := &VFS{Backends: map[string]*Backend{"a": b1, "b": b2}}
	h := NewS3Handlers(context.Background(), vfs, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)

	err := h.Filecmd(&sftp.Request{
		Filepath: "/a/missing.bin",
		Target:   "/b/missing.bin",
		Method:   "Copy",
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestHandlersCopyCrossBackendEmptyDirectory(t *testing.T) {
	b1 := newMockBackend(t, "bucket1", "", map[string][]byte{"dir/": nil})
	b2 := newMockBackend(t, "bucket2", "", nil)
	b1.Name = "a"
	b2.Name = "b"
	vfs := &VFS{Backends: map[string]*Backend{"a": b1, "b": b2}}
	h := NewS3Handlers(context.Background(), vfs, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)

	if err := h.Filecmd(&sftp.Request{
		Filepath: "/a/dir",
		Target:   "/b/dir",
		Method:   "Copy",
	}); err != nil {
		t.Fatalf("copy empty dir: %v", err)
	}

	out, err := b2.Client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &b2.Bucket,
		Key:    aws.String("dir/"),
	})
	if err != nil {
		t.Fatalf("get dest dir/: %v", err)
	}
	out.Body.Close()
}

func TestHandlersCopyDirectoryTreeCrossBackend(t *testing.T) {
	b1 := newMockBackend(t, "bucket1", "", map[string][]byte{
		"dir/file1.bin": []byte("a"),
		"dir/file2.bin": []byte("b"),
	})
	b2 := newMockBackend(t, "bucket2", "", nil)
	b1.Name = "a"
	b2.Name = "b"
	vfs := &VFS{Backends: map[string]*Backend{"a": b1, "b": b2}}
	h := NewS3Handlers(context.Background(), vfs, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)

	if err := h.Filecmd(&sftp.Request{
		Filepath: "/a/dir",
		Target:   "/b/dir",
		Method:   "Copy",
	}); err != nil {
		t.Fatalf("copy dir: %v", err)
	}

	for _, name := range []string{"dir/file1.bin", "dir/file2.bin"} {
		out, err := b2.Client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: &b2.Bucket,
			Key:    aws.String(name),
		})
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		body, _ := io.ReadAll(out.Body)
		out.Body.Close()
		if len(body) == 0 {
			t.Fatalf("dest %s is empty", name)
		}
	}
}

func TestResolveRoot(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)

	backend, key, err := h.resolve("/")
	if err != nil {
		t.Fatal(err)
	}
	if backend != nil || key != "" {
		t.Fatalf("expected root, got backend=%v key=%q", backend, key)
	}
}

func TestResolveTraversalRejected(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)

	if _, _, err := h.resolve("/../etc/passwd"); err == nil {
		t.Fatal("expected traversal error")
	}
}

func TestFilewriteWithRateLimiter(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	lim := newUserRateLimiter(1024)
	h := NewS3Handlers(context.Background(), &VFS{Backends: map[string]*Backend{b.Name: b}}, "testuser", "127.0.0.1", allPermissions(), lim, 0, 0, "", nil).FilePut.(*S3Handlers)

	w, err := h.Filewrite(&sftp.Request{Filepath: "/mock/file.bin", Method: "Put"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w.(*rateLimitedWriter); !ok {
		t.Fatalf("expected rateLimitedWriter, got %T", w)
	}
}

func TestFilecmdSetstatAndUnsupported(t *testing.T) {
	h := newTestHandlers(t, nil)
	if err := h.Filecmd(&sftp.Request{Filepath: "/mock/file.bin", Method: "Setstat"}); err != nil {
		t.Fatalf("Setstat: %v", err)
	}
	if err := h.Filecmd(&sftp.Request{Filepath: "/mock/file.bin", Method: "Symlink"}); err != sftp.ErrSSHFxOpUnsupported {
		t.Fatalf("expected unsupported, got %v", err)
	}
}

func TestHandlersCopyMissingDirectory(t *testing.T) {
	h := newTestHandlers(t, nil)
	err := h.Filecmd(&sftp.Request{
		Filepath: "/mock/dir",
		Target:   "/mock/dir2",
		Method:   "Copy",
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestHandlersRenameMissingDirectory(t *testing.T) {
	h := newTestHandlers(t, nil)
	err := h.Filecmd(&sftp.Request{
		Filepath: "/mock/dir",
		Target:   "/mock/dir2",
		Method:   "Rename",
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestHandlersStatExistingDirectory(t *testing.T) {
	objects := map[string][]byte{
		"router/dir/file1.bin": []byte("a"),
	}
	h := newTestListHandlers(t, objects)
	infos, err := h.statPath(context.Background(), "/mock/dir")
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	fi := infos.(listerAt)
	buf := make([]os.FileInfo, 1)
	if n, _ := fi.ListAt(buf, 0); n != 1 || !buf[0].IsDir() {
		t.Fatalf("expected directory entry, got %v", buf)
	}
}

func TestHandlersRenameDirectoryCrossBackend(t *testing.T) {
	b1 := newMockBackend(t, "bucket1", "", map[string][]byte{
		"dir/file1.bin": []byte("a"),
		"dir/file2.bin": []byte("b"),
	})
	b2 := newMockBackend(t, "bucket2", "", nil)
	b1.Name = "a"
	b2.Name = "b"
	vfs := &VFS{Backends: map[string]*Backend{"a": b1, "b": b2}}
	h := NewS3Handlers(context.Background(), vfs, "testuser", "127.0.0.1", allPermissions(), nil, 0, 0, "", nil).FileCmd.(*S3Handlers)

	if err := h.Filecmd(&sftp.Request{
		Filepath: "/a/dir",
		Target:   "/b/dir",
		Method:   "Rename",
	}); err != nil {
		t.Fatalf("rename dir: %v", err)
	}

	for _, name := range []string{"dir/file1.bin", "dir/file2.bin"} {
		_, err := b1.Client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: &b1.Bucket,
			Key:    aws.String(name),
		})
		if err == nil {
			t.Fatalf("expected source %s to be deleted", name)
		}
		out, err := b2.Client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: &b2.Bucket,
			Key:    aws.String(name),
		})
		if err != nil {
			t.Fatalf("get dest %s: %v", name, err)
		}
		out.Body.Close()
	}
}

func TestHandlersRenameDirectory(t *testing.T) {
	objects := map[string][]byte{
		"router/old/file1.bin": make([]byte, 100),
		"router/old/file2.bin": make([]byte, 200),
	}
	h := newTestHandlers(t, objects)

	err := h.Filecmd(&sftp.Request{
		Filepath: "/mock/old",
		Target:   "/mock/new",
		Method:   "Rename",
	})
	if err != nil {
		t.Fatalf("rename dir: %v", err)
	}
	if _, ok := objects["router/old/file1.bin"]; ok {
		t.Fatal("old dir still exists")
	}
	if len(objects["router/new/file1.bin"]) != 100 || len(objects["router/new/file2.bin"]) != 200 {
		t.Fatalf("objects not copied: %v", objects)
	}
}
