package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// extContentTypes overrides http.DetectContentType for extensions it gets wrong.
// Go's sniffer returns text/xml for SVG, text/plain for CSS/JS, etc.
var extContentTypes = map[string]string{
	".svg":        "image/svg+xml",
	".css":        "text/css",
	".js":         "application/javascript",
	".mjs":        "application/javascript",
	".json":       "application/json",
	".wasm":       "application/wasm",
	".excalidraw": "application/vnd.excalidraw+json",
}

const maxUploadSize = 100 << 20 // 100 MB

// maxExcalidrawSceneSize caps the body PUT /api/attachments/{id} will accept
// for in-place Excalidraw saves. Sized so a normal diagram with a handful of
// embedded raster references (`files`) fits, while a runaway scene with
// hundreds of inlined data URIs is rejected instead of silently bloating the
// bucket. Larger diagrams should use linked image attachments, not inline.
const maxExcalidrawSceneSize = 10 << 20 // 10 MB

// excalidrawContentType is the single allowed Content-Type for the in-place
// PUT path. Restricting the type here means the route can never be coerced
// into overwriting an unrelated attachment (e.g. a PDF) with a JSON blob.
const excalidrawContentType = "application/vnd.excalidraw+json"

// maxPreviewTextSize caps the body the preview proxy will load into memory
// for text-based types. Anything larger returns 413 and the UI falls back
// to "please download". Sized so a typical README/source-file fits but a
// 100 MB log dump can't blow up the renderer.
const maxPreviewTextSize = 2 << 20 // 2 MB

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

type AttachmentResponse struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	IssueID       *string `json:"issue_id"`
	CommentID     *string `json:"comment_id"`
	ChatSessionID *string `json:"chat_session_id"`
	ChatMessageID *string `json:"chat_message_id"`
	UploaderType  string  `json:"uploader_type"`
	UploaderID    string  `json:"uploader_id"`
	Filename      string  `json:"filename"`
	URL           string  `json:"url"`
	DownloadURL   string  `json:"download_url"`
	ContentType   string  `json:"content_type"`
	SizeBytes     int64   `json:"size_bytes"`
	CreatedAt     string  `json:"created_at"`
	// UpdatedAt doubles as the optimistic-locking ETag for in-place edits.
	// The client stores it and echoes it back as If-Match on the next PUT
	// (see UpdateAttachmentContent). Serialised with sub-second precision —
	// see attachmentToResponse for why that precision is load-bearing.
	UpdatedAt string `json:"updated_at"`
}

func (h *Handler) attachmentToResponse(a db.Attachment) AttachmentResponse {
	resp := AttachmentResponse{
		ID:           uuidToString(a.ID),
		WorkspaceID:  uuidToString(a.WorkspaceID),
		UploaderType: a.UploaderType,
		UploaderID:   uuidToString(a.UploaderID),
		Filename:     a.Filename,
		URL:          a.Url,
		DownloadURL:  a.Url,
		ContentType:  a.ContentType,
		SizeBytes:    a.SizeBytes,
		CreatedAt:    a.CreatedAt.Time.Format("2006-01-02T15:04:05Z07:00"),
		// RFC3339Nano (not the second-precision format used for CreatedAt):
		// updated_at round-trips back through If-Match into a timestamptz
		// equality check. Truncating to whole seconds would make the echoed
		// value differ from the stored microsecond-precision row and every
		// concurrent-save check would fail with a spurious 412.
		UpdatedAt: a.UpdatedAt.Time.UTC().Format(time.RFC3339Nano),
	}
	if h.CFSigner != nil {
		resp.DownloadURL = h.CFSigner.SignedURL(a.Url, time.Now().Add(30*time.Minute))
	}
	if a.IssueID.Valid {
		s := uuidToString(a.IssueID)
		resp.IssueID = &s
	}
	if a.CommentID.Valid {
		s := uuidToString(a.CommentID)
		resp.CommentID = &s
	}
	if a.ChatSessionID.Valid {
		s := uuidToString(a.ChatSessionID)
		resp.ChatSessionID = &s
	}
	if a.ChatMessageID.Valid {
		s := uuidToString(a.ChatMessageID)
		resp.ChatMessageID = &s
	}
	return resp
}

// groupAttachments loads attachments for multiple comments and groups them by comment ID.
func (h *Handler) groupAttachments(r *http.Request, commentIDs []pgtype.UUID) map[string][]AttachmentResponse {
	if len(commentIDs) == 0 {
		return nil
	}
	workspaceID := h.resolveWorkspaceID(r)
	attachments, err := h.Queries.ListAttachmentsByCommentIDs(r.Context(), db.ListAttachmentsByCommentIDsParams{
		Column1:     commentIDs,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		slog.Error("failed to load attachments for comments", "error", err)
		return nil
	}
	grouped := make(map[string][]AttachmentResponse, len(commentIDs))
	for _, a := range attachments {
		cid := uuidToString(a.CommentID)
		grouped[cid] = append(grouped[cid], h.attachmentToResponse(a))
	}
	return grouped
}

// groupChatMessageAttachments loads attachments for multiple chat messages
// and groups them by chat_message_id. Mirrors groupAttachments — used so the
// chat message list can surface attachment metadata to the UI bubble (file
// cards, click-through download) without an N+1 query per message.
func (h *Handler) groupChatMessageAttachments(ctx context.Context, workspaceID string, messageIDs []pgtype.UUID) map[string][]AttachmentResponse {
	if len(messageIDs) == 0 {
		return nil
	}
	attachments, err := h.Queries.ListAttachmentsByChatMessageIDs(ctx, db.ListAttachmentsByChatMessageIDsParams{
		Column1:     messageIDs,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		slog.Error("failed to load attachments for chat messages", "error", err)
		return nil
	}
	grouped := make(map[string][]AttachmentResponse, len(messageIDs))
	for _, a := range attachments {
		mid := uuidToString(a.ChatMessageID)
		grouped[mid] = append(grouped[mid], h.attachmentToResponse(a))
	}
	return grouped
}

// ---------------------------------------------------------------------------
// UploadFile — POST /api/upload-file
// ---------------------------------------------------------------------------

func (h *Handler) UploadFile(w http.ResponseWriter, r *http.Request) {
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "file upload not configured")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "file too large or invalid multipart form")
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("missing file field: %v", err))
		return
	}
	defer file.Close()

	// Sniff actual content type from file bytes instead of trusting the client header.
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "failed to read file")
		return
	}
	contentType := http.DetectContentType(buf[:n])
	// Override with extension-based type when the sniffer gets it wrong.
	if ct, ok := extContentTypes[strings.ToLower(path.Ext(header.Filename))]; ok {
		contentType = ct
	}
	// Seek back so the full file is uploaded.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read file")
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read file")
		return
	}

	// Generate a UUIDv7 to use as both the attachment ID and S3 key.
	id, err := uuid.NewV7()
	if err != nil {
		slog.Error("failed to generate uuid", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	filename := id.String() + path.Ext(header.Filename)
	var key string
	if workspaceID != "" {
		key = "workspaces/" + workspaceID + "/" + filename
	} else {
		key = "users/" + userID + "/" + filename
	}

	// If workspace context is available, validate membership before uploading.
	if workspaceID != "" {
		if _, err := h.getWorkspaceMember(r.Context(), userID, workspaceID); err != nil {
			writeError(w, http.StatusForbidden, "not a member of this workspace")
			return
		}

		uploaderType, uploaderID := h.resolveActor(r, userID, workspaceID)

		params := db.CreateAttachmentParams{
			ID:           pgtype.UUID{Bytes: id, Valid: true},
			WorkspaceID:  parseUUID(workspaceID),
			UploaderType: uploaderType,
			UploaderID:   parseUUID(uploaderID),
			Filename:     header.Filename,
			ContentType:  contentType,
			SizeBytes:    int64(len(data)),
		}

		if issueID := r.FormValue("issue_id"); issueID != "" {
			issueUUID, ok := parseUUIDOrBadRequest(w, issueID, "issue_id")
			if !ok {
				return
			}
			issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
				ID:          issueUUID,
				WorkspaceID: parseUUID(workspaceID),
			})
			if err != nil {
				writeError(w, http.StatusForbidden, "invalid issue_id")
				return
			}
			params.IssueID = issue.ID
		}
		if commentID := r.FormValue("comment_id"); commentID != "" {
			commentUUID, ok := parseUUIDOrBadRequest(w, commentID, "comment_id")
			if !ok {
				return
			}
			comment, err := h.Queries.GetComment(r.Context(), commentUUID)
			if err != nil || uuidToString(comment.WorkspaceID) != workspaceID {
				writeError(w, http.StatusForbidden, "invalid comment_id")
				return
			}
			params.CommentID = comment.ID
		}
		if chatSessionID := r.FormValue("chat_session_id"); chatSessionID != "" {
			// Re-use the existing private-agent gate so the user can still
			// reach this session — covers role downgrade and agent
			// visibility flips. The gate writes 4xx on failure.
			session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, chatSessionID)
			if !ok {
				return
			}
			params.ChatSessionID = session.ID
		}

		link, err := h.Storage.Upload(r.Context(), key, data, contentType, header.Filename)
		if err != nil {
			slog.Error("file upload failed", "error", err)
			writeError(w, http.StatusInternalServerError, "upload failed")
			return
		}
		params.Url = link

		att, err := h.Queries.CreateAttachment(r.Context(), params)
		if err != nil {
			slog.Error("failed to create attachment record", "error", err)
			// S3 upload succeeded but DB record failed — still return the link
			// so the file is usable. Log the error for investigation.
		} else {
			writeJSON(w, http.StatusOK, h.attachmentToResponse(att))
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{
			"id":       "",
			"url":      link,
			"filename": header.Filename,
		})
		return
	}

	// No workspace context (e.g. avatar upload) — upload directly.
	link, err := h.Storage.Upload(r.Context(), key, data, contentType, header.Filename)
	if err != nil {
		slog.Error("file upload failed", "error", err)
		writeError(w, http.StatusInternalServerError, "upload failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id":       id.String(),
		"url":      link,
		"filename": header.Filename,
	})
}

// ---------------------------------------------------------------------------
// ListAttachments — GET /api/issues/{id}/attachments
// ---------------------------------------------------------------------------

func (h *Handler) ListAttachments(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	attachments, err := h.Queries.ListAttachmentsByIssue(r.Context(), db.ListAttachmentsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Error("failed to list attachments", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list attachments")
		return
	}

	resp := make([]AttachmentResponse, len(attachments))
	for i, a := range attachments {
		resp[i] = h.attachmentToResponse(a)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// GetAttachmentByID — GET /api/attachments/{id}
// ---------------------------------------------------------------------------

func (h *Handler) GetAttachmentByID(w http.ResponseWriter, r *http.Request) {
	attachmentID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}

	attUUID, ok := parseUUIDOrBadRequest(w, attachmentID, "attachment id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	att, err := h.Queries.GetAttachment(r.Context(), db.GetAttachmentParams{
		ID:          attUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}

	// Expose the optimistic-locking token as a standard ETag too, so an
	// HTTP-aware client can use If-Match directly. The JSON `updated_at`
	// field carries the same value for clients that read the body.
	if tag := formatAttachmentETag(att.UpdatedAt); tag != "" {
		w.Header().Set("ETag", tag)
	}
	writeJSON(w, http.StatusOK, h.attachmentToResponse(att))
}

// ---------------------------------------------------------------------------
// GetAttachmentContent — GET /api/attachments/{id}/content
//
// Streams the raw bytes of a text-previewable attachment back to the client.
// Exists to (a) bypass CloudFront CORS (not configured) and (b) bypass
// Content-Disposition: attachment which Chromium honors for iframe document
// loads. Media types (image/video/audio/pdf) intentionally do NOT go through
// this endpoint — clients render them directly from the CloudFront signed
// download_url, which already serves them with Content-Disposition: inline
// (see storage/util.go isInlineContentType).
//
// Hard cap: 2 MB. Larger files return 413. Anything outside the text
// whitelist returns 415.
// ---------------------------------------------------------------------------

func (h *Handler) GetAttachmentContent(w http.ResponseWriter, r *http.Request) {
	attachmentID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}

	attUUID, ok := parseUUIDOrBadRequest(w, attachmentID, "attachment id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	att, err := h.Queries.GetAttachment(r.Context(), db.GetAttachmentParams{
		ID:          attUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}

	if !isTextPreviewable(att.ContentType, att.Filename) {
		writeError(w, http.StatusUnsupportedMediaType, "preview not supported for this file type")
		return
	}

	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage not configured")
		return
	}
	key := h.Storage.KeyFromURL(att.Url)
	reader, err := h.Storage.GetReader(r.Context(), key)
	if err != nil {
		slog.Error("failed to open attachment for preview", "id", attachmentID, "key", key, "error", err)
		writeError(w, http.StatusNotFound, "attachment object not found")
		return
	}
	defer reader.Close()

	// LimitReader to maxPreviewTextSize+1 so we can detect "exactly at the
	// limit" vs "exceeds the limit" by checking the returned length.
	body, err := io.ReadAll(io.LimitReader(reader, maxPreviewTextSize+1))
	if err != nil {
		slog.Error("failed to read attachment body for preview", "id", attachmentID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to read attachment body")
		return
	}
	if len(body) > maxPreviewTextSize {
		writeError(w, http.StatusRequestEntityTooLarge, "file too large for inline preview")
		return
	}

	// Always reply as text/plain so a hostile HTML payload can't be
	// re-interpreted as a document by the browser. The original MIME is
	// surfaced via X-Original-Content-Type for the client-side dispatcher.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Original-Content-Type", att.ContentType)
	// No-store: workspace membership / attachment ACL can change between
	// requests (member removed, attachment deleted). A cached body would
	// stay readable past the revocation window. The redundant request is
	// fine here — bodies are capped at 2 MB and the endpoint is only hit
	// when a user explicitly opens a preview.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	if _, err := w.Write(body); err != nil {
		slog.Error("failed to write attachment preview body", "id", attachmentID, "error", err)
	}
}

// isTextPreviewable is the whitelist for the text preview proxy.
//
// IMPORTANT — KEEP IN SYNC with the client-side mirror in
// packages/views/editor/utils/preview.ts (TEXT_EXTENSIONS / TEXT_CONTENT_TYPES
// / TEXT_BASENAMES + extensionToLanguage). If a type is allowed here but not
// mapped client-side the user sees raw unhighlighted text; if mapped client-side
// but rejected here the user sees a 415 fallback.
//
// TODO(follow-up): extract this list to a JSON single-source-of-truth and
// generate the TS side, mirroring the reserved-slugs pattern (see
// server/internal/handler/reserved_slugs.json + scripts/generate-reserved-slugs.mjs).
// Drift severity here is low (worst case: Eye button visible but proxy 415s,
// modal shows the unsupported fallback — still functional, just confusing),
// so it ships as manual hand-sync for v1.
//
// We check both content_type and extension because http.DetectContentType
// regularly returns "text/plain" for Markdown / source code, so a pure
// content-type check would 415 those.
func isTextPreviewable(contentType, filename string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	// Strip params (e.g. "text/plain; charset=utf-8")
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = strings.TrimSpace(ct[:idx])
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json",
		"application/javascript",
		"application/xml",
		"application/x-yaml",
		"application/yaml",
		"application/toml",
		"application/x-sh",
		"application/x-httpd-php":
		return true
	}

	ext := strings.ToLower(path.Ext(filename))
	switch ext {
	case ".md", ".markdown",
		".txt", ".log",
		".csv", ".tsv",
		".html", ".htm",
		".json", ".xml",
		".yml", ".yaml", ".toml", ".ini", ".conf",
		".sh", ".bash", ".zsh",
		".py", ".rb", ".go", ".rs",
		".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".css", ".scss", ".sass", ".less",
		".sql",
		".java", ".kt", ".swift",
		".c", ".cc", ".cpp", ".h", ".hpp",
		".cs", ".php", ".lua", ".vim",
		".dockerfile", ".makefile", ".gitignore":
		return true
	}
	// Filenames without extension that match well-known build files.
	base := strings.ToLower(path.Base(filename))
	switch base {
	case "dockerfile", "makefile", ".env":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// DeleteAttachment — DELETE /api/attachments/{id}
// ---------------------------------------------------------------------------

func (h *Handler) DeleteAttachment(w http.ResponseWriter, r *http.Request) {
	attachmentID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	attUUID, ok := parseUUIDOrBadRequest(w, attachmentID, "attachment id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	att, err := h.Queries.GetAttachment(r.Context(), db.GetAttachmentParams{
		ID:          attUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}

	// Only the uploader (or workspace admin) can delete
	uploaderID := uuidToString(att.UploaderID)
	isUploader := att.UploaderType == "member" && uploaderID == userID
	member, hasMember := ctxMember(r.Context())
	isAdmin := hasMember && (member.Role == "admin" || member.Role == "owner")

	if !isUploader && !isAdmin {
		writeError(w, http.StatusForbidden, "not authorized to delete this attachment")
		return
	}

	if err := h.Queries.DeleteAttachment(r.Context(), db.DeleteAttachmentParams{
		ID:          att.ID,
		WorkspaceID: att.WorkspaceID,
	}); err != nil {
		slog.Error("failed to delete attachment", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete attachment")
		return
	}

	h.deleteS3Object(r.Context(), att.Url)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// UpdateAttachmentContent — PUT /api/attachments/{id}
//
// In-place overwrite of an Excalidraw attachment body. Same id, same URL,
// new bytes — so the description-editor inline preview never has to rebind
// when the user re-opens the editor and saves. Restricted to
// application/vnd.excalidraw+json so this endpoint cannot be coerced into
// rewriting arbitrary attachments (PDFs, images) via a crafted PUT.
// ---------------------------------------------------------------------------

func (h *Handler) UpdateAttachmentContent(w http.ResponseWriter, r *http.Request) {
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "file storage not configured")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}

	attachmentID := chi.URLParam(r, "id")
	attUUID, ok := parseUUIDOrBadRequest(w, attachmentID, "attachment id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	// Workspace membership gate. The save UI is collaborative (anyone with
	// access to the issue can edit the diagram), so we don't restrict to the
	// uploader as DeleteAttachment does — but we do require membership.
	if _, err := h.getWorkspaceMember(r.Context(), userID, workspaceID); err != nil {
		writeError(w, http.StatusForbidden, "not a member of this workspace")
		return
	}

	att, err := h.Queries.GetAttachment(r.Context(), db.GetAttachmentParams{
		ID:          attUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}

	// Type guard: refuse to overwrite anything other than an Excalidraw
	// scene. Without this, a misbehaving (or hostile) client could PUT a
	// JSON blob over a PDF / image and silently corrupt unrelated data.
	if att.ContentType != excalidrawContentType {
		writeError(w, http.StatusUnsupportedMediaType, "in-place edit only supported for Excalidraw attachments")
		return
	}

	// Reject mismatched request Content-Type early so the client gets a
	// clear 415 instead of a confusing 200 with the wrong type written.
	if ct := strings.TrimSpace(r.Header.Get("Content-Type")); ct != "" {
		if idx := strings.Index(ct, ";"); idx >= 0 {
			ct = strings.TrimSpace(ct[:idx])
		}
		if !strings.EqualFold(ct, excalidrawContentType) {
			writeError(w, http.StatusUnsupportedMediaType, "expected Content-Type "+excalidrawContentType)
			return
		}
	}

	// Read up to maxExcalidrawSceneSize+1 so we can distinguish "exactly at
	// the cap" from "exceeds the cap" without trusting Content-Length.
	r.Body = http.MaxBytesReader(w, r.Body, maxExcalidrawSceneSize+1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "scene too large")
		return
	}
	if int64(len(body)) > maxExcalidrawSceneSize {
		writeError(w, http.StatusRequestEntityTooLarge, "scene too large")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty body")
		return
	}

	key := h.Storage.KeyFromURL(att.Url)
	if key == "" {
		slog.Error("attachment has no resolvable storage key", "id", attachmentID, "url", att.Url)
		writeError(w, http.StatusInternalServerError, "attachment storage key missing")
		return
	}

	// CAR-794 — Lost-Update-Protection. The save UI is collaborative: two
	// tabs can PUT the same scene concurrently and, without a version check,
	// the second writer silently clobbers the first. The client echoes the
	// attachment's last-seen `updated_at` back as an If-Match header; we
	// refuse the write if the row has moved on since that read.
	//
	// If-Match is intentionally optional. A desktop build predates any
	// server it eventually talks to (see API Response Compatibility in
	// CLAUDE.md), so an older client that never learned the header must
	// still be able to save — it just does so without conflict detection.
	var expectedUpdatedAt pgtype.Timestamptz
	if ifMatch := parseIfMatch(r.Header.Get("If-Match")); ifMatch != "" {
		t, perr := time.Parse(time.RFC3339Nano, ifMatch)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "invalid If-Match header: expected an RFC3339 timestamp ETag")
			return
		}
		expectedUpdatedAt = pgtype.Timestamptz{Time: t, Valid: true}

		// Fast-path conflict check against the row we already loaded. This
		// rejects the common (sequential) two-tab conflict *before* the
		// storage write, so the loser's bytes never overwrite the winner's.
		// The conditional in UpdateAttachmentContent below remains the
		// authoritative guard for the narrow read-to-write race.
		if !att.UpdatedAt.Time.Equal(t) {
			w.Header().Set("ETag", formatAttachmentETag(att.UpdatedAt))
			writeError(w, http.StatusPreconditionFailed, "attachment was modified by someone else; reload and retry")
			return
		}
	}

	// DB-first ordering is deliberate. The conditional UPDATE atomically
	// elects a single winner among concurrent PUTs; only that winner reaches
	// the storage write below. Writing storage first would let every racing
	// request clobber the bucket and only *then* discover the conflict —
	// last-write-wins at the storage layer, exactly what this fix removes.
	updated, err := h.Queries.UpdateAttachmentContent(r.Context(), db.UpdateAttachmentContentParams{
		ID:                att.ID,
		WorkspaceID:       att.WorkspaceID,
		SizeBytes:         int64(len(body)),
		ContentType:       excalidrawContentType,
		ExpectedUpdatedAt: expectedUpdatedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Zero rows matched. With an If-Match present this is a lost-update
		// conflict (the row advanced between our read and this write);
		// without one the only way to match zero rows is the attachment
		// having been deleted concurrently.
		if expectedUpdatedAt.Valid {
			w.Header().Set("ETag", formatAttachmentETag(att.UpdatedAt))
			writeError(w, http.StatusPreconditionFailed, "attachment was modified by someone else; reload and retry")
			return
		}
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	if err != nil {
		slog.Error("failed to update attachment record", "id", attachmentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save attachment")
		return
	}

	// Metadata is committed and this request owns the write. Overwrite the
	// stored bytes last. A failure here leaves updated_at/size_bytes briefly
	// ahead of the bucket; the next successful save reconciles both.
	if _, err := h.Storage.Replace(r.Context(), key, body, excalidrawContentType, att.Filename); err != nil {
		slog.Error("attachment replace failed after metadata update", "id", attachmentID, "key", key, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save attachment")
		return
	}

	w.Header().Set("ETag", formatAttachmentETag(updated.UpdatedAt))
	writeJSON(w, http.StatusOK, h.attachmentToResponse(updated))
}

// parseIfMatch normalises an If-Match header value down to the bare ETag
// payload. It tolerates the optional weak-validator prefix and the
// surrounding double quotes mandated by RFC 7232, so a spec-compliant client
// and one that sends the raw updated_at string both interoperate. An empty
// header or the wildcard "*" yields "" (no precondition).
func parseIfMatch(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" || v == "*" {
		return ""
	}
	v = strings.TrimSpace(strings.TrimPrefix(v, "W/"))
	v = strings.Trim(v, "\"")
	return strings.TrimSpace(v)
}

// formatAttachmentETag renders an attachment's updated_at as a strong,
// double-quoted ETag. RFC3339Nano is lossless for the microsecond precision
// Postgres stores, so the value round-trips through the client and back into
// a timestamptz equality check without drift. Returns "" for a NULL/zero
// timestamp so callers can skip emitting an empty header.
func formatAttachmentETag(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return "\"" + ts.Time.UTC().Format(time.RFC3339Nano) + "\""
}

// ---------------------------------------------------------------------------
// Attachment linking
// ---------------------------------------------------------------------------

// linkAttachmentsByIssueIDs links the given attachment IDs to an issue.
// Only updates attachments that have no issue_id yet.
func (h *Handler) linkAttachmentsByIssueIDs(ctx context.Context, issueID, workspaceID pgtype.UUID, ids []pgtype.UUID) {
	if err := h.Queries.LinkAttachmentsToIssue(ctx, db.LinkAttachmentsToIssueParams{
		IssueID:     issueID,
		WorkspaceID: workspaceID,
		Column3:     ids,
	}); err != nil {
		slog.Error("failed to link attachments to issue", "error", err)
	}
}

// linkAttachmentsByIDs links the given attachment IDs to a comment.
// Only updates attachments that belong to the same issue and have no comment_id yet.
func (h *Handler) linkAttachmentsByIDs(ctx context.Context, commentID, issueID pgtype.UUID, ids []pgtype.UUID) {
	if err := h.Queries.LinkAttachmentsToComment(ctx, db.LinkAttachmentsToCommentParams{
		CommentID: commentID,
		IssueID:   issueID,
		Column3:   ids,
	}); err != nil {
		slog.Error("failed to link attachments to comment", "error", err)
	}
}

// deleteS3Object removes a single file from S3 by its CDN URL.
func (h *Handler) deleteS3Object(ctx context.Context, url string) {
	if h.Storage == nil || url == "" {
		return
	}
	h.Storage.Delete(ctx, h.Storage.KeyFromURL(url))
}

// deleteS3Objects removes multiple files from S3 by their CDN URLs.
func (h *Handler) deleteS3Objects(ctx context.Context, urls []string) {
	if h.Storage == nil || len(urls) == 0 {
		return
	}
	keys := make([]string, len(urls))
	for i, u := range urls {
		keys[i] = h.Storage.KeyFromURL(u)
	}
	h.Storage.DeleteKeys(ctx, keys)
}
