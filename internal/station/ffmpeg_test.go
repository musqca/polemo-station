package station

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFFmpegPathUsesExplicitOverride(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(binary, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, "", t.TempDir())
	service.SetFFmpegPath(binary)
	path, err := service.FFmpegPath()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	if path != want {
		t.Fatalf("got %q, want %q", path, want)
	}
}

func TestFFmpegPathRejectsNonExecutableOverride(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(binary, []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, "", t.TempDir())
	service.SetFFmpegPath(binary)
	if _, err := service.FFmpegPath(); err == nil {
		t.Fatal("expected a non-executable override to be rejected")
	}
}
