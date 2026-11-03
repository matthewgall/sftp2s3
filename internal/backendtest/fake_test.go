package backendtest

import "testing"

func TestBackendTestPackageCompiles(t *testing.T) {
	_ = NewFakeS3(t, nil)
	_ = NewS3Client(t, nil, false)
}
