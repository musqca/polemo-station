package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrTranscodeBusy = errors.New("all media transcode slots are busy")

// TranscodeProfile is deliberately server-owned. Clients send only the short
// profile ID, so FFmpeg arguments cannot be injected or expanded arbitrarily.
type TranscodeProfile struct {
	ID         string `json:"id" mapstructure:"id"`
	Codec      string `json:"codec" mapstructure:"codec"`
	Container  string `json:"container" mapstructure:"container"`
	Bitrate    int    `json:"bitrate" mapstructure:"bitrate"`
	SampleRate int    `json:"sampleRate" mapstructure:"sampleRate"`
	Channels   int    `json:"channels" mapstructure:"channels"`
	Version    int    `json:"version" mapstructure:"version"`
}

func defaultTranscodeProfiles() map[string]TranscodeProfile {
	return map[string]TranscodeProfile{
		"balanced": {
			ID: "balanced", Codec: "aac", Container: "mp4", Bitrate: 256000,
			SampleRate: 48000, Channels: 2, Version: 1,
		},
		"mobile": {
			ID: "mobile", Codec: "aac", Container: "mp4", Bitrate: 192000,
			SampleRate: 48000, Channels: 2, Version: 1,
		},
	}
}

func validateTranscodeProfile(profile TranscodeProfile) error {
	if profile.ID == "" || profile.Codec != "aac" || profile.Container != "mp4" ||
		profile.Bitrate < 32000 ||
		profile.Bitrate > 512000 || profile.SampleRate < 8000 ||
		profile.SampleRate > 192000 || profile.Channels < 1 || profile.Channels > 2 ||
		profile.Version < 1 {
		return fmt.Errorf("invalid transcode profile: %s", profile.ID)
	}
	return nil
}

type transcodeMetadata struct {
	ServerFileID  string `json:"serverFileId"`
	SourceHash    string `json:"sourceHash"`
	SourceVersion int64  `json:"sourceVersion"`
	SourceSize    int64  `json:"sourceSize"`
	ProfileHash   string `json:"profileHash"`
}

// WebPlaybackDecision describes a source the browser can put directly into an
// HTMLAudioElement. Direct sources preserve HTTP Range support; transcoded
// sources are deliberately separate so HLS can replace them later without a
// client API change.
type WebPlaybackDecision struct {
	Mode     string `json:"mode"`
	URL      string `json:"url"`
	Seekable bool   `json:"seekable"`
	MIME     string `json:"mime"`
}

func (s *Service) ResolveWebPlayback(serverFileID string) (WebPlaybackDecision, error) {
	file, err := s.GetServerFile(serverFileID)
	if err != nil {
		return WebPlaybackDecision{}, err
	}
	format := strings.ToLower(strings.TrimSpace(file.Format))
	if isWebDirectFormat(format) {
		return WebPlaybackDecision{
			Mode: "direct", URL: "/api/station/files/" + file.ServerFileID + "/stream", Seekable: true, MIME: webMIME(format),
		}, nil
	}
	if _, err := s.FFmpegPath(); err != nil {
		return WebPlaybackDecision{}, err
	}
	return WebPlaybackDecision{
		Mode: "transcode", URL: "/api/station/files/" + file.ServerFileID + "/transcode/mobile", Seekable: true, MIME: "audio/mp4",
	}, nil
}

func isWebDirectFormat(format string) bool {
	switch format {
	case "mp3", "aac", "m4a", "mp4", "wav", "ogg", "opus", "flac":
		return true
	default:
		return false
	}
}

func webMIME(format string) string {
	switch format {
	case "mp3":
		return "audio/mpeg"
	case "aac":
		return "audio/aac"
	case "m4a", "mp4":
		return "audio/mp4"
	case "ogg":
		return "audio/ogg"
	case "opus":
		return "audio/ogg; codecs=opus"
	case "flac":
		return "audio/flac"
	case "wav":
		return "audio/wav"
	default:
		return "application/octet-stream"
	}
}

// WebMIMEForFormat exposes the browser MIME mapping to the HTTP layer.
func WebMIMEForFormat(format string) string {
	return webMIME(strings.ToLower(strings.TrimSpace(format)))
}

// OpenWebTranscode returns a cached AAC/M4A rendition for a server-owned
// profile. Each source file owns a directory and each profile gets a stable
// parameter hash, so multiple bitrates can coexist safely.
func (s *Service) OpenWebTranscode(ctx context.Context, serverFileID, profileID string) (*os.File, error) {
	file, err := s.GetServerFile(serverFileID)
	if err != nil {
		return nil, err
	}
	profile, err := s.transcodeProfile(profileID)
	if err != nil {
		return nil, err
	}
	profileHash := transcodeProfileHash(profile)
	cacheDir := filepath.Join(s.storageRoot, "transcodes", file.ServerFileID)
	path := filepath.Join(cacheDir, profileHash+".m4a")
	metadataPath := filepath.Join(cacheDir, profileHash+".json")
	if cached, ok := openValidTranscode(path, metadataPath, file, profileHash); ok {
		return cached, nil
	}

	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, err
	}
	lock := s.transferLock("transcode:" + file.ServerFileID + ":" + profileHash)
	lock.Lock()
	defer lock.Unlock()
	if cached, ok := openValidTranscode(path, metadataPath, file, profileHash); ok {
		return cached, nil
	}
	if err := s.createWebTranscode(ctx, serverFileID, path, file, profile, profileHash, metadataPath); err != nil {
		return nil, err
	}
	return os.Open(path)
}

func transcodeProfileHash(profile TranscodeProfile) string {
	value := struct {
		Codec      string `json:"codec"`
		Container  string `json:"container"`
		Bitrate    int    `json:"bitrate"`
		SampleRate int    `json:"sampleRate"`
		Channels   int    `json:"channels"`
		Version    int    `json:"version"`
	}{
		Codec: profile.Codec, Container: profile.Container, Bitrate: profile.Bitrate,
		SampleRate: profile.SampleRate, Channels: profile.Channels,
		Version: profile.Version,
	}
	payload, _ := json.Marshal(value)
	sum := sha256.Sum256(append([]byte("polemo-transcode-profile-v1:"), payload...))
	return hex.EncodeToString(sum[:])
}

func openValidTranscode(path, metadataPath string, file ServerFileView, profileHash string) (*os.File, bool) {
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, false
	}
	var metadata transcodeMetadata
	if json.Unmarshal(metadataBytes, &metadata) != nil ||
		metadata.ServerFileID != file.ServerFileID ||
		metadata.SourceHash != file.Hash ||
		metadata.SourceVersion != file.Version ||
		metadata.SourceSize != file.Size ||
		metadata.ProfileHash != profileHash {
		return nil, false
	}
	cached, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	return cached, true
}

// createWebTranscode converts once, into a temporary file, then publishes it
// atomically. Arguments are never constructed through a shell, and the source
// path only comes from a file that OpenAudio has already authorized and opened.
func (s *Service) createWebTranscode(ctx context.Context, serverFileID, finalPath string, file ServerFileView, profile TranscodeProfile, profileHash, metadataPath string) error {
	select {
	case s.transcodeSlots <- struct{}{}:
		defer func() { <-s.transcodeSlots }()
	default:
		return ErrTranscodeBusy
	}

	input, err := s.OpenAudio(serverFileID)
	if err != nil {
		return err
	}
	path := input.Name()
	if err := input.Close(); err != nil {
		return err
	}
	ffmpegPath, err := s.FFmpegPath()
	if err != nil {
		return err
	}

	temporary, err := os.CreateTemp(filepath.Dir(finalPath), ".transcode-*.m4a")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	defer os.Remove(temporaryPath)

	var stderr strings.Builder
	command := exec.CommandContext(ctx, ffmpegPath,
		"-nostdin", "-v", "error", "-i", path,
		"-map", "0:a:0", "-vn", "-c:a", profile.Codec,
		"-b:a", fmt.Sprintf("%dk", profile.Bitrate/1000),
		"-ar", fmt.Sprintf("%d", profile.SampleRate),
		"-ac", fmt.Sprintf("%d", profile.Channels),
		"-movflags", "+faststart", "-f", profile.Container, "-y", temporaryPath,
	)
	command.Stderr = &stderr
	if err := command.Run(); err != nil && ctx.Err() == nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("ffmpeg transcode failed: %s", message)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return err
	}
	metadata := transcodeMetadata{
		ServerFileID: file.ServerFileID, SourceHash: file.Hash,
		SourceVersion: file.Version, SourceSize: file.Size,
		ProfileHash: profileHash,
	}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	metadataTemp, err := os.CreateTemp(filepath.Dir(metadataPath), ".transcode-*.json")
	if err != nil {
		return err
	}
	metadataTempPath := metadataTemp.Name()
	if _, err := metadataTemp.Write(metadataBytes); err != nil {
		_ = metadataTemp.Close()
		_ = os.Remove(metadataTempPath)
		return err
	}
	if err := metadataTemp.Close(); err != nil {
		_ = os.Remove(metadataTempPath)
		return err
	}
	defer os.Remove(metadataTempPath)
	return os.Rename(metadataTempPath, metadataPath)
}
