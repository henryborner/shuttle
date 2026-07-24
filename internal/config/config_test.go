package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "syncd.yaml")
	content := []byte(`
version: "1.0"
servers:
  - name: srv1
    host: 10.0.0.1
    port: 22
    user: root
tasks:
  - name: web
    source: /src/web
    target: srv1:/var/www
`)
	os.WriteFile(tmp, content, 0600)

	cfg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Version != "1.0" {
		t.Errorf("version = %q, want 1.0", cfg.Version)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(cfg.Servers))
	}
	if cfg.Servers[0].Host != "10.0.0.1" {
		t.Errorf("host = %q", cfg.Servers[0].Host)
	}
	if len(cfg.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(cfg.Tasks))
	}
	if cfg.Tasks[0].Source != "/src/web" {
		t.Errorf("source = %q", cfg.Tasks[0].Source)
	}
}

func TestLoadDefaults(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "syncd.yaml")
	content := []byte(`
version: "1.0"
servers:
  - name: s
    host: 1.2.3.4
tasks: []
`)
	os.WriteFile(tmp, content, 0600)

	cfg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Defaults from YAML zero values
	if cfg.Servers[0].Port != 0 {
		t.Errorf("port = %d, want 0 (YAML default)", cfg.Servers[0].Port)
	}
	if cfg.Servers[0].User != "" {
		t.Errorf("user = %q, want empty", cfg.Servers[0].User)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/syncd.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "syncd.yaml")
	os.WriteFile(tmp, []byte(": {{ invalid"), 0600)

	_, err := Load(tmp)
	if err == nil {
		t.Error("expected parse error for invalid YAML")
	}
}

func TestGetServer(t *testing.T) {
	cfg := &Config{
		Servers: []Server{
			{Name: "prod", Host: "10.0.0.1"},
			{Name: "dev", Host: "10.0.0.2"},
		},
	}

	s := cfg.GetServer("prod")
	if s == nil || s.Host != "10.0.0.1" {
		t.Errorf("GetServer(prod) = %v", s)
	}

	s = cfg.GetServer("nonexistent")
	if s != nil {
		t.Error("GetServer(nonexistent) should be nil")
	}
}

func TestGetTask(t *testing.T) {
	cfg := &Config{
		Tasks: []Task{
			{Name: "web", Source: "/src/web"},
			{Name: "api", Source: "/src/api"},
		},
	}

	task := cfg.GetTask("api")
	if task == nil || task.Source != "/src/api" {
		t.Errorf("GetTask(api) = %v", task)
	}

	task = cfg.GetTask("missing")
	if task != nil {
		t.Error("GetTask(missing) should be nil")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{
			name:    "valid",
			cfg:     &Config{
				Servers: []Server{{Name: "srv", Host: "1.2.3.4"}},
				Tasks:   []Task{{Name: "t", Source: "/s", Target: "srv:/t"}},
			},
			wantErr: false,
		},
		{
			name:    "missing name",
			cfg:     &Config{Tasks: []Task{{Source: "/s", Target: "srv:/t"}}},
			wantErr: true,
		},
		{
			name:    "missing source",
			cfg:     &Config{Tasks: []Task{{Name: "t", Target: "srv:/t"}}},
			wantErr: true,
		},
		{
			name:    "missing target",
			cfg:     &Config{Tasks: []Task{{Name: "t", Source: "/s"}}},
			wantErr: true,
		},
		{
			name: "unknown server",
			cfg: &Config{
				Servers: []Server{{Name: "srv", Host: "1.2.3.4"}},
				Tasks:   []Task{{Name: "t", Source: "/s", Target: "other:/t"}},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseTarget(t *testing.T) {
	tests := []struct {
		input      string
		wantServer string
		wantPath   string
	}{
		{"myserver:/var/www", "myserver", "/var/www"},
		{"myserver:/var/www/html/", "myserver", "/var/www/html/"},
		{"srv:C:\\path", "srv", "C:\\path"},
		{"noslash", "", "noslash"},
		{"", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			srv, path := ParseTarget(tt.input)
			if srv != tt.wantServer || path != tt.wantPath {
				t.Errorf("ParseTarget(%q) = (%q, %q), want (%q, %q)",
					tt.input, srv, path, tt.wantServer, tt.wantPath)
			}
		})
	}
}

func TestSaveAndLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "syncd.yaml")

	cfg := &Config{
		Version:  "1.0",
		Language: "zh",
		Checksum: "xxh64",
		Workers:  4,
		Servers: []Server{
			{Name: "srv", Host: "1.2.3.4", Port: 2222, User: "deploy", KeyFile: "~/.ssh/id_ed25519", Protect: []string{"*.db", "secrets/"}},
		},
		Tasks: []Task{
			{Name: "web", Source: "E:\\dist", Target: "srv:/var/www", Options: Options{Delete: true, Exclude: []string{"*.tmp"}, Checksum: true}},
		},
	}

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Version != cfg.Version {
		t.Errorf("version mismatch")
	}
	if loaded.Language != cfg.Language {
		t.Errorf("language mismatch")
	}
	if loaded.Checksum != cfg.Checksum {
		t.Errorf("checksum mismatch")
	}
	if loaded.Workers != cfg.Workers {
		t.Errorf("workers mismatch")
	}
	if len(loaded.Servers) != 1 {
		t.Fatalf("server count mismatch")
	}
	if loaded.Servers[0].Port != 2222 {
		t.Errorf("port = %d", loaded.Servers[0].Port)
	}
	if len(loaded.Servers[0].Protect) != 2 {
		t.Errorf("protect count = %d", len(loaded.Servers[0].Protect))
	}
	if len(loaded.Tasks) != 1 {
		t.Fatalf("task count mismatch")
	}
	if !loaded.Tasks[0].Options.Delete {
		t.Error("delete option lost")
	}
	if !loaded.Tasks[0].Options.Checksum {
		t.Error("checksum option lost")
	}
}

func TestOptionsDefaults(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "syncd.yaml")
	content := []byte(`
version: "1.0"
servers:
  - name: s
    host: 1.2.3.4
tasks:
  - name: t
    source: /src
    target: s:/dst
`)
	os.WriteFile(tmp, content, 0600)

	cfg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	opt := cfg.Tasks[0].Options
	if opt.Delete {
		t.Error("delete should default to false")
	}
	if opt.Checksum {
		t.Error("checksum should default to false")
	}
	if opt.Flat {
		t.Error("flat should default to false")
	}
	if opt.ShowDots {
		t.Error("show_dots should default to false")
	}
}
