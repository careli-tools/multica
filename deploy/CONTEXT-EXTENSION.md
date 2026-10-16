# Careli Plugin Context Extension (CA-431)

Maintenance base: upstream v0.6.0 (`ea94c7cd5bbce9c8e1f28c5fa049c47ee7651d02`).
Merge target: `careli/v0.6.0`. The older fork main is not the running production source.

Adds project_panel and workspace_panel to shared web/desktop views, a project mount bound by the host bridge and signed hook, and scoped context reads/writes. Project callbacks cannot access other projects or workspace context. New write endpoints accept human members only; workspace writes additionally require owner/admin. Atomic expected_content comparison returns 409 on stale input. Successful writes publish the normal project/workspace event with via_plugin_id.

API: GET/PATCH `/v1/projects/{project_id}/context`, GET/PATCH `/v1/workspace/context`, GET `/v1/projects/{project_id}/issues` (newest 200 choices). Context writes require content and expected_content, each at most 100000 bytes. New scopes: projects:read/write, workspace:read/write; issue choices use issues:read. Context bootstrap optionally returns project id/title.

Notification delivery is now core-owned as well: migrations 564–566 add the
Inbox outbox and daily-report run history, import existing `de.careli.notify`
preferences from plugin storage, and seed the last successful legacy report
from its marked Issue comment. New Inbox items create per-channel delivery rows
in the same transaction. Settings live in the native Notifications tab; email
digests group all due workspaces by member/address/window, while Telegram uses
the existing workspace binding and channel registry. Daily reports support
manual creation plus the saved schedule, two report styles, custom agent
instructions, and the previous report as first context. The Notify hook server
stays available with `LEGACY_NOTIFY_DELIVERY=false` so old package versions in
workspaces outside the deployment API receive a successful no-op until updated.

## Build and deployment

Build production images only after merge, with build artifacts from the identical merged source tree. A pre-merge verification build may be reused only after verifying tree identity. For the next Careli image set use `0.6.0-careli.4`. Build the backend with Go 1.26.6 and CGO_ENABLED=0 (`go build -ldflags '-X main.version=0.6.0-careli.4 -X main.commit=<merged SHA>' -o bin/server ./cmd/server`). Build web with Node22/pnpm10.28.2, `STANDALONE=true NEXT_PUBLIC_APP_VERSION=0.6.0-careli.4 pnpm --filter @multica/web build`.

Then use deploy/Dockerfile.context-backend and deploy/Dockerfile.context-web with --build-arg COMMIT=<merged SHA>. The backend keeps the official image entrypoint and CLI; copy both the server binary and the complete migrations directory from the same merged source tree so API and schema versions stay aligned. Web uses Debian libc, matching the host-built native Node modules. Dockerfile-specific ignores include only runtime artifacts, the backend server/migrations, licenses and notices.

Keep `MULTICA_IMAGE_TAG=v0.6.0`; set `MULTICA_BACKEND_IMAGE=careli/multica-backend-context` and `MULTICA_WEB_IMAGE=careli/multica-web-context` (the aliases used by Compose stay tagged `v0.6.0`; retain the prior image tags and tag the new build `.4` before retagging the aliases). The backend Dockerfile must copy `server/bin/server` to `/app/server` and `server/migrations/` to `/app/migrations/` from that merged source tree. Keep `MULTICA_SERVER_PINNED=1`, enforced by the dashboard worker before any server update. Future upstream updates require porting and checking this Careli extension first.

Before cutover, confirm Notify reports `queued=0` and `reports_pending=0`, back up its SQLite state, and set `LEGACY_NOTIFY_DELIVERY=false` while leaving the hook service active. Take a PostgreSQL backup and record previous image IDs before recreating backend/frontend only via `/usr/local/bin/multica-compose up -d --no-deps backend frontend`. Do not restart PostgreSQL for this feature. Verify migrations 564–566, `/health`, frontend readiness, and the scheduler before installing Notify bundle 0.4.0 in the API-visible workspaces. The inbox/outbox is in the global server database, so missing plugin-package readback for other workspaces no longer blocks delivery.

Rollback: retag the archived `v0.6.0-careli.2` backend/web images as the Compose `v0.6.0` aliases, run `deploy/rollback-notification-delivery.sql` with `psql` in autocommit mode, set `LEGACY_NOTIFY_DELIVERY=true`, and restore the prior 0.3.2 bundle in the workspaces that were upgraded. The five API-invisible workspaces remain on 0.3.2 and continue through the restored hook server. Existing plugin preferences remain in both stores; report issues/comments remain available to seed a later upgrade. Recreate backend/frontend only; do not restart PostgreSQL. Do not use `migrate down`, which would reverse every migration in this repository.

## Test environment caveat

On this VM, upstream scripts/ensure-postgres.sh defaults to Compose project `multica`, also used by production. Always supply COMPOSE_PROJECT_NAME=multica-context-test and COMPOSE_FILE=/tmp/multica-context-postgres.yml for managed local commands, and verify dedicated PostgreSQL readiness on 15439 first. Do not use the production env file. The context extension itself does not modify developer environment scripts.
