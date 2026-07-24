package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindLocalBinary_NextToExecutable(t *testing.T) {
	// Create a fake shuttle_linux next to our test binary (or in cwd).
	// Since tests run from the module directory, and shuttle_linux exists
	// in the shuttle/ root, we test the cwd fallback.
	cwd, _ := os.Getwd()

	// If we're inside shuttle/ directory, shuttle_linux should be findable
	tmpFile := filepath.Join(t.TempDir(), "shuttle_linux")
	if err := os.WriteFile(tmpFile, []byte("fake"), 0755); err != nil {
		t.Fatalf("create fake: %v", err)
	}

	// Test from temp dir (no shuttle_linux) — should fail
	origDir, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(origDir)

	_, err := findLocalBinary()
	if err == nil {
		t.Error("findLocalBinary should fail when no shuttle_linux in cwd or exe dir")
	}

	// Test from temp dir with shuttle_linux present
	os.Chdir(filepath.Dir(tmpFile))
	path, err := findLocalBinary()
	if err != nil {
		t.Fatalf("findLocalBinary: %v", err)
	}
	if path != "shuttle_linux" {
		t.Errorf("path = %q, want shuttle_linux", path)
	}

	_ = cwd
}
