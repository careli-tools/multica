-- CAR-794: Lost-Update-Protection for PUT /api/attachments/{id}.
--
-- Excalidraw attachments are edited collaboratively — two tabs can PUT the
-- same scene concurrently. Without a version column the second writer
-- silently clobbers the first (last-write-wins), which is exactly the
-- failure this column exists to detect.
--
-- `updated_at` is the optimistic-locking token: UpdateAttachmentContent
-- bumps it on every in-place write and the client echoes its last-seen
-- value back as an If-Match header so a stale write can be rejected.
--
-- The column is NOT NULL from the start so the protection covers the
-- *entire* current attachment bestand — a nullable column would leave every
-- pre-migration row permanently unguarded. ADD COLUMN ... NOT NULL DEFAULT
-- backfills every existing row to now() in a single statement; the UPDATE
-- below then corrects those rows to their truthful "last modified" instant,
-- which for a never-edited attachment is its creation time.

ALTER TABLE attachment
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE attachment SET updated_at = created_at;
