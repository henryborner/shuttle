package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindLocalBinary_NextToExecutable(t *testing.T) {
	// Create a fake agent binary in a temp dir and test cwd fallback.
	// findLocalBinary looks for "shuttle_linux" as the legacy fallback name.
	tmpFile := filepath.Join(t.TempDir(), "shuttle_linux")
	if err := os.WriteFile(tmpFile, []byte("fake"), 0755); err != nil {
		t.Fatalf("create fake: %v", err)
	}

	// Test from temp dir (no agent binary) — should fail
	origDir, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(origDir)

	_, err := findLocalBinary()
	if err == nil {
		t.Error("findLocalBinary should fail when no agent binary found")
	}

	// Test from temp dir with agent binary present
	os.Chdir(filepath.Dir(tmpFile))
	path, err := findLocalBinary()
	if err != nil {
		t.Fatalf("findLocalBinary: %v", err)
	}
	if path != "shuttle_linux" {
		t.Errorf("path = %q, want shuttle_linux", path)
	}
}
