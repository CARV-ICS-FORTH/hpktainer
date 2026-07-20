package image_test

import (
	"os"
	"path/filepath"
	"testing"

	"hpk/internal/compute/image"
)

func Test_ParseImageName(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{
			name:  "tagWithDigest",
			image: "registry.k8s.io/ingress-nginx/kube-webhook-certgen:v20230407@sha256:543c40fd093964bc9ab509d3e791f9989963021f1e9e4c9c7b6700b02bfb227b",
			want:  "/registry.k8s.io_ingress-nginx_kube-webhook-certgen_v20230407_sha256_543c40fd093964bc9ab509d3e791f9989963021f1e9e4c9c7b6700b02bfb227b.sif",
		},
		{
			name:  "imageWithDigestNoTag",
			image: "img@sha256:123456",
			want:  "/img_latest_sha256_123456.sif",
		},
		{
			name:  "StrangeTag",
			image: "docker.io/istio/examples-bookinfo-details-v1:1.16.2",
			want:  "/docker.io_istio_examples-bookinfo-details-v1_1.16.2.sif",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := image.ParseImageName(tt.image); got != tt.want {
				t.Errorf("parseImageName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_CleanupTempFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a normal sif file, a temp file, and a directory
	normalFile := filepath.Join(tmpDir, "image_latest.sif")
	tempFile := filepath.Join(tmpDir, "image_latest.sif.tmp-123456789")
	subDir := filepath.Join(tmpDir, "sub.tmp-dir")

	_ = os.WriteFile(normalFile, []byte("data"), 0644)
	_ = os.WriteFile(tempFile, []byte("temp"), 0644)
	_ = os.Mkdir(subDir, 0755)

	if err := image.CleanupTempFiles(tmpDir); err != nil {
		t.Fatalf("CleanupTempFiles failed: %v", err)
	}

	if _, err := os.Stat(normalFile); os.IsNotExist(err) {
		t.Errorf("normal file should not be removed")
	}
	if _, err := os.Stat(tempFile); !os.IsNotExist(err) {
		t.Errorf("temp file should have been removed")
	}
	if _, err := os.Stat(subDir); os.IsNotExist(err) {
		t.Errorf("subdirectory should not be removed")
	}
}
