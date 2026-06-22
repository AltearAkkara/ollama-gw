package main

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

var imageDataURLPattern = regexp.MustCompile(`^data:image/[^;]+;base64,`)

type GenerateRequest struct {
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Images []string `json:"images,omitempty"`
	Stream *bool    `json:"stream,omitempty"`
}

type OllamaResponse struct {
	Model    string `json:"model"`
	Response string `json:"response"`
}

type GatewayResponse struct {
	Success    bool   `json:"success"`
	Response   string `json:"response"`
	Model      string `json:"model"`
	Timestamp  string `json:"timestamp"`
	DurationMS int64  `json:"duration_ms"`
}

type ErrorResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

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
		var req GenerateRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
				Success: false,
				Error:   "invalid request body: " + err.Error(),
			})
		}

		if req.Model == "" {
			req.Model = getEnv("DEFAULT_MODEL", "gemma3")
		}

		// Strip data URL prefix from images
		for i, img := range req.Images {
			req.Images[i] = imageDataURLPattern.ReplaceAllString(img, "")
			// Also strip any whitespace
			req.Images[i] = strings.TrimSpace(req.Images[i])
		}

		// Force non-streaming so we can buffer and transform
		falseVal := false
		req.Stream = &falseVal

		body, err := json.Marshal(req)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Success: false,
				Error:   "failed to marshal request",
			})
		}

		client := &http.Client{Timeout: 5 * time.Minute}
		start := time.Now()

		resp, err := client.Post(
			fmt.Sprintf("%s/api/generate", ollamaURL),
			"application/json",
			bytes.NewReader(body),
		)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{
				Success: false,
				Error:   "failed to reach ollama: " + err.Error(),
			})
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Success: false,
				Error:   "failed to read ollama response",
			})
		}

		if resp.StatusCode != http.StatusOK {
			return c.Status(fiber.StatusBadGateway).JSON(ErrorResponse{
				Success: false,
				Error:   fmt.Sprintf("ollama error (status %d): %s", resp.StatusCode, string(respBody)),
			})
		}

		var ollamaResp OllamaResponse
		if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Success: false,
				Error:   "failed to parse ollama response",
			})
		}

		return c.JSON(GatewayResponse{
			Success:    true,
			Response:   ollamaResp.Response,
			Model:      ollamaResp.Model,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			DurationMS: time.Since(start).Milliseconds(),
		})
	})

	app.Post("/generate-with-crop-preprocess", func(c *fiber.Ctx) error {
		var body struct {
			Image  string `json:"image"`
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
			Regex  string `json:"regex"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
				Success: false,
				Error:   "invalid request body: " + err.Error(),
			})
		}
		if body.Image == "" {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
				Success: false,
				Error:   "image field is required",
			})
		}
		if body.Prompt == "" {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
				Success: false,
				Error:   "prompt field is required",
			})
		}
		if body.Model == "" {
			body.Model = getEnv("DEFAULT_MODEL", "gemma3")
		}

		// Strip data URL prefix (e.g. "data:image/jpeg;base64,") if present
		b64 := imageDataURLPattern.ReplaceAllString(body.Image, "")
		b64 = strings.TrimSpace(b64)

		imgBytes, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
				Success: false,
				Error:   "invalid base64: " + err.Error(),
			})
		}

		// Write image to a temp file
		tmpImg, err := os.CreateTemp("", "crop_input_*.jpg")
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Success: false,
				Error:   "failed to create temp file: " + err.Error(),
			})
		}
		defer os.Remove(tmpImg.Name())

		if _, err := tmpImg.Write(imgBytes); err != nil {
			tmpImg.Close()
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Success: false,
				Error:   "failed to write temp file: " + err.Error(),
			})
		}
		tmpImg.Close()

		// Create a temp output dir
		tmpOutDir, err := os.MkdirTemp("", "crop_out_*")
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Success: false,
				Error:   "failed to create temp out_dir: " + err.Error(),
			})
		}
		defer os.RemoveAll(tmpOutDir)

		// CROP_MODEL_PATH defaults to "models/best_crop_egat_23.pt" (relative to CWD).
		// In Docker CWD=/app so this resolves correctly.
		// For local dev, set CROP_MODEL_PATH to an absolute path or run the binary from the project root.
		modelPath := getEnv("CROP_MODEL_PATH", "models/best_crop_egat_23.pt")

		start := time.Now()
		cmd := exec.CommandContext(c.Context(),
			"python3", "crop_counter.py",
			"--image", tmpImg.Name(),
			"--out_dir", tmpOutDir,
			"--model", modelPath,
			"--save_crops",
		)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		runErr := cmd.Run()

		result := fiber.Map{
			"success":     runErr == nil,
			"stdout":      stdout.String(),
			"stderr":      stderr.String(),
			"duration_ms": time.Since(start).Milliseconds(),
		}
		if runErr != nil {
			result["error"] = runErr.Error()
			return c.Status(fiber.StatusInternalServerError).JSON(result)
		}

		// Read crop files from tmpOutDir before defer deletes them
		entries, _ := os.ReadDir(tmpOutDir)
		crops := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(tmpOutDir, e.Name()))
			if err != nil {
				continue
			}
			crops = append(crops, base64.StdEncoding.EncodeToString(data))
		}

		// Fan-out: send each crop to LLM in parallel
		type cropLLMResult struct {
			Index    int    `json:"index"`
			Response string `json:"response"`
			Error    string `json:"error,omitempty"`
		}
		llmResults := make([]cropLLMResult, len(crops))
		var wg sync.WaitGroup
		falseVal := false
		for i, cropB64 := range crops {
			wg.Add(1)
			go func(idx int, img string) {
				defer wg.Done()
				reqPayload := GenerateRequest{
					Model:  body.Model,
					Prompt: body.Prompt,
					Images: []string{img},
					Stream: &falseVal,
				}
				reqBody, err := json.Marshal(reqPayload)
				if err != nil {
					llmResults[idx] = cropLLMResult{Index: idx, Error: "marshal: " + err.Error()}
					return
				}
				client := &http.Client{Timeout: 5 * time.Minute}
				resp, err := client.Post(
					fmt.Sprintf("%s/api/generate", ollamaURL),
					"application/json",
					bytes.NewReader(reqBody),
				)
				if err != nil {
					llmResults[idx] = cropLLMResult{Index: idx, Error: "ollama: " + err.Error()}
					return
				}
				defer resp.Body.Close()
				respBody, err := io.ReadAll(resp.Body)
				if err != nil {
					llmResults[idx] = cropLLMResult{Index: idx, Error: "read: " + err.Error()}
					return
				}
				var ollamaResp OllamaResponse
				if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
					llmResults[idx] = cropLLMResult{Index: idx, Error: "parse: " + err.Error()}
					return
				}
				llmResults[idx] = cropLLMResult{Index: idx, Response: ollamaResp.Response}
			}(i, cropB64)
		}
		wg.Wait()

		// Extract numbers inside [...] from each response, concat into one flat array
		bracketRe := regexp.MustCompile(`\[([^\]]+)\]`)
		numRe := regexp.MustCompile(`\d+`)

		var answers []int
		for _, r := range llmResults {
			if r.Error != "" {
				continue
			}
			for _, bracket := range bracketRe.FindAllStringSubmatch(r.Response, -1) {
				// bracket[1] is the content inside []
				for _, m := range numRe.FindAllString(bracket[1], -1) {
					var n int
					if _, err := fmt.Sscanf(m, "%d", &n); err == nil {
						answers = append(answers, n)
					}
				}
			}
		}
		if answers == nil {
			answers = []int{}
		}

		return c.JSON(fiber.Map{
			"success":     true,
			"duration_ms": time.Since(start).Milliseconds(),
			"answers":     answers,
		})
	})

	app.Post("/pull", func(c *fiber.Ctx) error {
		body := c.Body()

		// Forward request ไป Ollama /api/pull
		req, err := http.NewRequestWithContext(
			c.Context(),
			"POST",
			fmt.Sprintf("%s/api/pull", ollamaURL),
			bytes.NewReader(body),
		)
		if err != nil {
			return c.Status(500).JSON(ErrorResponse{Success: false, Error: err.Error()})
		}
		req.Header.Set("Content-Type", "application/json")

		// Pull ใช้เวลานาน — timeout เผื่อไว้
		client := &http.Client{Timeout: 30 * time.Minute}
		resp, err := client.Do(req)
		if err != nil {
			return c.Status(502).JSON(ErrorResponse{
				Success: false,
				Error:   "failed to reach ollama: " + err.Error(),
			})
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return c.Status(500).JSON(ErrorResponse{Success: false, Error: err.Error()})
		}

		return c.Status(resp.StatusCode).Send(respBody)
	})

	app.Delete("/delete", func(c *fiber.Ctx) error {
		body := c.Body()

		req, err := http.NewRequestWithContext(
			c.Context(),
			"DELETE",
			fmt.Sprintf("%s/api/delete", ollamaURL),
			bytes.NewReader(body),
		)
		if err != nil {
			return c.Status(500).JSON(ErrorResponse{Success: false, Error: err.Error()})
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return c.Status(502).JSON(ErrorResponse{
				Success: false,
				Error:   "failed to reach ollama: " + err.Error(),
			})
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return c.Status(500).JSON(ErrorResponse{Success: false, Error: err.Error()})
		}

		return c.Status(resp.StatusCode).Send(respBody)
	})

	app.Get("/tags", func(c *fiber.Ctx) error {
		resp, err := http.Get(fmt.Sprintf("%s/api/tags", ollamaURL))
		if err != nil {
			return c.Status(502).JSON(ErrorResponse{Success: false, Error: err.Error()})
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		return c.Status(resp.StatusCode).Send(body)
	})

	log.Printf("Gateway starting on :%s → Ollama at %s", port, ollamaURL)
	log.Fatal(app.Listen(":" + port))
}
