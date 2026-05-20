export interface Attachment {
  id: string;
  workspace_id: string;
  issue_id: string | null;
  comment_id: string | null;
  chat_session_id: string | null;
  chat_message_id: string | null;
  uploader_type: string;
  uploader_id: string;
  filename: string;
  url: string;
  download_url: string;
  content_type: string;
  size_bytes: number;
  created_at: string;
  // Optimistic-locking token for in-place edits (CAR-794). The Excalidraw
  // save path echoes this back as an If-Match header so a concurrent write
  // by another tab is rejected instead of silently clobbering. Optional: an
  // older backend that predates the column omits it, and a desktop build
  // always outlives the server it talks to — a missing value simply means
  // "no known version", and the PUT degrades to last-write-wins.
  updated_at?: string;
}
