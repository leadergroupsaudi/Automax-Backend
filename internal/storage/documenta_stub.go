package storage

import (
	"bytes"
	"context"
	"io"
	"log"
	"strings"

	"github.com/google/uuid"
)

// StubFileIDPrefix is what StubDocumentaClient.UploadFile puts in front of the
// uuid it invents, so a stub file id is recognisable as one.
//
// It is exported because the id the stub produces is what a deployment running
// it (DOCUMENTA_ENABLED=false) genuinely holds and submits, so a consumer that
// checks the SHAPE of a Documenta file id — ValidateRecordValues for the EO
// file kinds, spec §7.12 — has to accept it, and must not do so by repeating
// the literal.
const StubFileIDPrefix = "stub-file-"

// StubDocumentaClient is a placeholder implementation that logs all calls
// and returns mock data. Used when DOCUMENTA_ENABLED=false.
type StubDocumentaClient struct{}

func NewStubDocumentaClient() DocumentaClient {
	return &StubDocumentaClient{}
}

// ── Folder & File Operations ──

func (s *StubDocumentaClient) CreateFolder(ctx context.Context, workspaceName string, parentID string, name string) (string, error) {
	id := "stub-folder-" + uuid.New().String()
	log.Printf("[DOCUMENTA STUB] CreateFolder: workspace=%s, parent=%s, name=%s → folderID=%s", workspaceName, parentID, name, id)
	return id, nil
}

func (s *StubDocumentaClient) EnsureFolder(ctx context.Context, workspaceName string, parentID string, name string) (string, error) {
	// Special-case the root Goal Management folder so the stub Documents view
	// shows the same UUID every time — otherwise every goal creation would
	// produce a fresh "Goal Management" ID and break the illusion of hierarchy.
	if parentID == "" && name == "Goal Management" {
		id := "stub-folder-goal-mgmt"
		log.Printf("[DOCUMENTA STUB] EnsureFolder: workspace=%s, parent=(root), name=%q → %s (stable)", workspaceName, name, id)
		return id, nil
	}
	id := "stub-folder-" + uuid.New().String()
	log.Printf("[DOCUMENTA STUB] EnsureFolder: workspace=%s, parent=%s, name=%q → %s", workspaceName, parentID, name, id)
	return id, nil
}

func (s *StubDocumentaClient) UploadFile(ctx context.Context, folderID, fileName string, fileData io.Reader, fileSize int64, metadata map[string]string) (string, error) {
	id := StubFileIDPrefix + uuid.New().String()
	log.Printf("[DOCUMENTA STUB] UploadFile: folder=%s, file=%s, size=%d → fileID=%s", folderID, fileName, fileSize, id)
	return id, nil
}

func (s *StubDocumentaClient) GetPreviewURL(ctx context.Context, fileID string) (string, error) {
	url := "https://documenta.stub/preview/" + fileID
	log.Printf("[DOCUMENTA STUB] GetPreviewURL: fileID=%s → %s", fileID, url)
	return url, nil
}

func (s *StubDocumentaClient) GetDownloadURL(ctx context.Context, fileID string) (string, error) {
	url := "https://documenta.stub/download/" + fileID
	log.Printf("[DOCUMENTA STUB] GetDownloadURL: fileID=%s → %s", fileID, url)
	return url, nil
}

func (s *StubDocumentaClient) DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, *DmsFile, error) {
	log.Printf("[DOCUMENTA STUB] DownloadFile: fileID=%s", fileID)
	content := []byte("stub content for " + fileID)
	info := &DmsFile{
		UUID:     fileID,
		Name:     "stub-file.txt",
		Type:     "file",
		Size:     int64(len(content)),
		MimeType: "text/plain",
	}
	return io.NopCloser(bytes.NewReader(content)), info, nil
}

func (s *StubDocumentaClient) UpdateMetadata(ctx context.Context, fileID string, metadata map[string]string) error {
	log.Printf("[DOCUMENTA STUB] UpdateMetadata: fileID=%s, metadata=%v", fileID, metadata)
	return nil
}

func (s *StubDocumentaClient) DeleteFile(ctx context.Context, fileID string) error {
	log.Printf("[DOCUMENTA STUB] DeleteFile: fileID=%s", fileID)
	return nil
}

// ── Browsing & Search ──

func (s *StubDocumentaClient) ListFiles(ctx context.Context, workspaceSlug string, parentID string) (*DmsListResult, error) {
	log.Printf("[DOCUMENTA STUB] ListFiles: workspace=%s, parent=%s", workspaceSlug, parentID)
	// Workspace root: show "Goal Management" + a sample file.
	if parentID == "" {
		return &DmsListResult{
			Files: []DmsFile{
				{UUID: "stub-folder-goal-mgmt", Name: "Goal Management", Type: "folder", CreatedAt: "2025-01-01T00:00:00Z", UpdatedAt: "2025-01-01T00:00:00Z"},
				{UUID: "stub-file-readme", Name: "README.md", Type: "file", Size: 1024, MimeType: "text/markdown", CreatedAt: "2025-01-01T00:00:00Z", UpdatedAt: "2025-01-01T00:00:00Z"},
			},
		}, nil
	}
	// Inside the Goal Management folder: show a couple of sample goal folders.
	if parentID == "stub-folder-goal-mgmt" {
		return &DmsListResult{
			Files: []DmsFile{
				{UUID: "stub-folder-demo-goal-1", Name: "Sample Goal — Improve Safety", Type: "folder", Parent: parentID, CreatedAt: "2025-01-01T00:00:00Z", UpdatedAt: "2025-01-01T00:00:00Z"},
				{UUID: "stub-folder-demo-goal-2", Name: "Sample Goal — Upgrade Systems", Type: "folder", Parent: parentID, CreatedAt: "2025-01-01T00:00:00Z", UpdatedAt: "2025-01-01T00:00:00Z"},
			},
		}, nil
	}
	// Any other stub parent: show a single sample file, no nested folders. This prevents
	// the infinite-recursion breadcrumb you get if the stub always returned the same list.
	return &DmsListResult{
		Files: []DmsFile{
			{UUID: "stub-file-evidence", Name: "evidence.pdf", Type: "file", Size: 2048, MimeType: "application/pdf", Parent: parentID, CreatedAt: "2025-01-01T00:00:00Z", UpdatedAt: "2025-01-01T00:00:00Z"},
		},
	}, nil
}

func (s *StubDocumentaClient) SearchFiles(ctx context.Context, workspaceSlug string, query string) (*DmsSearchResult, error) {
	log.Printf("[DOCUMENTA STUB] SearchFiles: workspace=%s, query=%s", workspaceSlug, query)
	return &DmsSearchResult{
		Files: []DmsFile{
			{UUID: "stub-file-search-1", Name: "search-result.pdf", Type: "file", Size: 2048, MimeType: "application/pdf", CreatedAt: "2025-01-01T00:00:00Z", UpdatedAt: "2025-01-01T00:00:00Z"},
		},
		Total: 1,
	}, nil
}

func (s *StubDocumentaClient) SearchFilesWithTags(ctx context.Context, workspaceSlug string, query string, tags map[string]string) (*DmsSearchResult, error) {
	log.Printf("[DOCUMENTA STUB] SearchFilesWithTags: workspace=%s, query=%s, tags=%v", workspaceSlug, query, tags)
	return &DmsSearchResult{Files: []DmsFile{}, Total: 0}, nil
}

func (s *StubDocumentaClient) GetFileBreadcrumb(ctx context.Context, fileID string) ([]DmsBreadcrumbEntry, error) {
	log.Printf("[DOCUMENTA STUB] GetFileBreadcrumb: fileID=%s", fileID)
	// Pretend the file lives under Goal Management > a sample goal folder.
	return []DmsBreadcrumbEntry{
		{UUID: "stub-folder-goal-mgmt", Name: "Goal Management"},
		{UUID: "stub-folder-demo-goal-1", Name: "Sample Goal — Improve Safety"},
	}, nil
}

func (s *StubDocumentaClient) GetFileInfo(ctx context.Context, fileID string) (*DmsFile, error) {
	log.Printf("[DOCUMENTA STUB] GetFileInfo: fileID=%s", fileID)
	return &DmsFile{
		UUID:      fileID,
		Name:      "stub-file.pdf",
		Type:      "file",
		Size:      4096,
		MimeType:  "application/pdf",
		CreatedAt: "2025-01-01T00:00:00Z",
		UpdatedAt: "2025-01-01T00:00:00Z",
		Metadata:  map[string]string{"source_system": "automax"},
	}, nil
}

// ── Comments ──

func (s *StubDocumentaClient) GetComments(ctx context.Context, fileID string) ([]DmsComment, error) {
	log.Printf("[DOCUMENTA STUB] GetComments: fileID=%s", fileID)
	return []DmsComment{
		{ID: "stub-comment-1", FileID: fileID, Author: "admin@example.com", Content: "Stub comment", CreatedAt: "2025-01-01T00:00:00Z"},
	}, nil
}

func (s *StubDocumentaClient) AddComment(ctx context.Context, fileID string, content string) error {
	log.Printf("[DOCUMENTA STUB] AddComment: fileID=%s, content=%s", fileID, content)
	return nil
}

// ── Tags ──

func (s *StubDocumentaClient) GetTags(ctx context.Context, fileID string) (map[string]string, error) {
	log.Printf("[DOCUMENTA STUB] GetTags: fileID=%s", fileID)
	return map[string]string{"source_system": "automax", "evidence_type": "Report"}, nil
}

func (s *StubDocumentaClient) SetTags(ctx context.Context, fileID string, tags map[string]string) error {
	log.Printf("[DOCUMENTA STUB] SetTags: fileID=%s, tags=%v", fileID, tags)
	return nil
}

// ── Versions ──

func (s *StubDocumentaClient) ListVersions(ctx context.Context, fileID string) ([]DmsVersion, error) {
	log.Printf("[DOCUMENTA STUB] ListVersions: fileID=%s", fileID)
	return []DmsVersion{
		{UUID: "stub-version-1", NodeUUID: fileID, VersionNumber: 1, Size: 1024, Description: "Initial version", Source: "upload", CreatedBy: "admin@example.com", CreatedByName: "Admin", CreatedAt: "2025-01-01T00:00:00Z", IsCurrent: true},
	}, nil
}

func (s *StubDocumentaClient) UploadVersion(ctx context.Context, fileID string, fileName string, fileData io.Reader, fileSize int64, description string) (*DmsVersion, error) {
	id := uuid.New().String()
	log.Printf("[DOCUMENTA STUB] UploadVersion: fileID=%s, fileName=%s, size=%d → versionID=%s", fileID, fileName, fileSize, id)
	return &DmsVersion{
		UUID:          id,
		NodeUUID:      fileID,
		VersionNumber: 2,
		Size:          fileSize,
		Description:   description,
		Source:        "upload",
		CreatedBy:     "admin@example.com",
		CreatedByName: "Admin",
		CreatedAt:     "2025-01-01T00:00:00Z",
		IsCurrent:     true,
	}, nil
}

func (s *StubDocumentaClient) DownloadVersion(ctx context.Context, versionUUID string) (io.ReadCloser, string, error) {
	log.Printf("[DOCUMENTA STUB] DownloadVersion: versionUUID=%s", versionUUID)
	return io.NopCloser(strings.NewReader("stub version content")), "text/plain", nil
}

func (s *StubDocumentaClient) RollbackVersion(ctx context.Context, fileID string, versionUUID string) (*DmsVersion, error) {
	log.Printf("[DOCUMENTA STUB] RollbackVersion: fileID=%s, versionUUID=%s", fileID, versionUUID)
	return &DmsVersion{
		UUID:          versionUUID,
		NodeUUID:      fileID,
		VersionNumber: 1,
		Size:          1024,
		Description:   "Rolled back version",
		Source:        "rollback",
		CreatedBy:     "admin@example.com",
		CreatedByName: "Admin",
		CreatedAt:     "2025-01-01T00:00:00Z",
		IsCurrent:     true,
	}, nil
}
