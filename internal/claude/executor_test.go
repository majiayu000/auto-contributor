package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsBinaryFile(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  []byte
		mode     os.FileMode
		want     bool
	}{
		{
			name:     "go source file",
			filename: "main.go",
			content:  []byte("package main\n"),
			mode:     0644,
			want:     false,
		},
		{
			name:     "exe file by extension",
			filename: "program.exe",
			content:  []byte("MZ..."),
			mode:     0755,
			want:     true,
		},
		{
			name:     "dll file by extension",
			filename: "library.dll",
			content:  []byte("MZ..."),
			mode:     0644,
			want:     true,
		},
		{
			name:     "so file by extension",
			filename: "library.so",
			content:  []byte{0x7f, 'E', 'L', 'F'},
			mode:     0755,
			want:     true,
		},
		{
			name:     "pyc file by extension",
			filename: "module.pyc",
			content:  []byte{0x00, 0x00},
			mode:     0644,
			want:     true,
		},
		{
			name:     "zip file by extension",
			filename: "archive.zip",
			content:  []byte("PK..."),
			mode:     0644,
			want:     true,
		},
		{
			name:     "png image by extension",
			filename: "image.png",
			content:  []byte{0x89, 'P', 'N', 'G'},
			mode:     0644,
			want:     true,
		},
		{
			name:     "markdown file",
			filename: "README.md",
			content:  []byte("# Hello\n"),
			mode:     0644,
			want:     false,
		},
		{
			name:     "json file",
			filename: "config.json",
			content:  []byte(`{"key": "value"}`),
			mode:     0644,
			want:     false,
		},
	}

	tmpDir := t.TempDir()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := filepath.Join(tmpDir, tt.filename)
			if err := os.WriteFile(filePath, tt.content, tt.mode); err != nil {
				t.Fatalf("Failed to create test file: %v", err)
			}

			got := isBinaryFile(filePath)
			if got != tt.want {
				t.Errorf("isBinaryFile(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

func TestIsBinaryFileWithELFHeader(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file with ELF header but no extension
	elfContent := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}
	elfPath := filepath.Join(tmpDir, "mybinary")
	if err := os.WriteFile(elfPath, elfContent, 0755); err != nil {
		t.Fatalf("Failed to create ELF file: %v", err)
	}

	if !isBinaryFile(elfPath) {
		t.Error("isBinaryFile should detect ELF binaries without extension")
	}
}

func TestIsBinaryFileWithMachOHeader(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file with Mach-O header (64-bit)
	machoContent := []byte{0xfe, 0xed, 0xfa, 0xcf, 0x00, 0x00, 0x00, 0x00}
	machoPath := filepath.Join(tmpDir, "mybinary")
	if err := os.WriteFile(machoPath, machoContent, 0755); err != nil {
		t.Fatalf("Failed to create Mach-O file: %v", err)
	}

	if !isBinaryFile(machoPath) {
		t.Error("isBinaryFile should detect Mach-O binaries without extension")
	}
}

func TestIsBinaryFileWithUniversalHeader(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file with Mach-O universal header
	universalContent := []byte{0xca, 0xfe, 0xba, 0xbe, 0x00, 0x00, 0x00, 0x00}
	universalPath := filepath.Join(tmpDir, "mybinary")
	if err := os.WriteFile(universalPath, universalContent, 0755); err != nil {
		t.Fatalf("Failed to create universal file: %v", err)
	}

	if !isBinaryFile(universalPath) {
		t.Error("isBinaryFile should detect Mach-O universal binaries")
	}
}

func TestIsBinaryFileNonExecutable(t *testing.T) {
	tmpDir := t.TempDir()

	// Non-executable file without extension should not be detected as binary
	textContent := []byte("This is a text file without extension")
	textPath := filepath.Join(tmpDir, "textfile")
	if err := os.WriteFile(textPath, textContent, 0644); err != nil {
		t.Fatalf("Failed to create text file: %v", err)
	}

	if isBinaryFile(textPath) {
		t.Error("isBinaryFile should not detect non-executable text files as binary")
	}
}

func TestBinaryExtensions(t *testing.T) {
	// Verify all expected extensions are in the map
	expectedBinary := []string{
		".exe", ".bin", ".so", ".dylib", ".dll",
		".o", ".a", ".lib", ".obj",
		".pyc", ".pyo", ".class",
		".jar", ".war", ".ear",
		".zip", ".tar", ".gz", ".bz2", ".xz",
		".png", ".jpg", ".jpeg", ".gif", ".ico",
		".pdf", ".doc", ".docx",
		".wasm",
	}

	for _, ext := range expectedBinary {
		if !binaryExtensions[ext] {
			t.Errorf("Expected extension %q to be in binaryExtensions map", ext)
		}
	}
}

func TestValidationResultStruct(t *testing.T) {
	// Test that ValidationResult struct works as expected
	result := &ValidationResult{
		Passed:   true,
		Language: "go",
		Errors:   []string{},
		Warnings: []string{"gofmt: auto-fixed formatting"},
	}

	if !result.Passed {
		t.Error("ValidationResult.Passed should be true")
	}
	if result.Language != "go" {
		t.Errorf("ValidationResult.Language = %q, want %q", result.Language, "go")
	}
	if len(result.Errors) != 0 {
		t.Error("ValidationResult.Errors should be empty")
	}
	if len(result.Warnings) != 1 {
		t.Error("ValidationResult.Warnings should have 1 warning")
	}
}
