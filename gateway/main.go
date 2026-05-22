package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
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
