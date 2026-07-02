package webfront

import (
	"testing"
)

func TestEmbeddedFilesExist(t *testing.T) {
	tests := []struct {
		path string
	}{
		{"webfront-dist/index.html"},
	}
	for _, tt := range tests {
		f, err := embeddedFiles.Open(tt.path)
		if err != nil {
			t.Fatalf("embedded file %q not found: %v", tt.path, err)
		}
		f.Close()
	}
}

func TestWeFrontFileSystem(t *testing.T) {
	// Test that our custom filesystem correctly serves index.html.
	fsys := webfrontFileSystem{inner: embeddedFiles}
	f, err := fsys.Open("/index.html")
	if err != nil {
		t.Fatalf("open /index.html via webfrontFileSystem: %v", err)
	}
	defer f.Close()
	buf := make([]byte, 100)
	n, _ := f.Read(buf)
	if n == 0 {
		t.Fatal("index.html is empty")
	}
}

func TestAssetFilesAccessible(t *testing.T) {
	// Verify that asset files are accessible through our filesystem.
	fsys := webfrontFileSystem{inner: embeddedFiles}
	entries, err := fsys.Open("/assets/")
	if err != nil {
		t.Fatalf("open /assets/ via webfrontFileSystem: %v", err)
	}
	defer entries.Close()
}

func TestGatewayNew(t *testing.T) {
	// New requires valid components; these tests are done in main_test.go
	// integration tests. Here we just verify the type is correct.
	var gw *Gateway
	_ = gw
}

func TestGatewayEmptyAddrStart(t *testing.T) {
	// Start with empty addr on a minimal Gateway should be no-op.
	gw := &Gateway{addr: ""}
	if err := gw.Start(nil); err != nil {
		t.Fatalf("Start with empty addr should succeed: %v", err)
	}
}
