# Careli Plugin Context Extension (CA-431)

Maintenance base: upstream v0.6.0 (`ea94c7cd5bbce9c8e1f28c5fa049c47ee7651d02`).
Merge target: `careli/v0.6.0`. No database migration. The older fork main is not the running production source.

Adds project_panel and workspace_panel to shared web/desktop views, a project mount bound by the host bridge and signed hook, and scoped context reads/writes. Project callbacks cannot access other projects or workspace context. New write endpoints accept human members only; workspace writes additionally require owner/admin. Atomic expected_content comparison returns 409 on stale input. Successful writes publish the normal project/workspace event with via_plugin_id.

API: GET/PATCH `/v1/projects/{project_id}/context`, GET/PATCH `/v1/workspace/context`, GET `/v1/projects/{project_id}/issues` (newest 200 choices). Context writes require content and expected_content, each at most 100000 bytes. New scopes: projects:read/write, workspace:read/write; issue choices use issues:read. Context bootstrap optionally returns project id/title.

## Build and deployment

Only build production artifacts after merge. Build the backend with Go 1.26.6 and CGO_ENABLED=0 (`go build -ldflags '-X main.version=0.6.0-careli.1 -X main.commit=<merged SHA>' -o bin/server ./cmd/server`). Build web with Node22/pnpm10.28.2, `STANDALONE=true NEXT_PUBLIC_APP_VERSION=0.6.0-careli.1 pnpm --filter @multica/web build`.

Then use deploy/Dockerfile.context-backend and deploy/Dockerfile.context-web with --build-arg COMMIT=<merged SHA>. The backend keeps the official image entrypoint, migrations and CLI; only /app/server changes. Web uses Debian libc, matching the host-built native Node modules. Dockerfile-specific ignores include only runtime artifacts, licenses and notices.

Keep MULTICA_IMAGE_TAG=v0.6.0; set MULTICA_BACKEND_IMAGE=careli/multica-backend-context and MULTICA_WEB_IMAGE=careli/multica-web-context (both local images tagged v0.6.0). Set MULTICA_SERVER_PINNED=1, enforced by the dashboard worker before any server update. Future upstream updates require porting and testing this small extension first.

Take a PostgreSQL backup and record previous image IDs before recreating backend/frontend only via `/usr/local/bin/multica-compose up -d --no-deps backend frontend`. Do not restart PostgreSQL for this feature. Upgrade Wissen to 0.2.0 only after both host containers are healthy. Rollback: restore Wissen 0.1.1 first, remove custom image variables/pin from deployment env, then recreate backend/frontend with official v0.6.0. Stored managed context remains ordinary Markdown.

## Test environment caveat

On this VM, upstream scripts/ensure-postgres.sh defaults to Compose project `multica`, also used by production. Always supply COMPOSE_PROJECT_NAME=multica-context-test and COMPOSE_FILE=/tmp/multica-context-postgres.yml for managed local commands, and verify dedicated PostgreSQL readiness on 15439 first. Do not use the production env file. The context extension itself does not modify developer environment scripts.
