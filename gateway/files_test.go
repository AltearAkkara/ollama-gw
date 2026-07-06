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
