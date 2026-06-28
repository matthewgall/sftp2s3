package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakeS3 is an in-memory S3 implementation used to test the SFTP handlers.
type fakeS3 struct {
	t       *testing.T
	objects map[string][]byte // key -> content
	fail    bool              // when true, all requests return HTTP 500
}

func newFakeS3(t *testing.T, objects map[string][]byte) *fakeS3 {
	if objects == nil {
		objects = map[string][]byte{}
	}
	return &fakeS3{t: t, objects: objects}
}

// newFakeS3FromSizes creates a fakeS3 with zero-filled objects of the given sizes.
func newFakeS3FromSizes(t *testing.T, sizes map[string]int64) *fakeS3 {
	objects := make(map[string][]byte, len(sizes))
	for k, size := range sizes {
		objects[k] = make([]byte, size)
	}
	return newFakeS3(t, objects)
}

func (f *fakeS3) Do(req *http.Request) (*http.Response, error) {
	if f.fail {
		return emptyHTTPResponse(500), nil
	}

	bucket, key := parseS3Path(req.URL.Path)
	_ = bucket

	switch req.Method {
	case "GET":
		if req.URL.Query().Get("list-type") == "2" {
			return f.listResponse(bucket, req.URL.Query().Get("prefix"), req.URL.Query().Get("delimiter")), nil
		}
		return f.getObject(key)

	case "HEAD":
		return f.headObject(key)

	case "DELETE":
		delete(f.objects, key)
		return emptyHTTPResponse(204), nil

	case "POST":
		if req.URL.Query().Has("delete") {
			return f.deleteObjects(req.Body)
		}
		return emptyHTTPResponse(400), nil

	case "PUT":
		if src := req.Header.Get("X-Amz-Copy-Source"); src != "" {
			return f.copyObject(src, key)
		}
		var data []byte
		if req.Body != nil {
			data, _ = io.ReadAll(req.Body)
		}
		f.objects[key] = data
		return emptyHTTPResponse(200), nil

	default:
		return emptyHTTPResponse(400), nil
	}
}

func (f *fakeS3) listResponse(bucket, prefix, delimiter string) *http.Response {
	var contents []string
	var prefixes []string
	seen := make(map[string]bool)

	for k, data := range f.objects {
		if prefix != "" && !strings.HasPrefix(k, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(k, prefix)
		if delimiter == "" {
			contents = append(contents, objectXML(k, int64(len(data))))
			continue
		}
		idx := strings.Index(suffix, delimiter)
		if idx >= 0 {
			dir := prefix + suffix[:idx+len(delimiter)]
			if !seen[dir] {
				seen[dir] = true
				prefixes = append(prefixes, commonPrefixXML(dir))
			}
			continue
		}
		contents = append(contents, objectXML(k, int64(len(data))))
	}

	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>%s</Name>
  <Prefix>%s</Prefix>
  <MaxKeys>1000</MaxKeys>
  <IsTruncated>false</IsTruncated>
  <KeyCount>%d</KeyCount>
  %s
  %s
</ListBucketResult>`, xmlEscape(bucket), xmlEscape(prefix), len(contents)+len(prefixes), strings.Join(contents, "\n  "), strings.Join(prefixes, "\n  "))

	return xmlHTTPResponse(200, body)
}

func (f *fakeS3) headObject(key string) (*http.Response, error) {
	data, ok := f.objects[key]
	if !ok {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Length": []string{fmt.Sprintf("%d", len(data))},
			"Last-Modified":  []string{"Mon, 01 Jan 2024 00:00:00 GMT"},
		},
		Body: io.NopCloser(strings.NewReader("")),
	}, nil
}

func (f *fakeS3) getObject(key string) (*http.Response, error) {
	data, ok := f.objects[key]
	if !ok {
		body := `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code></Error>`
		return xmlHTTPResponse(404, body), nil
	}
	return &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Length": []string{fmt.Sprintf("%d", len(data))},
			"Last-Modified":  []string{"Mon, 01 Jan 2024 00:00:00 GMT"},
		},
		Body: io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func (f *fakeS3) deleteObjects(body io.ReadCloser) (*http.Response, error) {
	defer body.Close()
	var del struct {
		Objects []struct {
			Key string `xml:"Key"`
		} `xml:"Object"`
	}
	if err := xml.NewDecoder(body).Decode(&del); err != nil {
		return emptyHTTPResponse(400), nil
	}
	var deleted []string
	for _, obj := range del.Objects {
		delete(f.objects, obj.Key)
		deleted = append(deleted, fmt.Sprintf("<Deleted><Key>%s</Key></Deleted>", xmlEscape(obj.Key)))
	}
	resp := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  %s
</DeleteResult>`, strings.Join(deleted, "\n  "))
	return xmlHTTPResponse(200, resp), nil
}

func (f *fakeS3) copyObject(src, dst string) (*http.Response, error) {
	src = strings.TrimPrefix(src, "/")
	parts := strings.SplitN(src, "/", 2)
	if len(parts) != 2 {
		return emptyHTTPResponse(400), nil
	}
	srcKey, err := url.PathUnescape(parts[1])
	if err != nil {
		return emptyHTTPResponse(400), nil
	}
	data, ok := f.objects[srcKey]
	if !ok {
		return emptyHTTPResponse(404), nil
	}
	f.objects[dst] = bytes.Clone(data)
	return xmlHTTPResponse(200, `<?xml version="1.0" encoding="UTF-8"?>
<CopyObjectResult><ETag>"etag"</ETag></CopyObjectResult>`), nil
}

// newMockBackend creates a backend backed by fakeS3 with the given object contents.
// newFailingBackend creates a backend whose S3 requests always return HTTP 500.
func newFailingBackend(t *testing.T, bucket, prefix string) *Backend {
	t.Helper()
	fake := newFakeS3(t, nil)
	fake.fail = true
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("ak", "sk", "")),
		awsconfig.WithHTTPClient(fake),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = strPtr("http://s3mock")
		o.UsePathStyle = true
		o.Retryer = aws.NopRetryer{}
	})
	return &Backend{
		Name:     "mock",
		Bucket:   bucket,
		Prefix:   prefix,
		Client:   client,
		Uploader: manager.NewUploader(client),
		PartSize: 8 * 1024 * 1024,
		Timeout:  5 * time.Second,
	}
}

func newMockBackend(t *testing.T, bucket, prefix string, content map[string][]byte) *Backend {
	t.Helper()
	fake := newFakeS3(t, content)
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("ak", "sk", "")),
		awsconfig.WithHTTPClient(fake),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = strPtr("http://s3mock")
		o.UsePathStyle = true
	})
	return &Backend{
		Name:     "mock",
		Bucket:   bucket,
		Prefix:   prefix,
		Client:   client,
		Uploader: manager.NewUploader(client),
		PartSize: 8 * 1024 * 1024,
		Timeout:  5 * time.Second,
	}
}

func parseS3Path(path string) (bucket, key string) {
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 {
		return "", ""
	}
	bucket = parts[0]
	if len(parts) == 2 {
		key = parts[1]
	}
	return bucket, key
}

func objectXML(key string, size int64) string {
	return fmt.Sprintf(`<Contents>
    <Key>%s</Key>
    <Size>%d</Size>
    <LastModified>2024-01-01T00:00:00.000Z</LastModified>
    <ETag>"etag"</ETag>
  </Contents>`, xmlEscape(key), size)
}

func commonPrefixXML(prefix string) string {
	return fmt.Sprintf(`<CommonPrefixes>
    <Prefix>%s</Prefix>
  </CommonPrefixes>`, xmlEscape(prefix))
}

func xmlHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/xml"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func emptyHTTPResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func strPtr(s string) *string { return &s }
