-- Optimistic-locking support for in-place Excalidraw attachment saves
-- (CAR-794). `updated_at` is the entity validator behind the ETag/If-Match
-- lost-update guard on PUT /api/attachments/{id}.
--
-- NOT NULL DEFAULT now() backfills every existing row at ALTER time, so
-- there is no nullable window: the guard applies uniformly to the entire
-- existing attachment set, not just rows created after this migration.
-- This is why the update query needs no `updated_at IS NULL` escape hatch.
ALTER TABLE attachment
  ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
