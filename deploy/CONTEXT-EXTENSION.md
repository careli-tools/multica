# Careli Plugin Context Extension (CA-431)

Maintenance base: upstream v0.7.0 (`dbdbb299164347586e2650bb67684a490726a798`).
Merge target: `careli/v0.7.0`. `careli/v0.6.0` remains the rollback source; the older fork main is not the running production source.

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

Build production images only after merge, with build artifacts from the identical merged source tree. A pre-merge verification build may be reused only after verifying tree identity. The first image set on this base is `0.7.0-careli.1`. Build the backend with Go 1.26.9 or newer and CGO_ENABLED=0: `go build -ldflags '-s -w -X main.version=0.7.0-careli.1 -X main.commit=<merged SHA>' -o bin/server ./cmd/server` and `go build -ldflags '-s -w' -o bin/migrate ./cmd/migrate`. Build web with Node22/pnpm10.28.2, `STANDALONE=true NEXT_PUBLIC_APP_VERSION=0.7.0-careli.1 pnpm --filter @multica/web build`.

Then use deploy/Dockerfile.context-backend and deploy/Dockerfile.context-web with --build-arg COMMIT=<merged SHA>. The backend keeps the official v0.7.0 image entrypoint and CLI and replaces `/app/server`, `/app/migrate` and `/app/migrations/` from the same merged source tree, so API, migration runner (it carries the Careli index cleanup registrations) and schema stay aligned. Web uses Debian libc, matching the host-built native Node modules. Dockerfile-specific ignores include only runtime artifacts, the backend server/migrate/migrations, licenses and notices.

Migrations: the Careli migrations 564–566 (`564_notification_delivery`, `565_notification_delivery_claim_index`, `566_notification_daily_report_due_index`) share numeric prefixes with upstream 564–566. The runner keys on the full file stem, so both sets apply; production already records the Careli stems, so they must never be renamed. New Careli migrations must use a number above the newest upstream migration of the base release and be rechecked on each port.

Compose: set `MULTICA_IMAGE_TAG=v0.7.0`, keep `MULTICA_BACKEND_IMAGE=careli/multica-backend-context` and `MULTICA_WEB_IMAGE=careli/multica-web-context`. Tag the build `v0.7.0-careli.N` and the Compose alias `v0.7.0`; keep the `v0.6.0` aliases as rollback. Keep `MULTICA_SERVER_PINNED=1`, enforced by the dashboard worker before any server update. Future upstream updates require porting and checking this Careli extension first.

Cutover: take a PostgreSQL backup and record previous image IDs, then recreate backend/frontend only via `/usr/local/bin/multica-compose up -d --no-deps backend frontend`. Do not restart PostgreSQL. Verify the upstream migrations through 592, `/health`, frontend readiness, plugin panels and notification delivery.

Rollback: do not rely on down migrations for the upstream v0.7.0 schema changes. Restore the pre-cutover dump into the stopped stack, set `MULTICA_IMAGE_TAG=v0.6.0` (aliases built from `careli/v0.6.0`), and recreate backend/frontend. Do not use `migrate down`, which would reverse every migration in this repository.

## Test environment caveat

On this VM, upstream scripts/ensure-postgres.sh defaults to Compose project `multica`, also used by production. Always supply COMPOSE_PROJECT_NAME=multica-context-test and COMPOSE_FILE=/tmp/multica-context-postgres.yml for managed local commands, and verify dedicated PostgreSQL readiness on 15439 first. Do not use the production env file. The context extension itself does not modify developer environment scripts.
