package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
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
