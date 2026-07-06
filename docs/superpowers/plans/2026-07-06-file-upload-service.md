# File Upload & Public Serving Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /upload` and `GET /files/:filename` to the existing Go/Fiber gateway so images and HTML reports can be turned into public HTTPS URLs usable in LINE Messaging API calls.

**Architecture:** Two new routes on the existing Fiber app (`gateway/main.go`), backed by local disk storage (a new Docker volume). Validation and handler logic live in a new file `gateway/files.go`. `/upload` sits behind the existing Basic Auth middleware; `/files/:filename` is registered before that middleware so it stays public, matching how `/health` is already public.

**Tech Stack:** Go 1.24, Fiber v2 (`github.com/gofiber/fiber/v2`), `github.com/google/uuid` (already a transitive dependency).

## Global Constraints

- Allowed upload extensions (case-insensitive): `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.html` — reject everything else.
- `MAX_UPLOAD_SIZE_MB` env var, default `10` (matches LINE's 10MB image message cap).
- `PUBLIC_BASE_URL` env var is **required** — no default; startup must fail fast (`log.Fatal`) if unset.
- `UPLOAD_DIR` env var, default `/app/uploads`; created at startup with `os.MkdirAll` if missing.
- Stored filenames are always `<uuid>.<lowercase-ext>` — never the client-supplied name.
- `GET /files/:filename` must validate against `^[a-zA-Z0-9_-]+\.(jpg|jpeg|png|gif|webp|html)$` before touching the filesystem.
- No auto-cleanup/TTL, no S3, no rate limiting, no signed URLs — explicitly out of scope per the approved spec (`docs/superpowers/specs/2026-07-06-file-upload-service-design.md`).
- Testing is scoped to pure logic only (extension whitelist, filename-safety regex) — no automated integration test harness for the multipart upload flow; verify that manually with `curl`.

---

### Task 1: Extension & Filename Validation Logic

**Files:**
- Create: `gateway/files.go`
- Test: `gateway/files_test.go`

**Interfaces:**
- Produces: `func contentTypeForExt(ext string) (string, bool)` — `ext` includes the leading dot (e.g. `.jpg`), case-insensitive; returns the MIME type and whether the extension is allowed.
- Produces: `func isSafeFilename(name string) bool` — true only if `name` matches `^[a-zA-Z0-9_-]+\.(jpg|jpeg|png|gif|webp|html)$` exactly.

- [ ] **Step 1: Write the failing tests**

Create `gateway/files_test.go`:

```go
package main

import "testing"

func TestContentTypeForExt(t *testing.T) {
	tests := []struct {
		name        string
		ext         string
		wantType    string
		wantAllowed bool
	}{
		{"lowercase jpg", ".jpg", "image/jpeg", true},
		{"lowercase jpeg", ".jpeg", "image/jpeg", true},
		{"uppercase JPG", ".JPG", "image/jpeg", true},
		{"png", ".png", "image/png", true},
		{"gif", ".gif", "image/gif", true},
		{"webp", ".webp", "image/webp", true},
		{"html", ".html", "text/html", true},
		{"uppercase HTML", ".HTML", "text/html", true},
		{"disallowed exe", ".exe", "", false},
		{"disallowed pdf", ".pdf", "", false},
		{"empty extension", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotAllowed := contentTypeForExt(tt.ext)
			if gotAllowed != tt.wantAllowed {
				t.Fatalf("contentTypeForExt(%q) allowed = %v, want %v", tt.ext, gotAllowed, tt.wantAllowed)
			}
			if gotAllowed && gotType != tt.wantType {
				t.Fatalf("contentTypeForExt(%q) type = %q, want %q", tt.ext, gotType, tt.wantType)
			}
		})
	}
}

func TestIsSafeFilename(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"valid uuid jpg", "550e8400-e29b-41d4-a716-446655440000.jpg", true},
		{"valid uuid html", "550e8400-e29b-41d4-a716-446655440000.html", true},
		{"valid with underscore", "abc_123-DEF.png", true},
		{"path traversal", "../../etc/passwd", false},
		{"embedded slash", "sub/dir.jpg", false},
		{"disallowed extension", "file.exe", false},
		{"no extension", "file", false},
		{"empty string", "", false},
		{"encoded traversal", "..%2F..%2Fetc%2Fpasswd.jpg", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSafeFilename(tt.in); got != tt.want {
				t.Fatalf("isSafeFilename(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd gateway && go test ./... -run 'TestContentTypeForExt|TestIsSafeFilename' -v`
Expected: build FAILS with `undefined: contentTypeForExt` and `undefined: isSafeFilename`.

- [ ] **Step 3: Write minimal implementation**

Create `gateway/files.go`:

```go
package main

import (
	"regexp"
	"strings"
)

var (
	allowedExtensions = map[string]string{
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".webp": "image/webp",
		".html": "text/html",
	}

	safeFilenamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+\.(jpg|jpeg|png|gif|webp|html)$`)
)

func contentTypeForExt(ext string) (string, bool) {
	contentType, ok := allowedExtensions[strings.ToLower(ext)]
	return contentType, ok
}

func isSafeFilename(name string) bool {
	return safeFilenamePattern.MatchString(name)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd gateway && go test ./... -run 'TestContentTypeForExt|TestIsSafeFilename' -v`
Expected: `PASS` for both test functions, all subtests green.

- [ ] **Step 5: Commit**

```bash
git add gateway/files.go gateway/files_test.go
git commit -m "feat: add upload extension and filename validation logic"
```

---

### Task 2: Upload & Serve Handlers

**Files:**
- Modify: `gateway/files.go` (append to the file created in Task 1)

**Interfaces:**
- Consumes: `contentTypeForExt(ext string) (string, bool)`, `isSafeFilename(name string) bool` from Task 1. Consumes the existing `ErrorResponse` struct already defined in `gateway/main.go:45-48` (`Success bool`, `Error string`).
- Produces: `func newUploadHandler(uploadDir, publicBaseURL string, maxUploadSizeBytes int64) fiber.Handler` — handler for `POST /upload`.
- Produces: `func newServeFileHandler(uploadDir string) fiber.Handler` — handler for `GET /files/:filename`.

No automated tests for this task per the approved spec (multipart/handler flow is verified manually with `curl` in Task 3, once routes are wired in `main.go`). This task only adds the handler functions; they aren't reachable until Task 3 registers the routes.

- [ ] **Step 1: Add handler code**

Replace the import block in `gateway/files.go` with:

```go
import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)
```

Then append this code to `gateway/files.go` (after the existing `isSafeFilename` function):

```go
type UploadResponse struct {
	Success     bool   `json:"success"`
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentType string `json:"content_type"`
}

func newUploadHandler(uploadDir, publicBaseURL string, maxUploadSizeBytes int64) fiber.Handler {
	return func(c *fiber.Ctx) error {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
				Success: false,
				Error:   "file field is required: " + err.Error(),
			})
		}

		ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
		contentType, ok := contentTypeForExt(ext)
		if !ok {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
				Success: false,
				Error:   "unsupported file type: allowed extensions are .jpg, .jpeg, .png, .gif, .webp, .html",
			})
		}

		if fileHeader.Size > maxUploadSizeBytes {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
				Success: false,
				Error:   fmt.Sprintf("file exceeds maximum size of %d bytes", maxUploadSizeBytes),
			})
		}

		filename := uuid.NewString() + ext
		destPath := filepath.Join(uploadDir, filename)

		if err := c.SaveFile(fileHeader, destPath); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Success: false,
				Error:   "failed to save file: " + err.Error(),
			})
		}

		url := strings.TrimRight(publicBaseURL, "/") + "/files/" + filename

		return c.JSON(UploadResponse{
			Success:     true,
			URL:         url,
			Filename:    filename,
			SizeBytes:   fileHeader.Size,
			ContentType: contentType,
		})
	}
}

func newServeFileHandler(uploadDir string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		filename := c.Params("filename")
		if !isSafeFilename(filename) {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
				Success: false,
				Error:   "invalid filename",
			})
		}

		fullPath := filepath.Join(uploadDir, filename)
		if _, err := os.Stat(fullPath); err != nil {
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{
				Success: false,
				Error:   "file not found",
			})
		}

		return c.SendFile(fullPath)
	}
}
```

- [ ] **Step 2: Compile-check (routes aren't wired yet, so just confirm the package builds)**

Run: `cd gateway && go build ./...`
Expected: builds successfully (handlers are unused-by-router but referenced by nothing yet, which is fine — Go only errors on unused local variables/imports, not unused top-level functions).

- [ ] **Step 3: Commit**

```bash
git add gateway/files.go
git commit -m "feat: add upload and serve-file handlers"
```

---

### Task 3: Wire Routes & Config in main.go

**Files:**
- Modify: `gateway/main.go:3-21` (imports)
- Modify: `gateway/main.go:57-80` (main() setup and route registration)

**Interfaces:**
- Consumes: `newUploadHandler(uploadDir, publicBaseURL string, maxUploadSizeBytes int64) fiber.Handler` and `newServeFileHandler(uploadDir string) fiber.Handler` from Task 2.

- [ ] **Step 1: Add `strconv` to the import block**

In `gateway/main.go`, the current import block (lines 3-21) is:

```go
import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/basicauth"
)
```

Change it to:

```go
import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/basicauth"
)
```

- [ ] **Step 2: Add env/config reads, mkdir, and route registration**

In `gateway/main.go`, the current `main()` opening (lines 57-80) is:

```go
func main() {
	ollamaURL := getEnv("OLLAMA_URL", "http://localhost:11434")
	port := getEnv("PORT", "8080")
	authUser := getEnv("BASIC_AUTH_USER", "admin")
	authPass := getEnv("BASIC_AUTH_PASS", "secret")

	app := fiber.New(fiber.Config{
		BodyLimit:    50 * 1024 * 1024, // 50MB
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  5 * time.Minute,
	})

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	app.Use(basicauth.New(basicauth.Config{
		Users: map[string]string{
			authUser: authPass,
		},
	}))

	app.Post("/generate", func(c *fiber.Ctx) error {
```

Change it to:

```go
func main() {
	ollamaURL := getEnv("OLLAMA_URL", "http://localhost:11434")
	port := getEnv("PORT", "8080")
	authUser := getEnv("BASIC_AUTH_USER", "admin")
	authPass := getEnv("BASIC_AUTH_PASS", "secret")

	uploadDir := getEnv("UPLOAD_DIR", "/app/uploads")
	publicBaseURL := getEnv("PUBLIC_BASE_URL", "")
	if publicBaseURL == "" {
		log.Fatal("PUBLIC_BASE_URL environment variable is required")
	}
	maxUploadSizeMB, err := strconv.ParseInt(getEnv("MAX_UPLOAD_SIZE_MB", "10"), 10, 64)
	if err != nil {
		log.Fatalf("invalid MAX_UPLOAD_SIZE_MB: %v", err)
	}
	maxUploadSizeBytes := maxUploadSizeMB * 1024 * 1024

	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		log.Fatalf("failed to create upload dir %s: %v", uploadDir, err)
	}

	app := fiber.New(fiber.Config{
		BodyLimit:    50 * 1024 * 1024, // 50MB
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  5 * time.Minute,
	})

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	app.Get("/files/:filename", newServeFileHandler(uploadDir))

	app.Use(basicauth.New(basicauth.Config{
		Users: map[string]string{
			authUser: authPass,
		},
	}))

	app.Post("/upload", newUploadHandler(uploadDir, publicBaseURL, maxUploadSizeBytes))

	app.Post("/generate", func(c *fiber.Ctx) error {
```

- [ ] **Step 3: Build**

Run: `cd gateway && go build ./...`
Expected: builds successfully with no errors.

- [ ] **Step 4: Manual verification — run the server locally**

Run:
```bash
cd gateway
UPLOAD_DIR=/tmp/gw-uploads PUBLIC_BASE_URL=http://localhost:8080 BASIC_AUTH_USER=admin BASIC_AUTH_PASS=secret PORT=8080 OLLAMA_URL=http://localhost:11434 go run .
```
Expected: log line `Gateway starting on :8080 → Ollama at http://localhost:11434` with no fatal errors (ignore Ollama connectivity — that's unrelated to this feature).

- [ ] **Step 5: Manual verification — upload and fetch a file**

In a second terminal, create a tiny test image and upload it:
```bash
printf '\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00\xff\xd9' > /tmp/test.jpg

curl -s -u admin:secret -F "file=@/tmp/test.jpg" http://localhost:8080/upload
```
Expected: JSON response like `{"success":true,"url":"http://localhost:8080/files/<uuid>.jpg","filename":"<uuid>.jpg","size_bytes":20,"content_type":"image/jpeg"}`.

Then fetch it publicly (no `-u` credentials):
```bash
curl -s -o /tmp/downloaded.jpg -w "%{http_code}\n" http://localhost:8080/files/<uuid>.jpg
```
Replace `<uuid>.jpg` with the filename from the previous response. Expected: `200` and `/tmp/downloaded.jpg` matches `/tmp/test.jpg` in size.

Also verify rejections:
```bash
# Disallowed extension
curl -s -u admin:secret -F "file=@/tmp/test.jpg;filename=test.exe" http://localhost:8080/upload
# Expected: 400 "unsupported file type..."

# Path traversal on fetch
curl -s -o /dev/null -w "%{http_code}\n" "http://localhost:8080/files/..%2F..%2Fetc%2Fpasswd"
# Expected: 400 (not 200, not a leaked file)

# Fetch without auth still works (public route)
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/files/<uuid>.jpg
# Expected: 200 even with no -u flag
```

Stop the server (Ctrl+C) once verified.

- [ ] **Step 6: Commit**

```bash
git add gateway/main.go
git commit -m "feat: wire upload and file-serving routes into gateway"
```

---

### Task 4: Dependency Cleanup & Docker Compose Wiring

**Files:**
- Modify: `gateway/go.mod`, `gateway/go.sum` (via `go mod tidy`)
- Modify: `docker-compose.yaml`

**Interfaces:**
- None — this task only touches dependency metadata and deployment config, no Go code changes.

- [ ] **Step 1: Promote `google/uuid` to a direct dependency**

Run:
```bash
cd gateway && go mod tidy
```
Expected: `gateway/go.mod` no longer marks `github.com/google/uuid` as `// indirect` (since `files.go` now imports it directly). Diff should be small — just the comment removal on that line (and possibly reordering).

- [ ] **Step 2: Verify build still passes after tidy**

Run: `cd gateway && go build ./... && go test ./...`
Expected: build succeeds, all tests still pass.

- [ ] **Step 3: Update docker-compose.yaml**

Current `docker-compose.yaml`:

```yaml
services:
  ollama-api:
    image: 'ollama/ollama:latest'
    restart: unless-stopped
    environment:
      - OLLAMA_NUM_PARALLEL=4
    volumes:
      - 'ollama:/root/.ollama'
    healthcheck:
      test: ["CMD", "ollama", "list"]
      interval: 10s
      timeout: 10s
      retries: 30
      start_period: 30s

  gateway:
    build:
      context: .
      dockerfile: Dockerfile
    restart: unless-stopped
    environment:
      - OLLAMA_URL=http://ollama-api:11434
      - PORT=8080
      - BASIC_AUTH_USER=${BASIC_AUTH_USER}
      - BASIC_AUTH_PASS=${BASIC_AUTH_PASS}
      - DEFAULT_MODEL=${DEFAULT_MODEL:-gemma3}
    depends_on:
      ollama-api:
        condition: service_healthy
    ports:
      - "8080:8080"

volumes:
  ollama:
```

Change it to:

```yaml
services:
  ollama-api:
    image: 'ollama/ollama:latest'
    restart: unless-stopped
    environment:
      - OLLAMA_NUM_PARALLEL=4
    volumes:
      - 'ollama:/root/.ollama'
    healthcheck:
      test: ["CMD", "ollama", "list"]
      interval: 10s
      timeout: 10s
      retries: 30
      start_period: 30s

  gateway:
    build:
      context: .
      dockerfile: Dockerfile
    restart: unless-stopped
    environment:
      - OLLAMA_URL=http://ollama-api:11434
      - PORT=8080
      - BASIC_AUTH_USER=${BASIC_AUTH_USER}
      - BASIC_AUTH_PASS=${BASIC_AUTH_PASS}
      - DEFAULT_MODEL=${DEFAULT_MODEL:-gemma3}
      - UPLOAD_DIR=/app/uploads
      - PUBLIC_BASE_URL=${PUBLIC_BASE_URL}
      - MAX_UPLOAD_SIZE_MB=${MAX_UPLOAD_SIZE_MB:-10}
    volumes:
      - 'uploads:/app/uploads'
    depends_on:
      ollama-api:
        condition: service_healthy
    ports:
      - "8080:8080"

volumes:
  ollama:
  uploads:
```

- [ ] **Step 4: Validate the compose file**

Run: `docker compose config`
Expected: renders without errors, `gateway.environment` includes `PUBLIC_BASE_URL` and `MAX_UPLOAD_SIZE_MB`, and `gateway.volumes` includes the `uploads` mount. (If `PUBLIC_BASE_URL` isn't set in your shell/`.env`, `docker compose config` will show it as empty — that's expected at this validation step; it must be set in the real `.env` before `docker compose up`.)

- [ ] **Step 5: Commit**

```bash
git add gateway/go.mod gateway/go.sum docker-compose.yaml
git commit -m "chore: wire uploads volume and promote uuid dependency"
```
