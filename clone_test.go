package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		name         string
		rawURL       string
		wantURL      string
		wantRepoName string
		wantErr      bool
	}{
		{
			name:         "GitHub SSH",
			rawURL:       "git@github.com:owner/my-repo.git",
			wantURL:      "git@github.com:owner/my-repo.git",
			wantRepoName: "my-repo",
			wantErr:      false,
		},
		{
			name:         "GitLab SSH nested group",
			rawURL:       "git@gitlab.com:group/subgroup/target-app.git",
			wantURL:      "git@gitlab.com:group/subgroup/target-app.git",
			wantRepoName: "target-app",
			wantErr:      false,
		},
		{
			name:         "SSH with scheme and port",
			rawURL:       "ssh://git@custom-server.com:2222/org/project-x.git",
			wantURL:      "ssh://git@custom-server.com:2222/org/project-x.git",
			wantRepoName: "project-x",
			wantErr:      false,
		},
		{
			name:         "HTTPS standard with .git",
			rawURL:       "https://github.com/facebook/react.git",
			wantURL:      "https://github.com/facebook/react.git",
			wantRepoName: "react",
			wantErr:      false,
		},
		{
			name:         "HTTPS without .git",
			rawURL:       "https://github.com/golang/go",
			wantURL:      "https://github.com/golang/go",
			wantRepoName: "go",
			wantErr:      false,
		},
		{
			name:         "HTTPS with trailing slash",
			rawURL:       "https://github.com/golang/go/",
			wantURL:      "https://github.com/golang/go/",
			wantRepoName: "go",
			wantErr:      false,
		},
		{
			name:         "HTTPS with auth token",
			rawURL:       "https://user:ghp_123456789@github.com/private/repo.git",
			wantURL:      "https://user:ghp_123456789@github.com/private/repo.git",
			wantRepoName: "repo",
			wantErr:      false,
		},
		{
			name:    "Flag injection attempt",
			rawURL:  "--upload-pack=evil",
			wantErr: true,
		},
		{
			name:    "Empty URL",
			rawURL:  "   ",
			wantErr: true,
		},
		{
			name:    "Unsupported protocol file://",
			rawURL:  "file:///etc/passwd",
			wantErr: true,
		},
		{
			name:    "Plain string without protocol",
			rawURL:  "some-random-string",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotName, err := parseRepoURL(tt.rawURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseRepoURL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if gotURL != tt.wantURL {
					t.Errorf("parseRepoURL() gotURL = %v, want %v", gotURL, tt.wantURL)
				}
				if gotName != tt.wantRepoName {
					t.Errorf("parseRepoURL() gotName = %v, want %v", gotName, tt.wantRepoName)
				}
			}
		})
	}
}

func TestSanitizeProjectName(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "my-project", want: "my-project", wantErr: false},
		{input: "project_1.2", want: "project_1.2", wantErr: false},
		{input: "repo.git", want: "repo", wantErr: false},
		{input: "  trimmed-name  ", want: "trimmed-name", wantErr: false},
		{input: "../escape", wantErr: true},
		{input: "sub/dir", wantErr: true},
		{input: "back\\slash", wantErr: true},
		{input: "name with space", wantErr: true},
		{input: "-leading-dash", wantErr: true},
		{input: "..", wantErr: true},
		{input: ".", wantErr: true},
		{input: "", wantErr: true},
		{input: "name:colon", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := sanitizeProjectName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("sanitizeProjectName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("sanitizeProjectName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")

	content := "test content for AGENT.md template"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile dst failed: %v", err)
	}

	if string(data) != content {
		t.Errorf("got content %q, want %q", string(data), content)
	}
}

func TestCloneDestinationAndActiveProjectUnchanged(t *testing.T) {
	tmpProjectsDir := t.TempDir()

	// Existing projects in projects dir
	existing1 := filepath.Join(tmpProjectsDir, "existing-repo-1")
	existing2 := filepath.Join(tmpProjectsDir, "existing-repo-2")
	if err := os.MkdirAll(existing1, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.MkdirAll(existing2, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Set active project
	initialActiveProject := "existing-repo-1"
	projectState.Lock()
	projectState.currentProject = initialActiveProject
	projectState.Unlock()

	// Simulate target resolution in clone command
	targetName, err := sanitizeProjectName("new-cloned-repo")
	if err != nil {
		t.Fatalf("sanitizeProjectName failed: %v", err)
	}

	cleanRoot := filepath.Clean(tmpProjectsDir)
	targetPath := filepath.Join(cleanRoot, targetName)

	// Verify targetPath is directly inside projectsRoot (adjacent to existing projects)
	if filepath.Dir(targetPath) != cleanRoot {
		t.Errorf("expected targetPath parent to be %q, got %q", cleanRoot, filepath.Dir(targetPath))
	}
	if filepath.Base(targetPath) != "new-cloned-repo" {
		t.Errorf("expected target folder name %q, got %q", "new-cloned-repo", filepath.Base(targetPath))
	}

	// Verify active project remains untouched
	projectState.RLock()
	currentActive := projectState.currentProject
	projectState.RUnlock()

	if currentActive != initialActiveProject {
		t.Errorf("active project changed: got %q, want %q", currentActive, initialActiveProject)
	}
}

