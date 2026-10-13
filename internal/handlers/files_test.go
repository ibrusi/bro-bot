package handlers

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
)

func TestHandleDocumentNilFile(t *testing.T) {
	m := mock.New()
	sess := &mock.Session{
		M:       m,
		ChatID:  "123",
		Sender:  "123",
		FileVal: nil,
	}

	err := handleDocument(sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Sent) != 0 {
		t.Errorf("expected no messages for nil file, got %d", len(m.Sent))
	}
}

func TestHandleDocumentNoProject(t *testing.T) {
	prevTM := domain.GlobalTaskManager
	domain.GlobalTaskManager = domain.NewTaskManager()
	defer func() { domain.GlobalTaskManager = prevTM }()

	config.ProjectState.Lock()
	prevProj := config.ProjectState.CurrentProject
	config.ProjectState.CurrentProject = ""
	config.ProjectState.Unlock()
	defer func() {
		config.ProjectState.Lock()
		config.ProjectState.CurrentProject = prevProj
		config.ProjectState.Unlock()
	}()

	m := mock.New()
	sess := &mock.Session{
		M:      m,
		ChatID: "123",
		Sender: "123",
		FileVal: &ports.FileMessage{
			FileID:   "f1",
			FileName: "test.txt",
			FileSize: 100,
		},
	}

	err := handleDocument(sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all := m.AllTexts()
	found := false
	for _, txt := range all {
		if strings.Contains(txt, i18n.T(i18n.Default, "projects.select_first")) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected projects.select_first message in %v", all)
	}
}

func TestHandleDocumentTooLarge(t *testing.T) {
	config.ProjectState.Lock()
	prevProj := config.ProjectState.CurrentProject
	config.ProjectState.CurrentProject = "testproj"
	config.ProjectState.Unlock()
	defer func() {
		config.ProjectState.Lock()
		config.ProjectState.CurrentProject = prevProj
		config.ProjectState.Unlock()
	}()

	m := mock.New()
	sess := &mock.Session{
		M:      m,
		ChatID: "123",
		Sender: "123",
		FileVal: &ports.FileMessage{
			FileID:   "f_big",
			FileName: "big.zip",
			FileSize: 25 * 1024 * 1024, // 25 MB > 20 MB
		},
	}

	err := handleDocument(sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all := m.AllTexts()
	found := false
	for _, txt := range all {
		if strings.Contains(txt, "20 МБ") || strings.Contains(txt, "20 MB") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected size limit warning in %v", all)
	}
}

func TestHandleDocumentDownloadError(t *testing.T) {
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "testproj")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}

	prevRoot := config.ProjectsRoot
	config.ProjectsRoot = tmpDir
	defer func() { config.ProjectsRoot = prevRoot }()

	config.ProjectState.Lock()
	prevProj := config.ProjectState.CurrentProject
	config.ProjectState.CurrentProject = "testproj"
	config.ProjectState.Unlock()
	defer func() {
		config.ProjectState.Lock()
		config.ProjectState.CurrentProject = prevProj
		config.ProjectState.Unlock()
	}()

	m := mock.New()
	sess := &mock.Session{
		M:      m,
		ChatID: "123",
		Sender: "123",
		FileVal: &ports.FileMessage{
			FileID:   "f1",
			FileName: "error.txt",
			FileSize: 50,
		},
		OpenFileErr: errors.New("network timeout"),
	}

	err := handleDocument(sess)
	if err == nil {
		t.Fatal("expected download error, got nil")
	}

	all := m.AllTexts()
	found := false
	for _, txt := range all {
		if strings.Contains(txt, "network timeout") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error notification in %v", all)
	}
}

func TestHandleDocumentStreamExceedsLimit(t *testing.T) {
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "testproj")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}

	prevRoot := config.ProjectsRoot
	config.ProjectsRoot = tmpDir
	defer func() { config.ProjectsRoot = prevRoot }()

	config.ProjectState.Lock()
	prevProj := config.ProjectState.CurrentProject
	config.ProjectState.CurrentProject = "testproj"
	config.ProjectState.Unlock()
	defer func() {
		config.ProjectState.Lock()
		config.ProjectState.CurrentProject = prevProj
		config.ProjectState.Unlock()
	}()

	m := mock.New()
	// Stream yields more than 20MB even though FileSize reported 0
	oversizedData := make([]byte, 21*1024*1024)
	sess := &mock.Session{
		M:      m,
		ChatID: "123",
		Sender: "123",
		FileVal: &ports.FileMessage{
			FileID:   "f_stream",
			FileName: "stream.bin",
			FileSize: 0,
		},
		FileReader: io.NopCloser(bytes.NewReader(oversizedData)),
	}

	err := handleDocument(sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all := m.AllTexts()
	found := false
	for _, txt := range all {
		if strings.Contains(txt, "20 МБ") || strings.Contains(txt, "20 MB") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected limit warning for oversized stream in %v", all)
	}

	// Verify temp file is cleaned up
	uploadsDir := filepath.Join(projDir, "uploads")
	entries, _ := os.ReadDir(uploadsDir)
	if len(entries) != 0 {
		t.Errorf("expected no files left in uploads after abort, got %d", len(entries))
	}
}

func TestHandleDocumentIdenticalFileReused(t *testing.T) {
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "testproj")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}

	prevRoot := config.ProjectsRoot
	config.ProjectsRoot = tmpDir
	defer func() { config.ProjectsRoot = prevRoot }()

	config.ProjectState.Lock()
	prevProj := config.ProjectState.CurrentProject
	config.ProjectState.CurrentProject = "testproj"
	config.ProjectState.Unlock()
	defer func() {
		config.ProjectState.Lock()
		config.ProjectState.CurrentProject = prevProj
		config.ProjectState.Unlock()
	}()

	content := []byte("package main\n\nfunc main() {}\n")

	// First upload
	m1 := mock.New()
	sess1 := &mock.Session{
		M:      m1,
		ChatID: "123",
		Sender: "123",
		FileVal: &ports.FileMessage{
			FileID:   "f_code",
			FileName: "main.go",
			FileSize: int64(len(content)),
			Caption:  "first upload",
		},
		FileReader: io.NopCloser(bytes.NewReader(content)),
	}

	if err := handleDocument(sess1); err != nil {
		t.Fatalf("first upload failed: %v", err)
	}

	destPath := filepath.Join(projDir, "uploads", "main.go")
	if _, err := os.Stat(destPath); os.IsNotExist(err) {
		t.Fatalf("expected file to exist at %s", destPath)
	}

	// Second upload with EXACT same content
	m2 := mock.New()
	sess2 := &mock.Session{
		M:      m2,
		ChatID: "123",
		Sender: "123",
		FileVal: &ports.FileMessage{
			FileID:   "f_code_repeat",
			FileName: "main.go",
			FileSize: int64(len(content)),
			Caption:  "second identical upload",
		},
		FileReader: io.NopCloser(bytes.NewReader(content)),
	}

	if err := handleDocument(sess2); err != nil {
		t.Fatalf("second upload failed: %v", err)
	}

	// Check that NO duplicate main_1.go was created
	dupPath := filepath.Join(projDir, "uploads", "main_1.go")
	if _, err := os.Stat(dupPath); !os.IsNotExist(err) {
		t.Errorf("main_1.go should not exist for identical content")
	}

	// Check status message mentions reused / identical
	all2 := m2.AllTexts()
	foundReused := false
	for _, txt := range all2 {
		if strings.Contains(txt, "идентичен") || strings.Contains(txt, "identical") {
			foundReused = true
			break
		}
	}
	if !foundReused {
		t.Errorf("expected reused/identical notification in %v", all2)
	}
}

func TestHandleDocumentModifiedContentSavesNumbered(t *testing.T) {
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "testproj")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}

	prevRoot := config.ProjectsRoot
	config.ProjectsRoot = tmpDir
	defer func() { config.ProjectsRoot = prevRoot }()

	config.ProjectState.Lock()
	prevProj := config.ProjectState.CurrentProject
	config.ProjectState.CurrentProject = "testproj"
	config.ProjectState.Unlock()
	defer func() {
		config.ProjectState.Lock()
		config.ProjectState.CurrentProject = prevProj
		config.ProjectState.Unlock()
	}()

	content1 := []byte("version 1")
	content2 := []byte("version 2 different")

	// First upload
	m1 := mock.New()
	sess1 := &mock.Session{
		M:      m1,
		ChatID: "123",
		Sender: "123",
		FileVal: &ports.FileMessage{
			FileID:   "f_v1",
			FileName: "note.txt",
			FileSize: int64(len(content1)),
		},
		FileReader: io.NopCloser(bytes.NewReader(content1)),
	}
	if err := handleDocument(sess1); err != nil {
		t.Fatalf("upload 1 failed: %v", err)
	}

	// Second upload with DIFFERENT content
	m2 := mock.New()
	sess2 := &mock.Session{
		M:      m2,
		ChatID: "123",
		Sender: "123",
		FileVal: &ports.FileMessage{
			FileID:   "f_v2",
			FileName: "note.txt",
			FileSize: int64(len(content2)),
		},
		FileReader: io.NopCloser(bytes.NewReader(content2)),
	}
	if err := handleDocument(sess2); err != nil {
		t.Fatalf("upload 2 failed: %v", err)
	}

	// Verify both note.txt and note_1.txt exist
	v1Path := filepath.Join(projDir, "uploads", "note.txt")
	v2Path := filepath.Join(projDir, "uploads", "note_1.txt")
	if _, err := os.Stat(v1Path); os.IsNotExist(err) {
		t.Errorf("note.txt should exist")
	}
	if _, err := os.Stat(v2Path); os.IsNotExist(err) {
		t.Errorf("note_1.txt should exist")
	}

	data1, _ := os.ReadFile(v1Path)
	data2, _ := os.ReadFile(v2Path)
	if string(data1) != string(content1) {
		t.Errorf("v1 content corrupted: %s", data1)
	}
	if string(data2) != string(content2) {
		t.Errorf("v2 content corrupted: %s", data2)
	}
}

func TestHandleDocumentPhotoUpload(t *testing.T) {
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "testproj")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}

	prevRoot := config.ProjectsRoot
	config.ProjectsRoot = tmpDir
	defer func() { config.ProjectsRoot = prevRoot }()

	config.ProjectState.Lock()
	prevProj := config.ProjectState.CurrentProject
	config.ProjectState.CurrentProject = "testproj"
	config.ProjectState.Unlock()
	defer func() {
		config.ProjectState.Lock()
		config.ProjectState.CurrentProject = prevProj
		config.ProjectState.Unlock()
	}()

	photoBytes := []byte("fake-jpeg-data")
	m := mock.New()
	sess := &mock.Session{
		M:      m,
		ChatID: "123",
		Sender: "123",
		FileVal: &ports.FileMessage{
			FileID:   "photo_123",
			FileName: "photo.jpg",
			FileSize: int64(len(photoBytes)),
			MIME:     "image/jpeg",
			Caption:  "check this error screenshot",
		},
		FileReader: io.NopCloser(bytes.NewReader(photoBytes)),
	}

	if err := handleDocument(sess); err != nil {
		t.Fatalf("photo upload failed: %v", err)
	}

	savedPhoto := filepath.Join(projDir, "uploads", "photo.jpg")
	if _, err := os.Stat(savedPhoto); os.IsNotExist(err) {
		t.Errorf("photo.jpg should be saved to %s", savedPhoto)
	}
}

func TestHandleDocumentActiveTaskFollowup(t *testing.T) {
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "testproj")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}

	prevRoot := config.ProjectsRoot
	config.ProjectsRoot = tmpDir
	defer func() { config.ProjectsRoot = prevRoot }()

	config.ProjectState.Lock()
	prevProj := config.ProjectState.CurrentProject
	config.ProjectState.CurrentProject = "testproj"
	config.ProjectState.Unlock()
	defer func() {
		config.ProjectState.Lock()
		config.ProjectState.CurrentProject = prevProj
		config.ProjectState.Unlock()
	}()

	// Create an active running task
	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "model1", "agy", "initial prompt", "123", false)
	task.Update(func(ts *domain.TaskSession) {
		ts.Status = domain.TaskStatusRunning
	})
	_, _ = domain.GlobalTaskManager.SetActiveTask(task.Snapshot().ID)
	defer func() {
		_, _ = domain.GlobalTaskManager.CancelTask(task.Snapshot().ID)
	}()

	fileContent := []byte("config: val\n")
	m := mock.New()
	sess := &mock.Session{
		M:      m,
		ChatID: "123",
		Sender: "123",
		FileVal: &ports.FileMessage{
			FileID:   "f_conf",
			FileName: "app.yaml",
			FileSize: int64(len(fileContent)),
			Caption:  "update configuration",
		},
		FileReader: io.NopCloser(bytes.NewReader(fileContent)),
	}

	if err := handleDocument(sess); err != nil {
		t.Fatalf("handleDocument failed: %v", err)
	}

	// Verify task received followup
	snap := task.Snapshot()
	if len(snap.PendingFollowups) == 0 {
		t.Errorf("expected task to have pending followups with uploaded file")
	} else {
		lastFollowup := snap.PendingFollowups[len(snap.PendingFollowups)-1]
		if !strings.Contains(lastFollowup, "uploads/app.yaml") {
			t.Errorf("expected followup to contain file path, got %q", lastFollowup)
		}
		if !strings.Contains(lastFollowup, "update configuration") {
			t.Errorf("expected followup to contain caption, got %q", lastFollowup)
		}
	}
}
