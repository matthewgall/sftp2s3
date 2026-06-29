package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/pkg/sftp"
	"golang.org/x/time/rate"
)

// S3Handlers implements the pkg/sftp request handler interfaces.
type S3Handlers struct {
	vfs         *VFS
	user        string
	remote      string
	perms       UserPermissions
	ctx         context.Context
	rateLimiter *rate.Limiter
	maxFileSize int64
	maxReadSize int64
	cacheDir    string
	metrics     *Metrics
}

// NewS3Handlers returns an sftp.Handlers backed by the supplied VFS.
func NewS3Handlers(ctx context.Context, vfs *VFS, user, remote string, perms UserPermissions, rateLimiter *rate.Limiter, maxFileSize, maxReadSize int64, cacheDir string, metrics *Metrics) sftp.Handlers {
	h := &S3Handlers{
		vfs: vfs, user: user, remote: remote, perms: perms, ctx: ctx,
		rateLimiter: rateLimiter, maxFileSize: maxFileSize, maxReadSize: maxReadSize,
		cacheDir: cacheDir, metrics: metrics,
	}
	return sftp.Handlers{
		FileGet:  h,
		FilePut:  h,
		FileCmd:  h,
		FileList: h,
	}
}

// recordS3Op records the result of an S3 operation, if metrics are enabled.
func (h *S3Handlers) recordS3Op(op string, backend *Backend, start time.Time, err error) {
	if h.metrics != nil {
		h.metrics.ObserveS3Op(op, backend.Name, start, err)
	}
}

// timedS3Op runs fn and records its duration and success/failure as an S3
// operation named op for backend b.
func timedS3Op[T any](h *S3Handlers, op string, b *Backend, fn func() (T, error)) (T, error) {
	start := time.Now()
	v, err := fn()
	h.recordS3Op(op, b, start, err)
	return v, err
}

// objectExists is a metrics-aware wrapper around s3ObjectExists.
func (h *S3Handlers) objectExists(ctx context.Context, b *Backend, key string) (bool, error) {
	return timedS3Op(h, "HeadObject", b, func() (bool, error) {
		return s3ObjectExists(ctx, b, key)
	})
}

// prefixHasEntries is a metrics-aware wrapper around s3PrefixHasEntries.
func (h *S3Handlers) prefixHasEntries(ctx context.Context, b *Backend, prefix string) (bool, error) {
	return timedS3Op(h, "ListObjectsV2", b, func() (bool, error) {
		return s3PrefixHasEntries(ctx, b, prefix)
	})
}

// deleteObject is a metrics-aware wrapper around s3DeleteObject.
func (h *S3Handlers) deleteObject(ctx context.Context, b *Backend, key string) error {
	_, err := timedS3Op(h, "DeleteObject", b, func() (struct{}, error) {
		return struct{}{}, s3DeleteObject(ctx, b, key)
	})
	return err
}

// deletePrefix is a metrics-aware wrapper around s3DeletePrefix.
func (h *S3Handlers) deletePrefix(ctx context.Context, b *Backend, prefix string) error {
	_, err := timedS3Op(h, "DeleteObjects", b, func() (struct{}, error) {
		return struct{}{}, s3DeletePrefix(ctx, b, prefix)
	})
	return err
}

// copyObject is a metrics-aware wrapper around s3CopyObject.
func (h *S3Handlers) copyObject(ctx context.Context, b *Backend, src, dst string) error {
	_, err := timedS3Op(h, "CopyObject", b, func() (struct{}, error) {
		return struct{}{}, s3CopyObject(ctx, b, src, dst)
	})
	return err
}

// nextListPage is a metrics-aware wrapper around paginator.NextPage.
func (h *S3Handlers) nextListPage(paginator *s3.ListObjectsV2Paginator, ctx context.Context, b *Backend) (*s3.ListObjectsV2Output, error) {
	return timedS3Op(h, "ListObjectsV2", b, func() (*s3.ListObjectsV2Output, error) {
		return paginator.NextPage(ctx)
	})
}

// logRequest emits a debug log entry for an SFTP request.
func (h *S3Handlers) logRequest(r *sftp.Request) {
	slog.Debug("sftp request",
		"remote", h.remote,
		"user", h.user,
		"method", r.Method,
		"path", r.Filepath,
		"target", r.Target,
	)
}

func withBackendTimeout(ctx context.Context, b *Backend) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, b.Timeout)
}

// sanitizePath normalizes p and rejects any traversal outside the virtual root.
func sanitizePath(p string) (string, error) {
	cleaned := path.Clean(p)
	for _, part := range strings.Split(cleaned, "/") {
		if part == ".." {
			slog.Debug("sanitizePath rejected traversal", "path", p)
			return "", fmt.Errorf("invalid path")
		}
	}
	slog.Debug("sanitizePath", "in", p, "out", cleaned)
	return cleaned, nil
}

// resolve sanitizes and resolves p against the handler's VFS.
func (h *S3Handlers) resolve(p string) (*Backend, string, error) {
	p, err := sanitizePath(p)
	if err != nil {
		return nil, "", err
	}
	b, key, err := h.vfs.Resolve(p)
	if err != nil {
		slog.Debug("vfs resolve failed", "path", p, "error", err)
		return nil, "", err
	}
	if b == nil {
		slog.Debug("resolved to virtual root", "path", p)
	} else {
		slog.Debug("resolved path", "path", p, "backend", b.Name, "key", key)
	}
	return b, key, nil
}

// s3Reader implements io.ReaderAt by fetching byte ranges from S3 on demand.
type s3Reader struct {
	backend     *Backend
	key         string
	size        int64
	ctx         context.Context
	metrics     *Metrics
	maxReadSize int64
}

// ReadAt reads len(p) bytes starting at off from the S3 object.
func (r *s3Reader) ReadAt(p []byte, off int64) (int, error) {
	if r.maxReadSize > 0 && int64(len(p)) > r.maxReadSize {
		return 0, fmt.Errorf("read exceeds maximum allowed size")
	}
	if off >= r.size {
		return 0, io.EOF
	}
	end := off + int64(len(p)) - 1
	if end >= r.size {
		end = r.size - 1
	}
	if end < off {
		return 0, io.EOF
	}

	ctx, cancel := context.WithTimeout(r.ctx, r.backend.Timeout)
	defer cancel()

	start := time.Now()
	out, err := r.backend.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.backend.Bucket),
		Key:    aws.String(r.key),
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", off, end)),
	})
	if r.metrics != nil {
		r.metrics.ObserveS3Op("GetObject", r.backend.Name, start, err)
	}
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return 0, os.ErrNotExist
		}
		return 0, err
	}
	defer out.Body.Close()

	n, err := io.ReadFull(out.Body, p[:end-off+1])
	if r.metrics != nil && n > 0 {
		r.metrics.AddDownloadBytes(int64(n))
	}
	slog.Debug("s3 read range", "backend", r.backend.Name, "key", r.key, "off", off, "requested", len(p), "read", n)
	if err == io.ErrUnexpectedEOF {
		return n, io.EOF
	}
	return n, err
}

// Fileread handles SFTP download requests.
func (h *S3Handlers) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	h.logRequest(r)
	if err := h.requireRead(); err != nil {
		return nil, err
	}
	if r.Method != "Get" {
		return nil, sftp.ErrSSHFxOpUnsupported
	}
	b, key, err := h.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	if b == nil || key == "" {
		return nil, fmt.Errorf("cannot read directory")
	}

	ctx, cancel := withBackendTimeout(h.ctx, b)
	defer cancel()

	head, err := timedS3Op(h, "HeadObject", b, func() (*s3.HeadObjectOutput, error) {
		return b.Client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(b.Bucket),
			Key:    aws.String(b.objectKey(key)),
		})
	})
	if err != nil {
		var notFound *types.NotFound
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &notFound) || errors.As(err, &noSuchKey) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}

	reader := &s3Reader{
		backend:     b,
		key:         b.objectKey(key),
		size:        aws.ToInt64(head.ContentLength),
		ctx:         h.ctx,
		metrics:     h.metrics,
		maxReadSize: h.maxReadSize,
	}
	slog.Debug("Fileread opened", "path", r.Filepath, "backend", b.Name, "key", reader.key, "size", reader.size)
	if h.rateLimiter != nil {
		return &rateLimitedReader{ReaderAt: reader, lim: h.rateLimiter}, nil
	}
	return reader, nil
}

// s3Writer buffers SFTP writes to a temp file and uploads the result to S3 on
// close.
type s3Writer struct {
	backend     *Backend
	key         string
	file        *os.File
	size        int64
	ctx         context.Context
	metrics     *Metrics
	mu          sync.Mutex
	closed      bool
	maxFileSize int64
}

// WriteAt writes data to the temp file at the given offset.
func (w *s3Writer) WriteAt(p []byte, off int64) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.maxFileSize > 0 && off+int64(len(p)) > w.maxFileSize {
		return 0, fmt.Errorf("file exceeds maximum allowed size")
	}

	n, err := w.file.WriteAt(p, off)
	if err != nil {
		return n, err
	}
	if end := off + int64(n); end > w.size {
		w.size = end
	}
	return n, nil
}

// Close uploads the buffered file to S3.
func (w *s3Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	defer os.Remove(w.file.Name())

	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(w.ctx, w.backend.Timeout)
	defer cancel()

	start := time.Now()
	_, err := w.backend.Uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(w.backend.Bucket),
		Key:    aws.String(w.key),
		Body:   w.file,
	})
	if w.metrics != nil {
		w.metrics.ObserveS3Op("PutObject", w.backend.Name, start, err)
		if err == nil {
			w.metrics.AddUploadBytes(w.size)
		}
	}
	if err != nil {
		slog.Error("s3 upload failed", "backend", w.backend.Name, "key", w.key, "size", w.size, "error", err)
		return err
	}
	slog.Debug("s3 upload complete", "backend", w.backend.Name, "key", w.key, "size", w.size)
	return nil
}

// Filewrite handles SFTP upload requests.
func (h *S3Handlers) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	h.logRequest(r)
	if err := h.requireWrite(); err != nil {
		slog.Debug("Filewrite denied", "path", r.Filepath, "error", err)
		return nil, err
	}
	if r.Method != "Put" {
		return nil, sftp.ErrSSHFxOpUnsupported
	}
	b, key, err := h.resolve(r.Filepath)
	if err != nil {
		slog.Debug("Filewrite resolve failed", "path", r.Filepath, "error", err)
		return nil, err
	}
	if b == nil || key == "" {
		slog.Debug("Filewrite cannot write directory", "path", r.Filepath)
		return nil, fmt.Errorf("cannot write directory")
	}

	tmp, err := os.CreateTemp(h.cacheDir, "sftp2s3-write-*")
	if err != nil {
		slog.Error("failed to create upload temp file", "dir", h.cacheDir, "error", err)
		return nil, err
	}
	writer := &s3Writer{
		backend:     b,
		key:         b.objectKey(key),
		file:        tmp,
		ctx:         h.ctx,
		metrics:     h.metrics,
		maxFileSize: h.maxFileSize,
	}
	slog.Debug("Filewrite opened", "path", r.Filepath, "backend", b.Name, "key", writer.key, "temp", tmp.Name())
	if h.rateLimiter != nil {
		return &rateLimitedWriter{WriterAt: writer, lim: h.rateLimiter}, nil
	}
	return writer, nil
}

// Filecmd handles SFTP file commands such as rename, remove, and mkdir.
func (h *S3Handlers) Filecmd(r *sftp.Request) error {
	h.logRequest(r)
	baseCtx := h.ctx

	switch r.Method {
	case "Setstat":
		slog.Debug("Setstat ignored", "path", r.Filepath)
		return nil

	case "Rename":
		if err := h.handleRename(baseCtx, r.Filepath, r.Target); err != nil {
			slog.Debug("Rename failed", "from", r.Filepath, "to", r.Target, "error", err)
			return err
		}
		slog.Debug("Rename complete", "from", r.Filepath, "to", r.Target)
		return nil

	case "Copy":
		if err := h.requireRead(); err != nil {
			return err
		}
		if err := h.requireWrite(); err != nil {
			return err
		}
		if err := h.handleCopy(baseCtx, r.Filepath, r.Target); err != nil {
			slog.Debug("Copy failed", "from", r.Filepath, "to", r.Target, "error", err)
			return err
		}
		slog.Debug("Copy complete", "from", r.Filepath, "to", r.Target)
		return nil

	case "Rmdir", "Remove":
		if err := h.requireDelete(); err != nil {
			return err
		}
		b, key, err := h.resolve(r.Filepath)
		if err != nil {
			return err
		}
		if b == nil || key == "" {
			return fmt.Errorf("cannot remove root")
		}
		ctx, cancel := withBackendTimeout(baseCtx, b)
		defer cancel()

		objKey := b.objectKey(key)
		exists, err := h.objectExists(ctx, b, objKey)
		if err != nil {
			return err
		}
		hasChildren, err := h.prefixHasEntries(ctx, b, b.dirPrefix(key))
		if err != nil {
			return err
		}
		if !exists && !hasChildren {
			return os.ErrNotExist
		}

		if exists {
			if err := h.deleteObject(ctx, b, objKey); err != nil {
				slog.Debug("Remove object failed", "path", r.Filepath, "key", objKey, "error", err)
				return err
			}
		}
		if hasChildren {
			if err := h.deletePrefix(ctx, b, b.dirPrefix(key)); err != nil {
				slog.Debug("Remove prefix failed", "path", r.Filepath, "prefix", b.dirPrefix(key), "error", err)
				return err
			}
		}
		slog.Debug("Remove complete", "path", r.Filepath, "object", exists, "children", hasChildren)
		return nil

	case "Mkdir":
		if err := h.requireWrite(); err != nil {
			return err
		}
		b, key, err := h.resolve(r.Filepath)
		if err != nil {
			return err
		}
		if b == nil {
			return fmt.Errorf("cannot create top-level folders")
		}
		if key == "" {
			return nil
		}
		ctx, cancel := withBackendTimeout(h.ctx, b)
		defer cancel()

		_, err = timedS3Op(h, "PutObject", b, func() (*s3.PutObjectOutput, error) {
			return b.Client.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String(b.Bucket),
				Key:    aws.String(b.dirPrefix(key)),
				Body:   bytes.NewReader(nil),
			})
		})
		if err != nil {
			slog.Debug("Mkdir failed", "path", r.Filepath, "key", b.dirPrefix(key), "error", err)
			return err
		}
		slog.Debug("Mkdir complete", "path", r.Filepath, "key", b.dirPrefix(key))
		return nil

	default:
		return sftp.ErrSSHFxOpUnsupported
	}
}

// handleRename moves oldPath to newPath. It supports both same-backend copies
// (server-side CopyObject) and cross-backend copies (download + re-upload).
func (h *S3Handlers) handleRename(ctx context.Context, oldPath, newPath string) error {
	oldBackend, oldKey, err := h.resolve(oldPath)
	if err != nil {
		return err
	}
	newBackend, newKey, err := h.resolve(newPath)
	if err != nil {
		return err
	}
	if oldBackend == nil || newBackend == nil {
		return fmt.Errorf("cannot rename root")
	}

	// Renaming removes the source and creates the destination.
	if err := h.requireDelete(); err != nil {
		return err
	}
	if err := h.requireWrite(); err != nil {
		return err
	}
	crossBackend := oldBackend.Name != newBackend.Name
	if crossBackend {
		// Cross-backend copies are performed by streaming through the server.
		if err := h.requireRead(); err != nil {
			return err
		}
	}

	if crossBackend {
		err = h.crossBackendRename(ctx, oldBackend, oldKey, newBackend, newKey)
	} else {
		err = h.sameBackendRename(ctx, oldBackend, oldKey, newKey)
	}
	if err != nil {
		return err
	}
	slog.Debug("Rename complete", "from", oldPath, "to", newPath, "cross_backend", crossBackend)
	return nil
}

// sameBackendRename renames within a single S3 backend using CopyObject.
func (h *S3Handlers) sameBackendRename(ctx context.Context, b *Backend, oldKey, newKey string) error {
	if err := h.sameBackendCopy(ctx, b, oldKey, newKey); err != nil {
		return err
	}

	ctx, cancel := withBackendTimeout(ctx, b)
	defer cancel()

	oldObjKey := b.objectKey(oldKey)
	isFile, err := h.objectExists(ctx, b, oldObjKey)
	if err != nil {
		return err
	}
	if isFile {
		return h.deleteObject(ctx, b, oldObjKey)
	}
	return h.deletePrefix(ctx, b, b.dirPrefix(oldKey))
}

// sameBackendCopy copies oldKey to newKey within a single S3 backend using
// server-side CopyObject.
func (h *S3Handlers) sameBackendCopy(ctx context.Context, b *Backend, oldKey, newKey string) error {
	ctx, cancel := withBackendTimeout(ctx, b)
	defer cancel()

	oldObjKey := b.objectKey(oldKey)
	newObjKey := b.objectKey(newKey)

	isFile, err := h.objectExists(ctx, b, oldObjKey)
	if err != nil {
		return err
	}

	if isFile {
		return h.copyObject(ctx, b, oldObjKey, newObjKey)
	}

	oldDirPrefix := b.dirPrefix(oldKey)
	hasChildren, err := h.prefixHasEntries(ctx, b, oldDirPrefix)
	if err != nil {
		return err
	}

	if !hasChildren {
		// Copy a directory placeholder object, if one exists.
		exists, err := h.objectExists(ctx, b, oldDirPrefix)
		if err != nil {
			return err
		}
		if !exists {
			return os.ErrNotExist
		}
		newDirPrefix := b.dirPrefix(newKey)
		return h.copyObject(ctx, b, oldDirPrefix, newDirPrefix)
	}

	// Copy a non-empty directory by copying every object to the new prefix.
	newDirPrefix := b.dirPrefix(newKey)
	paginator := s3.NewListObjectsV2Paginator(b.Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.Bucket),
		Prefix: aws.String(oldDirPrefix),
	})
	for paginator.HasMorePages() {
		page, err := h.nextListPage(paginator, ctx, b)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			srcKey := aws.ToString(obj.Key)
			suffix := strings.TrimPrefix(srcKey, oldDirPrefix)
			dstKey := newDirPrefix + suffix
			if err := h.copyObject(ctx, b, srcKey, dstKey); err != nil {
				return err
			}
		}
	}
	return nil
}

// crossBackendRename copies an object or tree from one backend to another by
// streaming through sftp2s3.
func (h *S3Handlers) crossBackendRename(ctx context.Context, srcB *Backend, srcKey string, dstB *Backend, dstKey string) error {
	if err := h.crossBackendCopy(ctx, srcB, srcKey, dstB, dstKey); err != nil {
		return err
	}

	srcObjKey := srcB.objectKey(srcKey)
	ctx, cancel := withBackendTimeout(ctx, srcB)
	defer cancel()
	isFile, err := h.objectExists(ctx, srcB, srcObjKey)
	if err != nil {
		return err
	}
	if isFile {
		return h.deleteObject(ctx, srcB, srcObjKey)
	}
	return h.deletePrefix(ctx, srcB, srcB.dirPrefix(srcKey))
}

// crossBackendCopy copies an object or tree from one backend to another by
// streaming through sftp2s3.
func (h *S3Handlers) crossBackendCopy(ctx context.Context, srcB *Backend, srcKey string, dstB *Backend, dstKey string) error {
	srcObjKey := srcB.objectKey(srcKey)
	dstObjKey := dstB.objectKey(dstKey)

	srcCtx, srcCancel := withBackendTimeout(ctx, srcB)
	isFile, err := h.objectExists(srcCtx, srcB, srcObjKey)
	srcCancel()
	if err != nil {
		return err
	}

	if isFile {
		return h.streamCopyObject(ctx, srcB, srcObjKey, dstB, dstObjKey)
	}

	srcDirPrefix := srcB.dirPrefix(srcKey)
	hasChildren, err := h.prefixHasEntries(ctx, srcB, srcDirPrefix)
	if err != nil {
		return err
	}

	if !hasChildren {
		exists, err := h.objectExists(ctx, srcB, srcDirPrefix)
		if err != nil {
			return err
		}
		if !exists {
			return os.ErrNotExist
		}
		dstDirPrefix := dstB.dirPrefix(dstKey)
		return h.streamCopyObject(ctx, srcB, srcDirPrefix, dstB, dstDirPrefix)
	}

	// Copy every object under the source tree to the destination backend.
	dstDirPrefix := dstB.dirPrefix(dstKey)
	paginator := s3.NewListObjectsV2Paginator(srcB.Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(srcB.Bucket),
		Prefix: aws.String(srcDirPrefix),
	})
	for paginator.HasMorePages() {
		page, err := h.nextListPage(paginator, ctx, srcB)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			sk := aws.ToString(obj.Key)
			suffix := strings.TrimPrefix(sk, srcDirPrefix)
			if err := h.streamCopyObject(ctx, srcB, sk, dstB, dstDirPrefix+suffix); err != nil {
				return err
			}
		}
	}
	return nil
}

// handleCopy copies srcPath to dstPath without removing the source.
func (h *S3Handlers) handleCopy(ctx context.Context, srcPath, dstPath string) error {
	srcBackend, srcKey, err := h.resolve(srcPath)
	if err != nil {
		return err
	}
	dstBackend, dstKey, err := h.resolve(dstPath)
	if err != nil {
		return err
	}
	if srcBackend == nil || dstBackend == nil {
		return fmt.Errorf("cannot copy root")
	}

	crossBackend := srcBackend.Name != dstBackend.Name
	if crossBackend {
		err = h.crossBackendCopy(ctx, srcBackend, srcKey, dstBackend, dstKey)
	} else {
		err = h.sameBackendCopy(ctx, srcBackend, srcKey, dstKey)
	}
	if err != nil {
		return err
	}
	slog.Debug("Copy complete", "from", srcPath, "to", dstPath, "cross_backend", crossBackend)
	return nil
}

// streamCopyObject copies a single S3 object from srcB to dstB by streaming
// the body. No more than one backend's part size is held in memory at once.
func (h *S3Handlers) streamCopyObject(ctx context.Context, srcB *Backend, srcKey string, dstB *Backend, dstKey string) error {
	// Use a generous timeout for the whole stream: the HTTP client-level
	// timeouts still govern individual range/part requests.
	timeout := maxDuration(srcB.Timeout, dstB.Timeout) * 10
	if timeout < 5*time.Minute {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	getCtx, getCancel := context.WithTimeout(ctx, srcB.Timeout)
	out, err := srcB.Client.GetObject(getCtx, &s3.GetObjectInput{
		Bucket: aws.String(srcB.Bucket),
		Key:    aws.String(srcKey),
	})
	getCancel()
	if err != nil {
		return err
	}
	defer out.Body.Close()

	size := aws.ToInt64(out.ContentLength)

	pr, pw := io.Pipe()
	uploadDone := make(chan struct{})
	var uploadErr error
	go func() {
		defer close(uploadDone)
		uploadCtx, uploadCancel := context.WithTimeout(ctx, dstB.Timeout)
		defer uploadCancel()
		start := time.Now()
		_, uploadErr = dstB.Uploader.Upload(uploadCtx, &s3.PutObjectInput{
			Bucket: aws.String(dstB.Bucket),
			Key:    aws.String(dstKey),
			Body:   pr,
		})
		if h.metrics != nil {
			h.metrics.ObserveS3Op("PutObject", dstB.Name, start, uploadErr)
			if uploadErr == nil {
				h.metrics.AddUploadBytes(size)
			}
		}
		_ = pr.CloseWithError(uploadErr)
	}()

	start := time.Now()
	written, copyErr := io.Copy(pw, out.Body)
	if copyErr != nil {
		_ = pw.CloseWithError(copyErr)
	} else {
		_ = pw.Close()
	}
	if h.metrics != nil {
		h.metrics.ObserveS3Op("GetObject", srcB.Name, start, copyErr)
		if copyErr == nil {
			h.metrics.AddDownloadBytes(written)
		}
	}
	<-uploadDone
	if copyErr != nil {
		slog.Debug("stream copy failed", "src_backend", srcB.Name, "src_key", srcKey, "dst_backend", dstB.Name, "dst_key", dstKey, "error", copyErr)
		return copyErr
	}
	if uploadErr != nil {
		slog.Debug("stream copy upload failed", "src_backend", srcB.Name, "src_key", srcKey, "dst_backend", dstB.Name, "dst_key", dstKey, "error", uploadErr)
		return uploadErr
	}
	slog.Debug("stream copy complete", "src_backend", srcB.Name, "src_key", srcKey, "dst_backend", dstB.Name, "dst_key", dstKey, "size", size)
	return nil
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// Filelist handles SFTP directory listing and stat requests.
func (h *S3Handlers) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	h.logRequest(r)
	if err := h.requireRead(); err != nil {
		return nil, err
	}
	baseCtx := h.ctx

	switch r.Method {
	case "List":
		return h.listDirectory(baseCtx, r.Filepath)
	case "Stat", "Lstat":
		return h.statPath(baseCtx, r.Filepath)
	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}

// listDirectory returns the entries inside p.
func (h *S3Handlers) listDirectory(ctx context.Context, p string) (sftp.ListerAt, error) {
	b, key, err := h.resolve(p)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return h.listRoot()
	}

	ctx, cancel := withBackendTimeout(ctx, b)
	defer cancel()

	dp := b.dirPrefix(key)
	paginator := s3.NewListObjectsV2Paginator(b.Client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(b.Bucket),
		Prefix:    aws.String(dp),
		Delimiter: aws.String("/"),
	})

	seen := make(map[string]bool)
	var infos []os.FileInfo
	for paginator.HasMorePages() {
		page, err := h.nextListPage(paginator, ctx, b)
		if err != nil {
			return nil, err
		}
		for _, cp := range page.CommonPrefixes {
			name := strings.TrimPrefix(aws.ToString(cp.Prefix), dp)
			name = strings.TrimSuffix(name, "/")
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			infos = append(infos, newDirInfo(name))
		}
		for _, obj := range page.Contents {
			k := aws.ToString(obj.Key)
			if k == dp {
				continue
			}
			name := strings.TrimPrefix(k, dp)
			if strings.Contains(name, "/") || seen[name] {
				continue
			}
			seen[name] = true
			infos = append(infos, newFileInfo(name, aws.ToInt64(obj.Size), aws.ToTime(obj.LastModified)))
		}
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name() < infos[j].Name() })
	slog.Debug("listed directory", "path", p, "prefix", dp, "entries", len(infos))
	return listerAt(infos), nil
}

// listRoot returns the available backend folders.
func (h *S3Handlers) listRoot() (sftp.ListerAt, error) {
	var infos []os.FileInfo
	for name := range h.vfs.Backends {
		infos = append(infos, newDirInfo(name))
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name() < infos[j].Name() })
	slog.Debug("listed root", "backends", len(infos))
	return listerAt(infos), nil
}

// statPath returns metadata for p.
func (h *S3Handlers) statPath(ctx context.Context, p string) (sftp.ListerAt, error) {
	b, key, err := h.resolve(p)
	if err != nil {
		return nil, err
	}
	if b == nil {
		slog.Debug("stat root", "path", p)
		return listerAt([]os.FileInfo{newDirInfo("/")}), nil
	}
	if key == "" {
		slog.Debug("stat backend root", "path", p, "backend", b.Name)
		return listerAt([]os.FileInfo{newDirInfo(b.Name)}), nil
	}

	ctx, cancel := withBackendTimeout(ctx, b)
	defer cancel()

	objKey := b.objectKey(key)
	out, err := b.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.Bucket),
		Key:    aws.String(objKey),
	})
	if err == nil {
		slog.Debug("stat file", "path", p, "backend", b.Name, "key", objKey, "size", aws.ToInt64(out.ContentLength))
		return listerAt([]os.FileInfo{
			newFileInfo(path.Base(key), aws.ToInt64(out.ContentLength), aws.ToTime(out.LastModified)),
		}), nil
	}
	var notFound *types.NotFound
	var noSuchKey *types.NoSuchKey
	if !(errors.As(err, &notFound) || errors.As(err, &noSuchKey)) {
		slog.Debug("stat HeadObject failed", "path", p, "backend", b.Name, "key", objKey, "error", err)
		return nil, err
	}

	dp := b.dirPrefix(key)
	hasChildren, err := h.prefixHasEntries(ctx, b, dp)
	if err != nil {
		slog.Debug("stat prefix check failed", "path", p, "backend", b.Name, "prefix", dp, "error", err)
		return nil, err
	}
	if hasChildren {
		slog.Debug("stat directory", "path", p, "backend", b.Name, "prefix", dp)
		return listerAt([]os.FileInfo{newDirInfo(path.Base(key))}), nil
	}
	slog.Debug("stat not found", "path", p, "backend", b.Name, "key", objKey)
	return nil, os.ErrNotExist
}
