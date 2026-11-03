package storagetest

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestNewS3ClientRoundTrip(t *testing.T) {
	objects := map[string][]byte{
		"file.bin": []byte("hello"),
	}
	client := NewS3Client(t, objects, false)
	out, err := client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String("bucket"),
		Key:    aws.String("file.bin"),
	})
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	body, _ := io.ReadAll(out.Body)
	out.Body.Close()
	if string(body) != "hello" {
		t.Fatalf("body=%q, want hello", body)
	}
}

func TestFakeS3HeadObject(t *testing.T) {
	fake := NewFakeS3(t, map[string][]byte{"file.bin": []byte("hello")})
	resp, err := fake.Do(newRequest(t, "HEAD", "/bucket/file.bin", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Length") != "5" {
		t.Fatalf("length=%q", resp.Header.Get("Content-Length"))
	}
}

func TestFakeS3HeadObjectMissing(t *testing.T) {
	fake := NewFakeS3(t, nil)
	resp, err := fake.Do(newRequest(t, "HEAD", "/bucket/missing.bin", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestFakeS3GetObjectMissing(t *testing.T) {
	fake := NewFakeS3(t, nil)
	resp, err := fake.Do(newRequest(t, "GET", "/bucket/missing.bin", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestFakeS3DeleteObject(t *testing.T) {
	fake := NewFakeS3(t, map[string][]byte{"file.bin": []byte("x")})
	resp, err := fake.Do(newRequest(t, "DELETE", "/bucket/file.bin", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 204 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if _, ok := fake.objects["file.bin"]; ok {
		t.Fatal("object was not deleted")
	}
}

func TestFakeS3CopyObject(t *testing.T) {
	fake := NewFakeS3(t, map[string][]byte{"src.bin": []byte("copy")})
	req := newRequest(t, "PUT", "/bucket/dst.bin", nil)
	req.Header.Set("X-Amz-Copy-Source", "/bucket/src.bin")
	resp, err := fake.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if string(fake.objects["dst.bin"]) != "copy" {
		t.Fatalf("copy=%q", fake.objects["dst.bin"])
	}
}

func TestFakeS3CopyObjectMissing(t *testing.T) {
	fake := NewFakeS3(t, nil)
	req := newRequest(t, "PUT", "/bucket/dst.bin", nil)
	req.Header.Set("X-Amz-Copy-Source", "/bucket/missing.bin")
	resp, err := fake.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestFakeS3PutObject(t *testing.T) {
	fake := NewFakeS3(t, nil)
	resp, err := fake.Do(newRequest(t, "PUT", "/bucket/file.bin", strings.NewReader("data")))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if string(fake.objects["file.bin"]) != "data" {
		t.Fatalf("data=%q", fake.objects["file.bin"])
	}
}

func TestFakeS3ListObjects(t *testing.T) {
	fake := NewFakeS3(t, map[string][]byte{
		"dir/file1.bin": []byte("a"),
		"dir/file2.bin": []byte("b"),
	})
	resp, err := fake.Do(newRequest(t, "GET", "/bucket/?list-type=2&prefix=dir/&delimiter=/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "file1.bin") {
		t.Fatalf("missing file1 in %q", body)
	}
}

func TestFakeS3DeleteObjects(t *testing.T) {
	fake := NewFakeS3(t, map[string][]byte{
		"a.bin": []byte("1"),
		"b.bin": []byte("2"),
	})
	body := strings.NewReader(`<Delete><Object><Key>a.bin</Key></Object><Object><Key>b.bin</Key></Object></Delete>`)
	resp, err := fake.Do(newRequest(t, "POST", "/bucket/?delete", body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if len(fake.objects) != 0 {
		t.Fatalf("expected 0 objects, got %d", len(fake.objects))
	}
}

func TestFakeS3Failing(t *testing.T) {
	client := NewS3Client(t, map[string][]byte{"file.bin": []byte("x")}, true)
	_, err := client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String("bucket"),
		Key:    aws.String("file.bin"),
	})
	if err == nil {
		t.Fatal("expected error from failing client")
	}
}

func TestParseS3Path(t *testing.T) {
	cases := []struct {
		path, bucket, key string
	}{
		{"/bucket/key", "bucket", "key"},
		{"/bucket", "bucket", ""},
		{"/", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		b, k := parseS3Path(c.path)
		if b != c.bucket || k != c.key {
			t.Fatalf("parseS3Path(%q)=%q,%q want %q,%q", c.path, b, k, c.bucket, c.key)
		}
	}
}

func TestXMLEscape(t *testing.T) {
	if got := xmlEscape("<>&'\""); got != "&lt;&gt;&amp;&#39;&#34;" {
		t.Fatalf("xmlEscape=%q", got)
	}
}

func newRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
