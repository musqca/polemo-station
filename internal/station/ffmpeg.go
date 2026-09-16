package station

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// FFmpegPath resolves an explicit configured executable, then PATH. FFmpeg is
// optional: Station can direct-play browser-compatible files without it.
func (s *Service) FFmpegPath() (string, error) {
	if s.ffmpegPath != "" {
		return executablePath(s.ffmpegPath)
	}
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return executablePath(path)
	}
	return "", fmt.Errorf("ffmpeg is unavailable: install FFmpeg and add it to PATH, or set stationFFmpegPath")
}

func executablePath(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("%s is not an executable file", path)
	}
	return filepath.Abs(path)
}
