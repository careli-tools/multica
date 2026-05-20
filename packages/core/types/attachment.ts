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
  // Bumped on every content write; doubles as the ETag / If-Match validator
  // for in-place Excalidraw saves (CAR-794). Optional: older backends that
  // predate the column do not send it.
  updated_at?: string;
}
