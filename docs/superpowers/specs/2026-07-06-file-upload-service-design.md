# File Upload & Public Serving Service — Design

## Purpose

The gateway needs to send images and HTML reports to users via the LINE Messaging API. LINE requires publicly reachable HTTPS URLs for image messages (`originalContentUrl`/`previewImageUrl`) and for any linked HTML report (opened by the end user in an external browser). This adds upload and public-serving endpoints to the existing Fiber gateway so callers can turn a local file into a public URL usable in a LINE message.

This is a POC: local disk storage, no auto-cleanup, minimal test coverage on pure logic only.

## Architecture

Two new routes are added to the existing gateway service (`gateway/main.go`), backed by local disk storage mounted as a Docker volume — following the same pattern as the existing `ollama` volume. No new services or external dependencies (no S3/MinIO). Handler logic lives in a new file `gateway/files.go` to keep `main.go` focused.

The gateway already has a public domain/HTTPS termination in front of it (reverse proxy), so no changes are needed there — only the app-level routes and Docker volume/env wiring.

## Components

### `POST /upload`

- **Auth**: protected by the existing Basic Auth middleware (registered after `app.Use(basicauth...)`, same as `/generate`).
- **Request**: `multipart/form-data` with a single `file` field.
- **Validation**:
  - Extension must be one of: `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.html` (case-insensitive). Anything else → 400.
  - File size must not exceed `MAX_UPLOAD_SIZE_MB` (env, default `10`, matching LINE's 10MB image message cap). Oversized → 400.
- **Storage**: generates a random filename as `<uuid>.<ext>` using `github.com/google/uuid` (already a transitive dependency in `go.mod`, promoted to direct). Never trusts the client-supplied filename for storage. Saves under `UPLOAD_DIR` (env, default `/app/uploads`).
- **Response** (200):
  ```json
  {
    "success": true,
    "url": "https://<PUBLIC_BASE_URL>/files/<uuid>.<ext>",
    "filename": "<uuid>.<ext>",
    "size_bytes": 12345,
    "content_type": "image/jpeg"
  }
  ```
  `url` is built by joining `PUBLIC_BASE_URL` (env, required — the gateway's externally reachable base URL, e.g. `https://gw.example.com`) with `/files/<filename>`.

### `GET /files/:filename`

- **Auth**: **none** — registered before the Basic Auth middleware (same position as `/health`), since LINE's servers and end-user browsers fetch this with no credentials.
- **Validation**: `filename` must match `^[a-zA-Z0-9_-]+\.(jpg|jpeg|png|gif|webp|html)$` exactly. Anything else (including path-traversal attempts like `../`) → 400, rejected before any filesystem access.
- **Behavior**: streams the file from `UPLOAD_DIR` via Fiber's `c.SendFile`, which sets `Content-Type` from the extension automatically. Missing file → 404.

## Data Flow

1. Caller sends `POST /upload` with Basic Auth credentials and the file (image or HTML report).
2. Gateway validates extension/size, stores the file under a UUID-derived name, and returns a public URL.
3. Caller uses that URL as `originalContentUrl`/`previewImageUrl` in a LINE image message, or embeds the HTML report link in a text/Flex message.
4. LINE's servers (for images) or the end user's browser (for the HTML report) issue a plain `GET` against `/files/<filename>` — no auth headers needed or accepted.

## Error Handling

| Condition | Status | Notes |
|---|---|---|
| No `file` field / malformed multipart | 400 | |
| Disallowed extension | 400 | Message lists allowed extensions |
| File exceeds `MAX_UPLOAD_SIZE_MB` | 400 | |
| Invalid/unsafe filename on GET | 400 | Checked via regex before filesystem access |
| File not found on GET | 404 | |
| Write failure (disk full, permissions) | 500 | |

## Deployment Changes

- `docker-compose.yaml`: add an `uploads` named volume mounted at `/app/uploads` on the `gateway` service; add env vars `UPLOAD_DIR` (default `/app/uploads`), `PUBLIC_BASE_URL` (required, no default), `MAX_UPLOAD_SIZE_MB` (default `10`).
- `gateway/go.mod`: promote `github.com/google/uuid` from indirect to direct (already present transitively).
- No Dockerfile changes needed — `/app/uploads` is created at runtime by the handler if missing.

## Out of Scope (explicitly deferred)

- Auto-cleanup / TTL on uploaded files — deleted manually if ever needed.
- S3-compatible storage — local disk only for this POC.
- Rate limiting / per-caller quotas on `/upload`.
- Signed/expiring URLs on `/files/:filename` — plain public access, matching LINE's fetch model.

## Testing

Given the POC scope, tests are limited to pure logic with `go test` (table-driven, per repo convention):

- Extension whitelist validation (accepts allowed, case variants; rejects everything else).
- Filename-safety regex used by `GET /files/:filename` (accepts valid UUID-based names; rejects path traversal, unexpected extensions, empty/garbage input).

No integration/e2e test harness is added for the multipart upload flow itself in this pass.
