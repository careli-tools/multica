package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// CAR-794 — ETag / If-Match Lost-Update-Protection for PUT /api/attachments/{id}
// ---------------------------------------------------------------------------

func TestParseIfMatch(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"wildcard", "*", ""},
		{"bare value", "2026-05-20T17:00:00Z", "2026-05-20T17:00:00Z"},
		{"quoted value", "\"2026-05-20T17:00:00Z\"", "2026-05-20T17:00:00Z"},
		{"weak validator", "W/\"2026-05-20T17:00:00Z\"", "2026-05-20T17:00:00Z"},
		{"padded", "  \"2026-05-20T17:00:00.5Z\"  ", "2026-05-20T17:00:00.5Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseIfMatch(c.in); got != c.want {
				t.Errorf("parseIfMatch(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// seedExcalidrawAttachment inserts an attachment row with the Excalidraw
// content type and registers its bytes with the supplied mockStorage so the
// PUT handler's Storage.Replace round-trips. Returns the new attachment id.
func seedExcalidrawAttachment(t *testing.T, store *mockStorage, key string, body []byte) string {
	t.Helper()
	url, err := store.Upload(context.Background(), key, body, excalidrawContentType, "diagram.excalidraw")
	if err != nil {
		t.Fatalf("seed Upload: %v", err)
	}
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO attachment (workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		VALUES ($1, 'member', $2, $3, $4, $5, $6)
		RETURNING id::text
	`, testWorkspaceID, testUserID, "diagram.excalidraw", url, excalidrawContentType, len(body)).Scan(&id); err != nil {
		t.Fatalf("seed attachment row: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, id)
	})
	return id
}

// readAttachmentUpdatedAt fetches the attachment over GetAttachmentByID and
// returns the `updated_at` string exactly as a real client would read it —
// the value that gets echoed back as If-Match.
func readAttachmentUpdatedAt(t *testing.T, id string) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/attachments/"+id, nil)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	testHandler.GetAttachmentByID(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetAttachmentByID status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp AttachmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode attachment response: %v", err)
	}
	if resp.UpdatedAt == "" {
		t.Fatal("attachment response carried an empty updated_at")
	}
	return resp.UpdatedAt
}

// putExcalidraw issues a PUT /api/attachments/{id} with the given body and
// optional If-Match header. An empty ifMatch omits the header entirely.
func putExcalidraw(id, ifMatch string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/api/attachments/"+id, bytes.NewReader(body))
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("Content-Type", excalidrawContentType)
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	testHandler.UpdateAttachmentContent(w, req)
	return w
}

func withMockStorage(t *testing.T) *mockStorage {
	t.Helper()
	store := &mockStorage{}
	orig := testHandler.Storage
	testHandler.Storage = store
	t.Cleanup(func() { testHandler.Storage = orig })
	return store
}

// Without an If-Match header the write goes through unconditionally — an
// older client that predates the header must still be able to save.
func TestUpdateAttachmentContent_NoIfMatch_PassesThrough(t *testing.T) {
	store := withMockStorage(t)
	id := seedExcalidrawAttachment(t, store, "etag-passthrough.excalidraw", []byte(`{"elements":[]}`))

	before := readAttachmentUpdatedAt(t, id)
	newBody := []byte(`{"elements":[{"id":"a"}]}`)
	w := putExcalidraw(id, "", newBody)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := store.files["etag-passthrough.excalidraw"]; !bytes.Equal(got, newBody) {
		t.Errorf("stored bytes = %q, want %q", got, newBody)
	}
	if after := readAttachmentUpdatedAt(t, id); after == before {
		t.Errorf("updated_at did not advance after a successful write (still %q)", after)
	}
}

// A PUT carrying the attachment's current updated_at as If-Match succeeds and
// the response advances the ETag.
func TestUpdateAttachmentContent_MatchingIfMatch_Succeeds(t *testing.T) {
	store := withMockStorage(t)
	id := seedExcalidrawAttachment(t, store, "etag-match.excalidraw", []byte(`{"elements":[]}`))

	current := readAttachmentUpdatedAt(t, id)
	w := putExcalidraw(id, current, []byte(`{"elements":[{"id":"b"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp AttachmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.UpdatedAt == current {
		t.Errorf("updated_at = %q, expected it to advance past the If-Match value", resp.UpdatedAt)
	}
	if tag := w.Header().Get("ETag"); tag == "" {
		t.Error("successful PUT did not set an ETag header")
	}
}

// A stale If-Match (the row moved on since the client read it) is rejected
// with 412 and — critically — the stored bytes are left untouched.
func TestUpdateAttachmentContent_StaleIfMatch_Returns412(t *testing.T) {
	store := withMockStorage(t)
	original := []byte(`{"elements":[]}`)
	id := seedExcalidrawAttachment(t, store, "etag-stale.excalidraw", original)

	stale := readAttachmentUpdatedAt(t, id)

	// A first writer advances the row, invalidating `stale`.
	if w := putExcalidraw(id, stale, []byte(`{"elements":[{"id":"winner"}]}`)); w.Code != http.StatusOK {
		t.Fatalf("first write status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	winnerBytes := store.files["etag-stale.excalidraw"]

	// Second writer still holds the now-stale token.
	loser := putExcalidraw(id, stale, []byte(`{"elements":[{"id":"loser"}]}`))
	if loser.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale write status = %d, want 412; body=%s", loser.Code, loser.Body.String())
	}
	if got := store.files["etag-stale.excalidraw"]; !bytes.Equal(got, winnerBytes) {
		t.Errorf("stored bytes were clobbered by the rejected write: got %q, want %q", got, winnerBytes)
	}
}

// A syntactically invalid If-Match is a client error, not a conflict.
func TestUpdateAttachmentContent_InvalidIfMatch_Returns400(t *testing.T) {
	store := withMockStorage(t)
	id := seedExcalidrawAttachment(t, store, "etag-invalid.excalidraw", []byte(`{"elements":[]}`))

	w := putExcalidraw(id, "not-a-timestamp", []byte(`{"elements":[{"id":"x"}]}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// Acceptance criterion: two tabs that loaded the same version and then both
// PUT concurrently — exactly one wins, every other writer gets a 412.
func TestUpdateAttachmentContent_ConcurrentPuts_ExactlyOneWins(t *testing.T) {
	store := withMockStorage(t)
	id := seedExcalidrawAttachment(t, store, "etag-concurrent.excalidraw", []byte(`{"elements":[]}`))

	shared := readAttachmentUpdatedAt(t, id)
	const writers = 5

	var wg sync.WaitGroup
	codes := make([]int, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			body := []byte(`{"elements":[{"id":"tab"}]}`)
			codes[idx] = putExcalidraw(id, shared, body).Code
		}(i)
	}
	wg.Wait()

	wins, conflicts := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			wins++
		case http.StatusPreconditionFailed:
			conflicts++
		default:
			t.Errorf("unexpected status %d among concurrent writers", c)
		}
	}
	if wins != 1 {
		t.Errorf("winners = %d, want exactly 1 (codes: %v)", wins, codes)
	}
	if conflicts != writers-1 {
		t.Errorf("conflicts = %d, want %d (codes: %v)", conflicts, writers-1, codes)
	}
}

// The ETag derived from updated_at must survive a JSON round-trip without
// drift, otherwise every second save would 412 spuriously (reviewer #5).
func TestAttachmentETag_RoundTripsLosslessly(t *testing.T) {
	store := withMockStorage(t)
	id := seedExcalidrawAttachment(t, store, "etag-roundtrip.excalidraw", []byte(`{"elements":[]}`))

	// Two saves in a row: the second must accept the updated_at the first
	// returned. A precision mismatch would surface as a 412 here.
	tag := readAttachmentUpdatedAt(t, id)
	for i := 0; i < 3; i++ {
		w := putExcalidraw(id, tag, []byte(`{"elements":[{"id":"n"}]}`))
		if w.Code != http.StatusOK {
			t.Fatalf("save %d status = %d, want 200; body=%s", i, w.Code, w.Body.String())
		}
		var resp AttachmentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode save %d: %v", i, err)
		}
		if _, err := time.Parse(time.RFC3339Nano, resp.UpdatedAt); err != nil {
			t.Fatalf("updated_at %q is not parseable RFC3339Nano: %v", resp.UpdatedAt, err)
		}
		tag = resp.UpdatedAt
	}
}
