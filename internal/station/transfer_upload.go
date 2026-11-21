package station

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

const (
	// Larger chunks amortize the HTTP request/response round trip while
	// preserving resumability at chunk boundaries.
	transferChunkSize = 8 * 1024 * 1024
	maxUploadSize     = int64(4 * 1024 * 1024 * 1024)
)

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var serverFileIDPattern = regexp.MustCompile(`^sf1_[a-f0-9]{64}$`)

type UploadPrepareRequest struct {
	FileID       string `json:"fileId"`
	RelativePath string `json:"relativePath"`
	FileName     string `json:"fileName"`
	Size         int64  `json:"size"`
	Hash         string `json:"hash"`
	ServerFileID string `json:"serverFileId"`
	BaseVersion  int64  `json:"baseVersion"`
	BatchID      string `json:"batchId"`
}

type UploadPrepareResponse struct {
	TaskID           string `json:"taskId,omitempty"`
	ServerFileID     string `json:"serverFileId"`
	TransferredBytes int64  `json:"transferredBytes"`
	ChunkSize        int    `json:"chunkSize"`
	Version          int64  `json:"version"`
	AlreadyPresent   bool   `json:"alreadyPresent"`
}

func (s *Service) PrepareUpload(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var request UploadPrepareRequest
	if !decodeParams(a, &request) || !validUploadRequest(request) {
		a.SendCode(400, "invalid upload")
		return
	}
	response, err := s.prepareUpload(deviceID, request)
	if err != nil {
		a.SendCode(500, "upload preparation failed")
		return
	}
	a.Send(response)
}

type UploadRequestCommand struct {
	FileID          string `json:"fileId"`
	BatchID         string `json:"batchId,omitempty"`
	BatchTotalFiles int    `json:"batchTotalFiles,omitempty"`
	BatchTotalBytes int64  `json:"batchTotalBytes,omitempty"`
}

type DownloadRequestCommand struct {
	TaskID       string `json:"taskId"`
	BatchID      string `json:"batchId,omitempty"`
	FileID       string `json:"fileId"`
	RelativePath string `json:"relativePath"`
	FileName     string `json:"fileName"`
	Size         int64  `json:"size"`
	Hash         string `json:"hash"`
	ServerFileID string `json:"serverFileId"`
	Version      int64  `json:"version"`
}

func (s *Service) RequestDeviceUpload(deviceID, fileID string) error {
	value, ok := s.onlineDevices.Load(deviceID)
	if !ok {
		return errors.New("device is offline")
	}
	client, ok := value.(*ws.Client)
	if !ok || client == nil {
		return errors.New("device connection unavailable")
	}
	var track Track
	if err := s.db.Where("device_id = ? AND file_id = ?", deviceID, fileID).First(&track).Error; err != nil {
		return err
	}
	batchID := newID("bt")
	if err := s.createTransferBatch(batchID, deviceID, "upload", 1, track.Size); err != nil {
		return err
	}
	if _, err := s.createQueuedUploadTask(batchID, deviceID, track); err != nil {
		return err
	}
	client.SendActionMsg(&ws.Action{
		Action: "v1.transfer.upload.request",
		Data: UploadRequestCommand{
			FileID: track.FileID, BatchID: batchID, BatchTotalFiles: 1, BatchTotalBytes: track.Size,
		},
	})
	return nil
}

func (s *Service) RequestDeviceDownload(deviceID, fileID string) error {
	if _, err := s.downloadClient(deviceID); err != nil {
		return err
	}
	var track Track
	if err := s.db.Where("device_id = ? AND file_id = ?", deviceID, fileID).First(&track).Error; err != nil {
		return err
	}
	if track.ServerFileID == "" {
		return errors.New("device file is not linked to the server library")
	}
	var serverFile ServerFile
	if err := s.db.Where("server_file_id = ? AND deleted_at IS NULL", track.ServerFileID).First(&serverFile).Error; err != nil {
		return err
	}
	if track.AppliedVersion >= serverFile.Version && track.Hash == serverFile.Hash {
		return errors.New("device already has the latest version")
	}
	task := TransferTask{
		TaskID: newID("tr"), ServerFileID: serverFile.ServerFileID, DeviceID: deviceID,
		BatchID:   newID("bt"),
		Direction: "download", FileID: track.FileID, RelativePath: track.RelativePath,
		FileName: serverFile.FileName, ExpectedHash: serverFile.Hash, TotalBytes: serverFile.Size,
		BaseVersion: track.AppliedVersion, TargetVersion: serverFile.Version, State: "prepared",
	}
	if err := s.createTransferBatch(task.BatchID, deviceID, "download", 1, serverFile.Size); err != nil {
		return err
	}
	if err := s.db.Create(&task).Error; err != nil {
		return err
	}
	s.emitTransferChanged(task)
	return s.dispatchNextDownload(deviceID)
}

func (s *Service) dispatchDownload(task TransferTask) error {
	client, err := s.downloadClient(task.DeviceID)
	if err != nil {
		return err
	}
	client.SendActionMsg(&ws.Action{
		Action: "v1.transfer.download.request",
		Data: DownloadRequestCommand{
			TaskID: task.TaskID, BatchID: task.BatchID, FileID: task.FileID, RelativePath: task.RelativePath,
			FileName: task.FileName, Size: task.TotalBytes, Hash: task.ExpectedHash,
			ServerFileID: task.ServerFileID, Version: task.TargetVersion,
		},
	})
	return nil
}

func (s *Service) downloadClient(deviceID string) (*ws.Client, error) {
	value, ok := s.onlineDevices.Load(deviceID)
	if !ok {
		return nil, errors.New("device is offline")
	}
	client, ok := value.(*ws.Client)
	if !ok || client == nil {
		return nil, errors.New("device connection unavailable")
	}
	return client, nil
}

var errVersionConflict = errors.New("server version changed")

func validUploadRequest(request UploadPrepareRequest) bool {
	return strings.TrimSpace(request.BatchID) != "" &&
		strings.TrimSpace(request.FileID) != "" &&
		request.FileID == request.ServerFileID &&
		serverFileIDPattern.MatchString(strings.ToLower(request.ServerFileID)) &&
		strings.TrimSpace(request.FileName) != "" &&
		request.Size > 0 && request.Size <= maxUploadSize &&
		sha256Pattern.MatchString(strings.ToLower(request.Hash))
}

func (s *Service) prepareUpload(deviceID string, request UploadPrepareRequest) (UploadPrepareResponse, error) {
	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	request.Hash = strings.ToLower(request.Hash)

	var serverFile ServerFile
	err := s.db.Where("server_file_id = ? AND deleted_at IS NULL", request.ServerFileID).First(&serverFile).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return UploadPrepareResponse{}, err
	}
	if serverFile.ServerFileID != "" && serverFile.Hash == request.Hash && serverFile.DeletedAt == nil {
		if err := s.linkDeviceFile(deviceID, request, serverFile); err != nil {
			return UploadPrepareResponse{}, err
		}
		s.emitLibraryChanged(deviceID)
		var task TransferTask
		err := s.db.Where("batch_id = ? AND device_id = ? AND file_id = ? AND direction = ?", request.BatchID, deviceID, request.FileID, "upload").First(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			task = TransferTask{TaskID: newID("tr"), BatchID: request.BatchID, DeviceID: deviceID, Direction: "upload", FileID: request.FileID}
		} else if err != nil {
			return UploadPrepareResponse{}, err
		}
		task.ServerFileID = serverFile.ServerFileID
		task.RelativePath = cleanRelativePath(request.RelativePath)
		task.FileName = filepath.Base(request.FileName)
		task.ExpectedHash = request.Hash
		task.TotalBytes = request.Size
		task.TransferredBytes = request.Size
		task.TargetVersion = serverFile.Version
		task.State = "completed"
		if err := s.db.Save(&task).Error; err != nil {
			return UploadPrepareResponse{}, err
		}
		s.emitTransferChanged(task)
		return UploadPrepareResponse{
			TaskID:           task.TaskID,
			ServerFileID:     serverFile.ServerFileID,
			TransferredBytes: request.Size,
			ChunkSize:        transferChunkSize,
			Version:          1,
			AlreadyPresent:   true,
		}, nil
	}

	if serverFile.ServerFileID != "" && serverFile.Hash != request.Hash {
		return UploadPrepareResponse{}, errors.New("sampled server file id collision")
	}

	var task TransferTask
	if request.BatchID != "" {
		err = s.db.Where("batch_id = ? AND device_id = ? AND file_id = ? AND direction = ? AND state = ?", request.BatchID, deviceID, request.FileID, "upload", "queued").First(&task).Error
		if err == nil {
			task.ServerFileID = request.ServerFileID
			task.RelativePath = cleanRelativePath(request.RelativePath)
			task.FileName = filepath.Base(request.FileName)
			task.ExpectedHash = request.Hash
			task.TotalBytes = request.Size
			task.TargetVersion = 1
			task.TempPath = filepath.Join(s.storageRoot, "temp", task.TaskID+".part")
			task.State = "prepared"
			task.LastError = ""
			if err = s.db.Save(&task).Error; err != nil {
				return UploadPrepareResponse{}, err
			}
			s.emitTransferChanged(task)
			return uploadPrepareResponse(task), nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return UploadPrepareResponse{}, err
		}
	}
	err = s.db.Where(
		"batch_id = ? AND device_id = ? AND file_id = ? AND expected_hash = ? AND total_bytes = ? AND state IN ?",
		request.BatchID, deviceID, request.FileID, request.Hash, request.Size,
		[]string{"prepared", "uploading", "paused"},
	).Order("updated_at desc").First(&task).Error
	if err == nil {
		task.TransferredBytes = existingPartSize(task.TempPath, task.TotalBytes)
		task.State = "prepared"
		task.LastError = ""
		if err = s.db.Save(&task).Error; err != nil {
			return UploadPrepareResponse{}, err
		}
		s.emitTransferChanged(task)
		return uploadPrepareResponse(task), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return UploadPrepareResponse{}, err
	}

	taskID := newID("tr")
	task = TransferTask{
		TaskID: taskID, BatchID: request.BatchID, ServerFileID: request.ServerFileID, DeviceID: deviceID,
		Direction: "upload", FileID: request.FileID,
		RelativePath: cleanRelativePath(request.RelativePath),
		FileName:     filepath.Base(request.FileName), ExpectedHash: request.Hash,
		TotalBytes: request.Size, BaseVersion: 0,
		TargetVersion: 1,
		TempPath:      filepath.Join(s.storageRoot, "temp", taskID+".part"),
		State:         "prepared",
	}
	if err = s.db.Create(&task).Error; err != nil {
		return UploadPrepareResponse{}, err
	}
	s.emitTransferChanged(task)
	return uploadPrepareResponse(task), nil
}

func uploadPrepareResponse(task TransferTask) UploadPrepareResponse {
	return UploadPrepareResponse{
		TaskID: task.TaskID, ServerFileID: task.ServerFileID,
		TransferredBytes: task.TransferredBytes, ChunkSize: transferChunkSize,
		Version: task.TargetVersion,
	}
}

func (s *Service) linkDeviceFile(deviceID string, request UploadPrepareRequest, file ServerFile) error {
	return s.db.Model(&Track{}).
		Where("device_id = ? AND file_id = ?", deviceID, request.FileID).
		Updates(map[string]any{
			"server_file_id":  file.ServerFileID,
			"applied_version": 1,
			"hash":            file.Hash,
			"sync_state":      "synced",
		}).Error
}

func (s *Service) finalizeUpload(task *TransferTask) (ServerFile, error) {
	file, err := os.Open(task.TempPath)
	if err != nil {
		return ServerFile{}, err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(hasher, file)
	closeErr := file.Close()
	if copyErr != nil {
		return ServerFile{}, copyErr
	}
	if closeErr != nil {
		return ServerFile{}, closeErr
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != task.ExpectedHash {
		return ServerFile{}, fmt.Errorf("sha256 mismatch: got %s", actualHash)
	}
	actualServerFileID, err := serverFileIDForPath(task.TempPath)
	if err != nil {
		return ServerFile{}, err
	}
	if actualServerFileID != task.ServerFileID {
		return ServerFile{}, fmt.Errorf("serverFileId mismatch: got %s", actualServerFileID)
	}

	extension := safeExtension(task.FileName)
	relativePath := cleanRelativePath(task.RelativePath)
	if relativePath == "" {
		relativePath = filepath.Base(task.FileName)
	}
	objectDir := filepath.Join(s.storageRoot, "library", safePathSegment(task.DeviceID), filepath.Dir(relativePath))
	if err = os.MkdirAll(objectDir, 0755); err != nil {
		return ServerFile{}, err
	}
	objectPath := filepath.Join(objectDir, filepath.Base(relativePath))
	if filepath.Ext(objectPath) == "" {
		objectPath += extension
	}
	if _, statErr := os.Stat(objectPath); statErr == nil {
		return ServerFile{}, errors.New("station destination path already exists")
	}
	if err = os.Rename(task.TempPath, objectPath); err != nil {
		return ServerFile{}, err
	}

	serverFile := ServerFile{
		ServerFileID: task.ServerFileID, StoragePath: objectPath,
		FileName: task.FileName, RelativePath: relativePath, SourceDeviceID: task.DeviceID,
		Size: task.TotalBytes, Hash: actualHash, Version: 1,
		Format: strings.TrimPrefix(strings.ToUpper(extension), "."),
	}
	var sourceTrack Track
	if err := s.db.Where("device_id = ? AND file_id = ?", task.DeviceID, task.FileID).First(&sourceTrack).Error; err == nil {
		serverFile.Title = sourceTrack.Title
		serverFile.Artist = sourceTrack.Artist
		serverFile.Album = sourceTrack.Album
		serverFile.AlbumArtist = sourceTrack.AlbumArtist
		serverFile.TrackNumber = sourceTrack.TrackNumber
		serverFile.DiscNumber = sourceTrack.DiscNumber
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var current ServerFile
		lookup := tx.Where("server_file_id = ?", task.ServerFileID).First(&current).Error
		if errors.Is(lookup, gorm.ErrRecordNotFound) {
			if err := tx.Create(&serverFile).Error; err != nil {
				return err
			}
		} else if lookup == nil {
			if current.StoragePath == "" {
				serverFile.ID = current.ID
				if serverFile.Title == "" {
					serverFile.Title = current.Title
				}
				if serverFile.Artist == "" {
					serverFile.Artist = current.Artist
				}
				if serverFile.Album == "" {
					serverFile.Album = current.Album
				}
				if err := tx.Save(&serverFile).Error; err != nil {
					return err
				}
			} else {
				return errors.New("server file already exists with different content")
			}
		} else {
			return lookup
		}
		if err := tx.Model(&Track{}).
			Where("device_id = ? AND file_id = ?", task.DeviceID, task.FileID).
			Updates(map[string]any{
				"server_file_id":  task.ServerFileID,
				"applied_version": 1,
				"hash":            actualHash,
				"sync_state":      "synced",
			}).Error; err != nil {
			return err
		}
		task.State = "completed"
		task.TransferredBytes = task.TotalBytes
		task.LastError = ""
		return tx.Save(task).Error
	})
	if err != nil {
		// Keep the resumable upload recoverable if committing its database
		// identity fails after the atomic move into the library.
		_ = os.Rename(objectPath, task.TempPath)
	}
	return serverFile, err
}

func newID(prefix string) string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(value)
}

func existingPartSize(path string, maximum int64) int64 {
	info, err := os.Stat(path)
	if err != nil || info.Size() < 0 || info.Size() > maximum {
		return 0
	}
	return info.Size()
}

func cleanRelativePath(value string) string {
	value = filepath.ToSlash(filepath.Clean("/" + value))
	return strings.TrimPrefix(value, "/")
}

func safePathSegment(value string) string {
	value = strings.TrimSpace(value)
	value = regexp.MustCompile(`[^a-zA-Z0-9._-]+`).ReplaceAllString(value, "_")
	value = strings.Trim(value, ".")
	if value == "" {
		return "device"
	}
	return value
}

func safeExtension(name string) string {
	extension := strings.ToLower(filepath.Ext(filepath.Base(name)))
	if len(extension) > 16 || !regexp.MustCompile(`^\.[a-z0-9]+$`).MatchString(extension) {
		return ""
	}
	return extension
}
