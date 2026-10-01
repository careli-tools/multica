package handler

import (
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"net/http/httptest"
	"testing"
)

func TestPluginProjectContextCompareAndSwap(t *testing.T) {
	id := installPluginForAction(t, []string{"projects:read", "projects:write"})
	project := dbfx.Project(t, "Context target", testutil.Cols{"description": "Manual text"})
	params := map[string]string{"project_id": project}
	call := func(expected, content string) int {
		w := httptest.NewRecorder()
		testHandler.PatchPluginProjectContext(w, pluginActionRequest("PATCH", "/projects/"+project+"/context", id, map[string]string{"expected_content": expected, "content": content}, params))
		return w.Code
	}
	if got := call("Manual text", "Manual text\nGenerated"); got != 200 {
		t.Fatalf("first update: %d", got)
	}
	if got := call("Manual text", "Overwrite"); got != 409 {
		t.Fatalf("stale update: %d", got)
	}
	var content string
	if err := testPool.QueryRow(t.Context(), "SELECT description FROM project WHERE id=$1", project).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "Manual text\nGenerated" {
		t.Fatalf("content was lost: %q", content)
	}
}

func TestPluginContextWriteRequiresScope(t *testing.T) {
	id := installPluginForAction(t, []string{"projects:read"})
	project := dbfx.Project(t, "Read only")
	w := httptest.NewRecorder()
	testHandler.PatchPluginProjectContext(w, pluginActionRequest("PATCH", "/projects/x/context", id, map[string]string{"expected_content": "", "content": "x"}, map[string]string{"project_id": project}))
	if w.Code != 403 {
		t.Fatalf("ungranted write: %d", w.Code)
	}
}

func TestPluginWorkspaceContextRequiresAdminAndCAS(t *testing.T) {
	id := installPluginForAction(t, []string{"workspace:read", "workspace:write"})
	var before string
	if err := testPool.QueryRow(t.Context(), "SELECT COALESCE(context,'') FROM workspace WHERE id=$1", testWorkspaceID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	dbfx.Cleanup(t, "UPDATE workspace SET context=$1 WHERE id=$2", before, testWorkspaceID)
	call := func(expected string) int {
		w := httptest.NewRecorder()
		testHandler.PatchPluginWorkspaceContext(w, pluginActionRequest("PATCH", "/workspace/context", id, map[string]string{"expected_content": expected, "content": "test context"}, nil))
		return w.Code
	}
	if got := call(before); got != 200 {
		t.Fatalf("write: %d", got)
	}
	if got := call(before + "stale"); got != 409 {
		t.Fatalf("stale write: %d", got)
	}
	var role string
	if err := testPool.QueryRow(t.Context(), "SELECT role FROM member WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, testUserID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	dbfx.Cleanup(t, "UPDATE member SET role=$1 WHERE workspace_id=$2 AND user_id=$3", role, testWorkspaceID, testUserID)
	dbfx.Exec(t, "UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, testUserID)
	if got := call("test context"); got != 403 {
		t.Fatalf("non-admin write: %d", got)
	}
}

func TestPluginProjectCallbackCannotEscapeTarget(t *testing.T) {
	id := installPluginForAction(t, []string{"projects:read", "workspace:read", "issues:read"})
	withCallbackTokens(t)
	project := dbfx.Project(t, "Target")
	other := dbfx.Project(t, "Other")
	installation, err := testHandler.PluginService.InstallationForWorkspace(t.Context(), parseUUID(testWorkspaceID), id)
	if err != nil {
		t.Fatal(err)
	}
	token, err := testHandler.PluginService.Callbacks.Issue(t.Context(), service.HookInvocation{Installation: installation, ProjectID: parseUUID(project), Actor: service.HookActor{Type: "member", ID: parseUUID(testUserID)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		id     string
		status int
	}{{project, 200}, {other, 404}} {
		w := httptest.NewRecorder()
		testHandler.GetPluginProjectContext(w, callbackRequest(token, "GET", "/projects/x/context", nil, map[string]string{"project_id": target.id}))
		if w.Code != target.status {
			t.Fatalf("project read got %d, want %d", w.Code, target.status)
		}
	}
	w := httptest.NewRecorder()
	testHandler.GetPluginWorkspaceContext(w, callbackRequest(token, "GET", "/workspace/context", nil, nil))
	if w.Code != 403 {
		t.Fatalf("workspace escape: %d", w.Code)
	}
}
func TestPluginProjectContextCannotCrossWorkspace(t *testing.T) {
	id := installPluginForAction(t, []string{"projects:read", "projects:write"})
	ws := dbfx.Workspace(t, "Foreign context", "foreign-context-test")
	project := dbfx.Project(t, "Foreign", testutil.Cols{"workspace_id": ws})
	w := httptest.NewRecorder()
	testHandler.GetPluginProjectContext(w, pluginActionRequest("GET", "/projects/x/context", id, nil, map[string]string{"project_id": project}))
	if w.Code != 404 {
		t.Fatalf("cross workspace: %d", w.Code)
	}
}
