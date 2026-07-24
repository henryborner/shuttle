package transport

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestMatchProtect(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		patterns []string
		want     bool
	}{
		{"no patterns", "/var/www/config.yaml", nil, false},
		{"empty patterns", "/var/www/config.yaml", []string{}, false},
		{"exact basename match", "/var/www/config.yaml", []string{"config.yaml"}, true},
		{"glob basename match", "/var/www/data.db", []string{"*.db"}, true},
		{"glob basename no match", "/var/www/data.txt", []string{"*.db"}, false},
		{"glob path match via base", "secrets/token.pem", []string{"*.pem"}, true},
		{"multi-segment glob in full path", "secrets/token.pem", []string{"secrets/*"}, true},
		{"nested path no match", "/var/public/key", []string{"secrets/*"}, false},
		{"multiple patterns hit", "/var/www/data.db", []string{"*.log", "*.db", "*.tmp"}, true},
		{"multiple patterns miss", "/var/www/data.txt", []string{"*.log", "*.db"}, false},
		{"windows path", `C:\data\config.yaml`, []string{"config.yaml"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchProtect(tt.path, tt.patterns)
			if got != tt.want {
				t.Errorf("MatchProtect(%q, %v) = %v, want %v",
					tt.path, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestScanLocalFiles_Basic(t *testing.T) {
	dir := t.TempDir()

	// Create test files
	createFile(t, filepath.Join(dir, "a.txt"), "hello")
	createFile(t, filepath.Join(dir, "b.txt"), "world")
	createDir(t, filepath.Join(dir, "sub"))
	createFile(t, filepath.Join(dir, "sub", "c.txt"), "nested")

	files, err := scanLocalFiles(dir, nil, false)
	if err != nil {
		t.Fatalf("scanLocalFiles: %v", err)
	}

	if len(files) != 3 {
		t.Fatalf("got %d files, want 3", len(files))
	}

	names := fileNames(dir, files)
	sort.Strings(names)
	expected := []string{"a.txt", "b.txt", filepath.Join("sub", "c.txt")}
	sort.Strings(expected)
	for i, n := range expected {
		if names[i] != n {
			t.Errorf("file[%d] = %q, want %q", i, names[i], n)
		}
	}
}

func TestScanLocalFiles_SkipDots(t *testing.T) {
	dir := t.TempDir()

	createFile(t, filepath.Join(dir, "visible.txt"), "a")
	createFile(t, filepath.Join(dir, ".hidden"), "b")
	createDir(t, filepath.Join(dir, ".secret"))
	createFile(t, filepath.Join(dir, ".secret", "nested.txt"), "c")

	files, err := scanLocalFiles(dir, nil, true)
	if err != nil {
		t.Fatalf("scanLocalFiles: %v", err)
	}

	names := fileNames(dir, files)
	if len(names) != 1 || names[0] != "visible.txt" {
		t.Errorf("skipDots: got %v, want [visible.txt]", names)
	}
}

func TestScanLocalFiles_ShowDots(t *testing.T) {
	dir := t.TempDir()

	createFile(t, filepath.Join(dir, "visible.txt"), "a")
	createFile(t, filepath.Join(dir, ".env"), "SECRET=1")
	createDir(t, filepath.Join(dir, ".git"))
	createFile(t, filepath.Join(dir, ".git", "config"), "repo")

	files, err := scanLocalFiles(dir, nil, false)
	if err != nil {
		t.Fatalf("scanLocalFiles: %v", err)
	}

	names := fileNames(dir, files)
	if len(names) != 3 {
		t.Errorf("showDots: got %d files %v, want 3", len(files), names)
	}
}

func TestScanLocalFiles_Exclude(t *testing.T) {
	dir := t.TempDir()

	createFile(t, filepath.Join(dir, "main.go"), "package main")
	createFile(t, filepath.Join(dir, "main_test.go"), "package main_test")
	createFile(t, filepath.Join(dir, "README.md"), "# readme")
	createDir(t, filepath.Join(dir, "vendor"))
	createFile(t, filepath.Join(dir, "vendor", "lib.go"), "lib")

	files, err := scanLocalFiles(dir, []string{"*_test.go", "vendor/"}, false)
	if err != nil {
		t.Fatalf("scanLocalFiles: %v", err)
	}

	names := fileNames(dir, files)
	if len(names) != 2 {
		t.Errorf("exclude: got %d files %v, want 2 (main.go, README.md)", len(files), names)
	}
}

func TestScanLocalFiles_SingleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "single.txt")
	createFile(t, path, "content")

	files, err := scanLocalFiles(path, nil, false)
	if err != nil {
		t.Fatalf("scanLocalFiles: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if files[0].Path != path {
		t.Errorf("path = %q, want %q", files[0].Path, path)
	}
	if files[0].Size != 7 {
		t.Errorf("size = %d, want 7", files[0].Size)
	}
}

func TestScanLocalFiles_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	files, err := scanLocalFiles(dir, nil, false)
	if err != nil {
		t.Fatalf("scanLocalFiles: %v", err)
	}

	if len(files) != 0 {
		t.Errorf("empty dir: got %d files, want 0", len(files))
	}
}

func TestScanLocalFiles_ExcludeBasenameMatch(t *testing.T) {
	dir := t.TempDir()

	createFile(t, filepath.Join(dir, "app.log"), "log")
	createFile(t, filepath.Join(dir, "app.txt"), "txt")
	createDir(t, filepath.Join(dir, "logs"))
	createFile(t, filepath.Join(dir, "logs", "access.log"), "access")

	// "*.log" should match both files via basename
	files, err := scanLocalFiles(dir, []string{"*.log"}, false)
	if err != nil {
		t.Fatalf("scanLocalFiles: %v", err)
	}

	names := fileNames(dir, files)
	if len(names) != 1 || names[0] != "app.txt" {
		t.Errorf("exclude *.log: got %v, want [app.txt]", names)
	}
}

// helpers

func createFile(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("createFile(%q): %v", path, err)
	}
}

func createDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir(%q): %v", path, err)
	}
}

func fileNames(root string, files []localFileInfo) []string {
	names := make([]string, len(files))
	for i, f := range files {
		rel, err := filepath.Rel(root, f.Path)
		if err != nil {
			names[i] = filepath.Base(f.Path)
		} else {
			names[i] = rel
		}
	}
	return names
}
