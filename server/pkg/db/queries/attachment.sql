-- name: CreateAttachment :one
INSERT INTO attachment (
  id, workspace_id, issue_id, comment_id, chat_session_id,
  uploader_type, uploader_id, filename, url, content_type, size_bytes
)
VALUES (
  $1, $2, sqlc.narg(issue_id), sqlc.narg(comment_id), sqlc.narg(chat_session_id),
  $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: ListAttachmentsByIssue :many
SELECT * FROM attachment
WHERE issue_id = $1 AND workspace_id = $2
ORDER BY created_at ASC;

-- name: ListAttachmentsByComment :many
SELECT * FROM attachment
WHERE comment_id = $1 AND workspace_id = $2
ORDER BY created_at ASC;

-- name: GetAttachment :one
SELECT * FROM attachment
WHERE id = $1 AND workspace_id = $2;

-- name: ListAttachmentsByCommentIDs :many
SELECT * FROM attachment
WHERE comment_id = ANY($1::uuid[]) AND workspace_id = $2
ORDER BY created_at ASC;

-- name: ListAttachmentURLsByIssueOrComments :many
SELECT a.url FROM attachment a
WHERE a.issue_id = $1
   OR a.comment_id IN (SELECT c.id FROM comment c WHERE c.issue_id = $1);

-- name: ListAttachmentURLsByCommentID :many
SELECT url FROM attachment
WHERE comment_id = $1;

-- name: LinkAttachmentsToComment :exec
UPDATE attachment
SET comment_id = $1
WHERE issue_id = $2
  AND comment_id IS NULL
  AND id = ANY($3::uuid[]);

-- name: LinkAttachmentsToChatMessage :exec
UPDATE attachment
SET chat_message_id = $1
WHERE chat_session_id = $2
  AND chat_message_id IS NULL
  AND id = ANY($3::uuid[]);

-- name: ListAttachmentsByChatMessage :many
SELECT * FROM attachment
WHERE chat_message_id = $1 AND workspace_id = $2
ORDER BY created_at ASC;

-- name: ListAttachmentsByChatMessageIDs :many
SELECT * FROM attachment
WHERE chat_message_id = ANY($1::uuid[]) AND workspace_id = $2
ORDER BY created_at ASC;

-- name: LinkAttachmentsToIssue :exec
UPDATE attachment
SET issue_id = $1
WHERE workspace_id = $2
  AND issue_id IS NULL
  AND id = ANY($3::uuid[]);

-- name: UpdateAttachmentContent :one
-- Unconditional in-place content update. Used only on the backwards-compat
-- path where the client sends no If-Match header (older bundles): last
-- write wins, as before CAR-794.
UPDATE attachment
SET size_bytes = $3, content_type = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: UpdateAttachmentContentIfMatch :one
-- Optimistic-locking content update (CAR-794): only succeeds while the
-- row's updated_at still equals the value the client read (carried in the
-- If-Match header). A concurrent save bumps updated_at, so the losing PUT
-- matches zero rows -- the handler turns that into 412 Precondition Failed.
UPDATE attachment
SET size_bytes = $3, content_type = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND updated_at = $5
RETURNING *;

-- name: RevertAttachmentContent :one
-- Rollback content metadata after a storage write failure (CAR-797).
-- Only succeeds while the row's updated_at still equals the value set by the
-- failed write — a concurrent save would have bumped updated_at again, and
-- reverting over it would lose that valid write.
UPDATE attachment
SET size_bytes = $3, updated_at = $4
WHERE id = $1 AND workspace_id = $2 AND updated_at = $5
RETURNING *;

-- name: DeleteAttachment :exec
DELETE FROM attachment WHERE id = $1 AND workspace_id = $2;
