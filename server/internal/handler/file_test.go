package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// createHandlerTestChatSession seeds a chat_session row owned by testUserID
// targeting the given agent and returns the session UUID. Cleanup runs after
// the test. Used by attachment / chat tests that need an existing session.
func createHandlerTestChatSession(t *testing.T, agentID string) string {
	t.Helper()

	var sessionID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status)
		VALUES ($1, $2, $3, $4, 'active')
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, "Handler Test Chat Session").Scan(&sessionID); err != nil {
		t.Fatalf("failed to create handler test chat session: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})
	return sessionID
}

// mockStorage is a tiny in-memory Storage stand-in. Upload records the bytes
// keyed by the storage key so GetReader can round-trip them in tests; KeyFromURL
// strips the synthetic CDN host so consumers can pass either the URL or the
// raw key.
type mockStorage struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (m *mockStorage) Upload(_ context.Context, key string, data []byte, _ string, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[key] = append([]byte(nil), data...)
	return fmt.Sprintf("https://cdn.example.com/%s", key), nil
}

func (m *mockStorage) Replace(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error) {
	return m.Upload(ctx, key, data, contentType, filename)
}

func (m *mockStorage) Delete(_ context.Context, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, key)
}
func (m *mockStorage) DeleteKeys(_ context.Context, _ []string) {}
func (m *mockStorage) KeyFromURL(rawURL string) string {
	const prefix = "https://cdn.example.com/"
	if strings.HasPrefix(rawURL, prefix) {
		return strings.TrimPrefix(rawURL, prefix)
	}
	return rawURL
}
func (m *mockStorage) CdnDomain() string { return "cdn.example.com" }
func (m *mockStorage) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if data, ok := m.files[key]; ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return nil, fmt.Errorf("mockStorage GetReader: key not found: %q", key)
}

func TestUploadFileForeignWorkspace(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("hello world"))
	writer.Close()

	foreignWorkspaceID := "00000000-0000-0000-0000-000000000099"
	req := httptest.NewRequest("POST", "/api/upload-file", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", foreignWorkspaceID)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("UploadFile with foreign workspace: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUploadFileResolvesWorkspaceViaSlugHeader is a regression test for the
// v2 workspace URL refactor (#1141). The frontend switched from sending
// X-Workspace-ID (UUID) to X-Workspace-Slug. For endpoints that sit outside
// the workspace middleware — like /api/upload-file — the handler-side
// resolver must accept the slug and translate it to a UUID, otherwise the
// handler silently falls through to the "no workspace context" branch and
// skips creating the DB attachment record. Files end up in S3 with no row
// in the attachment table, invisible to the UI.
func TestUploadFileResolvesWorkspaceViaSlugHeader(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "slug-upload.txt")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("hello via slug"))
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload-file", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	// Intentionally NOT setting X-Workspace-ID — post-v2 clients only send slug.
	req.Header.Set("X-Workspace-Slug", handlerTestWorkspaceSlug)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UploadFile with slug header: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The workspace-aware branch returns the full AttachmentResponse (with
	// id, workspace_id, uploader, etc.). The no-workspace-context branch
	// returns only {filename, link}. Distinguish by checking the shape.
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, w.Body.String())
	}
	if _, ok := resp["id"]; !ok {
		t.Fatalf("expected attachment response with 'id' field (DB row created); got fallback link-only response: %s", w.Body.String())
	}
	if gotWs, _ := resp["workspace_id"].(string); gotWs != testWorkspaceID {
		t.Fatalf("attachment workspace_id mismatch: want %s, got %v", testWorkspaceID, resp["workspace_id"])
	}

	// Verify the row actually exists in the database.
	var count int
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM attachment WHERE workspace_id = $1 AND filename = $2`,
		testWorkspaceID,
		"slug-upload.txt",
	).Scan(&count); err != nil {
		t.Fatalf("query attachment count: %v", err)
	}
	if count != 1 {
		t.Fatalf("attachment row count: want 1, got %d", count)
	}

	// Clean up so reruns don't accumulate rows.
	if _, err := testPool.Exec(
		context.Background(),
		`DELETE FROM attachment WHERE workspace_id = $1 AND filename = $2`,
		testWorkspaceID,
		"slug-upload.txt",
	); err != nil {
		t.Fatalf("cleanup attachment: %v", err)
	}
}

// TestUploadFileResolvesWorkspaceViaIDHeaderStill confirms the legacy path
// (CLI / daemon clients sending X-Workspace-ID as a UUID) still works after
// the refactor. Prevents a regression in the CLI/daemon compat branch.
func TestUploadFileResolvesWorkspaceViaIDHeaderStill(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "uuid-upload.txt")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("hello via uuid"))
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload-file", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UploadFile with UUID header: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Clean up.
	if _, err := testPool.Exec(
		context.Background(),
		`DELETE FROM attachment WHERE workspace_id = $1 AND filename = $2`,
		testWorkspaceID,
		"uuid-upload.txt",
	); err != nil {
		t.Fatalf("cleanup attachment: %v", err)
	}
}

// TestUploadFile_AttachesToChatSession verifies that a multipart upload with
// a chat_session_id form field creates an attachment row linked to that chat
// session (chat_message_id remains NULL — it is back-filled on send).
func TestUploadFile_AttachesToChatSession(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	agentID := createHandlerTestAgent(t, "ChatUploadAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "chat-upload.png")
	if err != nil {
		t.Fatal(err)
	}
	// Minimal PNG signature so content-type sniffs as image/png.
	part.Write([]byte("\x89PNG\r\n\x1a\nrest-of-bytes"))
	if err := writer.WriteField("chat_session_id", sessionID); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload-file", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UploadFile with chat_session_id: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp AttachmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, w.Body.String())
	}
	if resp.ChatSessionID == nil || *resp.ChatSessionID != sessionID {
		t.Fatalf("chat_session_id in response: want %s, got %v", sessionID, resp.ChatSessionID)
	}
	if resp.ChatMessageID != nil {
		t.Fatalf("chat_message_id should be NULL before send, got %v", resp.ChatMessageID)
	}
	if resp.IssueID != nil || resp.CommentID != nil {
		t.Fatalf("issue_id/comment_id should be NULL for chat-only upload: %+v", resp)
	}
	if resp.URL == "" {
		t.Fatal("expected non-empty url")
	}

	// Verify the DB row directly.
	var dbSession, dbMessage *string
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT chat_session_id::text, chat_message_id::text FROM attachment WHERE id = $1`,
		resp.ID,
	).Scan(&dbSession, &dbMessage); err != nil {
		t.Fatalf("query attachment row: %v", err)
	}
	if dbSession == nil || *dbSession != sessionID {
		t.Fatalf("DB chat_session_id mismatch: want %s, got %v", sessionID, dbSession)
	}
	if dbMessage != nil {
		t.Fatalf("DB chat_message_id should be NULL, got %v", dbMessage)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, resp.ID)
	})
}

// TestUploadFile_RejectsForeignChatSession verifies a chat_session in another
// workspace (or owned by another user) is rejected with 403/404, preventing
// cross-tenant attachment binding.
func TestUploadFile_RejectsForeignChatSession(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "evil.txt")
	part.Write([]byte("payload"))
	// Random non-existent UUID.
	writer.WriteField("chat_session_id", "00000000-0000-0000-0000-0000deadbeef")
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload-file", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	if w.Code != http.StatusNotFound && w.Code != http.StatusForbidden && w.Code != http.StatusBadRequest {
		t.Fatalf("UploadFile with unknown chat_session_id: expected 4xx, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GetAttachmentContent tests (preview proxy)
// ---------------------------------------------------------------------------

// seedPreviewAttachment inserts an attachment row + writes the bytes into the
// active mockStorage. Returns the new attachment id. Caller is responsible for
// installing the mockStorage on testHandler before calling.
func seedPreviewAttachment(t *testing.T, store *mockStorage, key, filename, contentType string, body []byte) string {
	t.Helper()
	// Register the body so GetReader can find it via KeyFromURL → key.
	url, err := store.Upload(context.Background(), key, body, contentType, filename)
	if err != nil {
		t.Fatalf("seed Upload: %v", err)
	}

	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO attachment (workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		VALUES ($1, 'member', $2, $3, $4, $5, $6)
		RETURNING id::text
	`, testWorkspaceID, testUserID, filename, url, contentType, len(body)).Scan(&id); err != nil {
		t.Fatalf("seed attachment row: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, id)
	})
	return id
}

func newPreviewRequest(t *testing.T, attachmentID, workspaceID string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/attachments/"+attachmentID+"/content", nil)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", attachmentID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req, httptest.NewRecorder()
}

func TestGetAttachmentContent_HappyPath_Markdown(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	body := []byte("# heading\n\nbody text\n")
	id := seedPreviewAttachment(t, store, "preview-md-key.md", "preview.md", "text/markdown", body)

	req, w := newPreviewRequest(t, id, testWorkspaceID)
	testHandler.GetAttachmentContent(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != string(body) {
		t.Errorf("body = %q, want %q", got, body)
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", got)
	}
	if got := w.Header().Get("X-Original-Content-Type"); got != "text/markdown" {
		t.Errorf("X-Original-Content-Type = %q, want text/markdown", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// Even when http.DetectContentType returned "text/plain" instead of "text/markdown"
// (a known sniffer quirk), the extension whitelist still grants access.
func TestGetAttachmentContent_AcceptsByExtensionWhenContentTypeIsGeneric(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	body := []byte("package main\n")
	id := seedPreviewAttachment(t, store, "main-go-key.go", "main.go", "application/octet-stream", body)

	req, w := newPreviewRequest(t, id, testWorkspaceID)
	testHandler.GetAttachmentContent(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestGetAttachmentContent_Unsupported_PDF(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	id := seedPreviewAttachment(t, store, "pdf-key.pdf", "manual.pdf", "application/pdf", []byte("%PDF-1.4\n"))

	req, w := newPreviewRequest(t, id, testWorkspaceID)
	testHandler.GetAttachmentContent(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415; body=%s", w.Code, w.Body.String())
	}
}

func TestGetAttachmentContent_TooLarge(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	// One byte over the limit. Allocate ASCII so io.ReadAll has work to do.
	big := bytes.Repeat([]byte("a"), maxPreviewTextSize+1)
	id := seedPreviewAttachment(t, store, "huge-key.txt", "huge.txt", "text/plain", big)

	req, w := newPreviewRequest(t, id, testWorkspaceID)
	testHandler.GetAttachmentContent(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", w.Code, w.Body.String())
	}
}

func TestGetAttachmentContent_ForeignWorkspace(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	id := seedPreviewAttachment(t, store, "ws-mismatch.md", "note.md", "text/markdown", []byte("# secret\n"))

	// Same attachment id, but request comes in scoped to a different workspace.
	foreign := "00000000-0000-0000-0000-000000000099"
	req, w := newPreviewRequest(t, id, foreign)
	testHandler.GetAttachmentContent(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestGetAttachmentContent_NotFound(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	req, w := newPreviewRequest(t, "00000000-0000-0000-0000-000000000abc", testWorkspaceID)
	testHandler.GetAttachmentContent(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// isTextPreviewable is the whitelist linkpin between the proxy and the
// client-side dispatcher. Regress against the most common content types so
// drifting one of the lists alone fails loud.
func TestIsTextPreviewable(t *testing.T) {
	t.Helper()
	cases := []struct {
		name        string
		contentType string
		filename    string
		want        bool
	}{
		{"markdown by ext", "application/octet-stream", "README.md", true},
		{"markdown by mime", "text/markdown", "README", true},
		{"plain text", "text/plain", "log.txt", true},
		{"json by mime", "application/json", "data.json", true},
		{"yaml by ext", "application/octet-stream", "config.yml", true},
		{"go source", "text/plain", "main.go", true},
		{"typescript", "application/octet-stream", "index.ts", true},
		{"html", "text/html", "page.html", true},
		{"dockerfile no ext", "application/octet-stream", "Dockerfile", true},

		{"pdf rejected", "application/pdf", "doc.pdf", false},
		{"png rejected", "image/png", "shot.png", false},
		{"video rejected", "video/mp4", "clip.mp4", false},
		{"binary fallthrough", "application/octet-stream", "blob.bin", false},
		{"docx rejected", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "report.docx", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTextPreviewable(tc.contentType, tc.filename); got != tc.want {
				t.Errorf("isTextPreviewable(%q, %q) = %v, want %v", tc.contentType, tc.filename, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// UpdateAttachmentContent tests (in-place Excalidraw save + lost-update guard)
// ---------------------------------------------------------------------------

// newUpdateContentRequest builds a PUT /api/attachments/{id} request carrying
// an Excalidraw scene body. ifMatch, when non-empty, is sent as the If-Match
// header so the handler runs the optimistic-locking path.
func newUpdateContentRequest(t *testing.T, attachmentID, workspaceID, ifMatch string, body []byte) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest("PUT", "/api/attachments/"+attachmentID, bytes.NewReader(body))
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	req.Header.Set("Content-Type", excalidrawContentType)
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", attachmentID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req, httptest.NewRecorder()
}

// TestUpdateAttachmentContent_LostUpdateGuard exercises the CAR-794 ETag /
// If-Match flow end to end against the database: a save carrying the current
// ETag wins and the ETag advances, while a later save still built on the now
// stale ETag is rejected with 412 instead of silently clobbering the winner.
func TestUpdateAttachmentContent_LostUpdateGuard(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	id := seedPreviewAttachment(t, store, "scene-key.excalidraw", "diagram.excalidraw", excalidrawContentType, []byte(`{"elements":[]}`))

	// First save without If-Match — the backwards-compat pass-through path.
	// Still returns 200 and an ETag the next save can build on.
	req, w := newUpdateContentRequest(t, id, testWorkspaceID, "", []byte(`{"elements":[1]}`))
	testHandler.UpdateAttachmentContent(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pass-through PUT: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	etag1 := w.Header().Get("ETag")
	if etag1 == "" {
		t.Fatal("pass-through PUT: missing ETag response header")
	}

	// Second save carries the current ETag — it wins, and the ETag advances.
	req, w = newUpdateContentRequest(t, id, testWorkspaceID, etag1, []byte(`{"elements":[1,2]}`))
	testHandler.UpdateAttachmentContent(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("matching If-Match PUT: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	etag2 := w.Header().Get("ETag")
	if etag2 == "" || etag2 == etag1 {
		t.Fatalf("ETag did not advance after a winning save: etag1=%q etag2=%q", etag1, etag2)
	}

	// Third save still carries the now-stale ETag — it must lose with 412.
	req, w = newUpdateContentRequest(t, id, testWorkspaceID, etag1, []byte(`{"elements":[9,9,9]}`))
	testHandler.UpdateAttachmentContent(w, req)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match PUT: status = %d, want 412; body=%s", w.Code, w.Body.String())
	}

	// A malformed If-Match is a client bug, not a conflict — answer 400.
	req, w = newUpdateContentRequest(t, id, testWorkspaceID, `"not-a-timestamp"`, []byte(`{"elements":[]}`))
	testHandler.UpdateAttachmentContent(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed If-Match PUT: status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// failingMockStorage wraps mockStorage and injects a configurable error on
// Replace so tests can exercise the Storage.Replace failure path.
type failingMockStorage struct {
	mockStorage
	failReplace error
}

func (m *failingMockStorage) Replace(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error) {
	if m.failReplace != nil {
		return "", m.failReplace
	}
	return m.mockStorage.Replace(ctx, key, data, contentType, filename)
}

// TestUpdateAttachmentContent_StorageFailureRollback verifies CAR-797: when
// Storage.Replace fails after the metadata UPDATE (both If-Match and legacy
// paths), the handler rolls back size_bytes/updated_at to the previous values
// so the DB row stays consistent with the blob content.
func TestUpdateAttachmentContent_StorageFailureRollback(t *testing.T) {
	t.Run("if-match-path", func(t *testing.T) {
		testStorageFailureRollback(t, true)
	})
	t.Run("legacy-path", func(t *testing.T) {
		testStorageFailureRollback(t, false)
	})
}

func testStorageFailureRollback(t *testing.T, withIfMatch bool) {
	t.Helper()

	store := &failingMockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	originalBody := []byte(`{"elements":[1]}`)
	id := seedPreviewAttachment(t, &store.mockStorage, "scene-key.excalidraw", "diagram.excalidraw", excalidrawContentType, originalBody)

	// Read original DB state before the PUT.
	var origSizeBytes int64
	var origUpdatedAt time.Time
	if err := testPool.QueryRow(context.Background(),
		`SELECT size_bytes, updated_at FROM attachment WHERE id = $1`, parseUUID(id),
	).Scan(&origSizeBytes, &origUpdatedAt); err != nil {
		t.Fatalf("read original row: %v", err)
	}

	// Make Storage.Replace fail.
	store.failReplace = fmt.Errorf("simulated storage failure")

	var req *http.Request
	var w *httptest.ResponseRecorder
	if withIfMatch {
		etag := fmt.Sprintf(`"%s"`, origUpdatedAt.UTC().Format(time.RFC3339Nano))
		req, w = newUpdateContentRequest(t, id, testWorkspaceID, etag, []byte(`{"elements":[1,2]}`))
	} else {
		req, w = newUpdateContentRequest(t, id, testWorkspaceID, "", []byte(`{"elements":[1,2]}`))
	}
	testHandler.UpdateAttachmentContent(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	// DB row must carry the original size_bytes/updated_at after the rollback.
	var dbSizeBytes int64
	var dbUpdatedAt time.Time
	if err := testPool.QueryRow(context.Background(),
		`SELECT size_bytes, updated_at FROM attachment WHERE id = $1`, parseUUID(id),
	).Scan(&dbSizeBytes, &dbUpdatedAt); err != nil {
		t.Fatalf("read row after rollback: %v", err)
	}
	if dbSizeBytes != origSizeBytes {
		t.Errorf("size_bytes after rollback = %d, want %d (original)", dbSizeBytes, origSizeBytes)
	}
	if !dbUpdatedAt.Equal(origUpdatedAt) {
		t.Errorf("updated_at after rollback = %v, want %v (original)", dbUpdatedAt, origUpdatedAt)
	}

	// The storage file should still hold the original bytes (Replace never ran).
	got, err := io.ReadAll(io.NopCloser(bytes.NewReader(store.files["scene-key.excalidraw"])))
	if err != nil {
		t.Fatalf("read storage: %v", err)
	}
	if !bytes.Equal(got, originalBody) {
		t.Errorf("storage bytes = %q, want %q", got, originalBody)
	}
}
