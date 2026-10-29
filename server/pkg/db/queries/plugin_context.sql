-- name: CompareAndSwapProjectContext :one
UPDATE project SET description = sqlc.arg('content')::text, updated_at = now()
WHERE id = sqlc.arg('id')::uuid AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND COALESCE(description, '') = sqlc.arg('expected_content')::text
RETURNING *;

-- name: CompareAndSwapWorkspaceContext :one
UPDATE workspace SET context = sqlc.arg('content')::text, updated_at = now()
WHERE id = sqlc.arg('id')::uuid AND COALESCE(context, '') = sqlc.arg('expected_content')::text
RETURNING *;

-- name: ListPluginProjectIssueChoices :many
SELECT id, title, number FROM issue
WHERE workspace_id = $1 AND project_id = $2
ORDER BY updated_at DESC, id LIMIT 200;
