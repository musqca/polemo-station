package station

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

type httpTransferAck struct {
	TaskID        string `json:"taskId"`
	ReceivedBytes int64  `json:"receivedBytes"`
	Complete      bool   `json:"complete"`
	ServerFileID  string `json:"serverFileId,omitempty"`
	Version       int64  `json:"version,omitempty"`
	Hash          string `json:"hash,omitempty"`
}

// UploadHTTP accepts one resumable Content-Range chunk. The control WebSocket
// creates the task; audio bytes deliberately travel over HTTP so a mobile
// connection can resume from the persisted offset after interruption.
func (s *Service) UploadHTTP(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	deviceID := r.URL.Query().Get("deviceId")
	if taskID == "" || deviceID == "" {
		http.Error(w, "invalid transfer", http.StatusBadRequest)
		return
	}
	start, end, total, err := parseContentRange(r.Header.Get("Content-Range"))
	if err != nil {
		http.Error(w, "invalid content range", http.StatusBadRequest)
		return
	}
	var task TransferTask
	if err = s.db.Where("task_id = ? AND device_id = ? AND direction = ?", taskID, deviceID, "upload").First(&task).Error; err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, "transfer not found", status)
		return
	}
	if task.State == "cancelled" || task.State == "completed" || task.State == "failed" || task.State == "paused" {
		http.Error(w, "transfer is not active", http.StatusConflict)
		return
	}
	if total != task.TotalBytes || start < 0 || end < start || end >= total {
		http.Error(w, "content range does not match task", http.StatusConflict)
		return
	}

	lock := s.transferLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	if err = s.db.Where("task_id = ?", taskID).First(&task).Error; err != nil {
		http.Error(w, "transfer not found", http.StatusNotFound)
		return
	}
	if s.transferCancelled(taskID) {
		http.Error(w, "transfer cancelled", http.StatusConflict)
		return
	}
	offset := existingPartSize(task.TempPath, task.TotalBytes)
	if start != offset {
		writeHTTPTransferJSON(w, http.StatusConflict, httpTransferAck{TaskID: taskID, ReceivedBytes: offset})
		return
	}
	contentLength := end - start + 1
	if r.ContentLength >= 0 && r.ContentLength != contentLength {
		http.Error(w, "content length does not match range", http.StatusBadRequest)
		return
	}
	part, err := os.OpenFile(task.TempPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		http.Error(w, "temporary file unavailable", http.StatusInternalServerError)
		return
	}
	defer part.Close()
	if _, err = part.Seek(start, io.SeekStart); err == nil {
		_, err = io.CopyN(part, r.Body, contentLength)
	}
	if err != nil {
		http.Error(w, "chunk write failed", http.StatusBadRequest)
		return
	}
	task.TransferredBytes = end + 1
	task.State = "uploading"
	task.LastError = ""
	result := s.db.Model(&TransferTask{}).Where("task_id = ? AND state IN ?", taskID, []string{"queued", "prepared", "uploading"}).
		Updates(map[string]any{"transferred_bytes": task.TransferredBytes, "state": task.State, "last_error": task.LastError})
	if result.Error != nil {
		http.Error(w, "progress update failed", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		http.Error(w, "transfer is not active", http.StatusConflict)
		return
	}
	s.emitTransferChanged(task)
	if task.TransferredBytes < task.TotalBytes {
		writeHTTPTransferJSON(w, http.StatusOK, httpTransferAck{TaskID: taskID, ReceivedBytes: task.TransferredBytes})
		return
	}
	if err = part.Sync(); err != nil {
		http.Error(w, "file sync failed", http.StatusInternalServerError)
		return
	}
	serverFile, err := s.finalizeUpload(&task)
	if err != nil {
		task.State = "failed"
		task.LastError = err.Error()
		_ = s.db.Save(&task).Error
		s.emitTransferChanged(task)
		http.Error(w, "file verification failed", http.StatusConflict)
		return
	}
	s.emitTransferChanged(task)
	s.emitLibraryChanged(task.DeviceID)
	writeHTTPTransferJSON(w, http.StatusOK, httpTransferAck{TaskID: task.TaskID, ReceivedBytes: task.TotalBytes, Complete: true, ServerFileID: serverFile.ServerFileID, Version: serverFile.Version, Hash: serverFile.Hash})
}

func (s *Service) DownloadHTTP(w http.ResponseWriter, r *http.Request, taskID string) {
	deviceID := r.URL.Query().Get("deviceId")
	var task TransferTask
	if taskID == "" || deviceID == "" || s.db.Where(
		"task_id = ? AND device_id = ? AND direction = ?", taskID, deviceID, "download",
	).First(&task).Error != nil {
		http.Error(w, "transfer not found", http.StatusNotFound)
		return
	}
	if task.State == "cancelled" || task.State == "completed" || task.State == "failed" {
		http.Error(w, "transfer is not active", http.StatusConflict)
		return
	}
	var file ServerFile
	if s.db.Where("server_file_id = ? AND deleted_at IS NULL", task.ServerFileID).First(&file).Error != nil {
		http.Error(w, "server file unavailable", http.StatusNotFound)
		return
	}
	source, err := os.Open(file.StoragePath)
	if err != nil {
		http.Error(w, "server file unavailable", http.StatusNotFound)
		return
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || info.Size() != task.TotalBytes {
		http.Error(w, "server file changed", http.StatusConflict)
		return
	}
	task.State = "downloading"
	task.LastError = ""
	_ = s.db.Save(&task).Error
	s.emitTransferChanged(task)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", `"`+task.ExpectedHash+`"`)
	w.Header().Set("X-Polemo-Task-Id", task.TaskID)
	http.ServeContent(w, r, task.FileName, info.ModTime(), source)
}

func parseContentRange(value string) (int64, int64, int64, error) {
	parts := strings.Fields(value)
	if len(parts) != 2 || parts[0] != "bytes" {
		return 0, 0, 0, errors.New("invalid content range")
	}
	rangeParts := strings.Split(parts[1], "/")
	if len(rangeParts) != 2 {
		return 0, 0, 0, errors.New("invalid content range")
	}
	bounds := strings.Split(rangeParts[0], "-")
	if len(bounds) != 2 {
		return 0, 0, 0, errors.New("invalid content range")
	}
	start, err := strconv.ParseInt(bounds[0], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	end, err := strconv.ParseInt(bounds[1], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	total, err := strconv.ParseInt(rangeParts[1], 10, 64)
	if err != nil || total <= 0 {
		return 0, 0, 0, fmt.Errorf("invalid total")
	}
	return start, end, total, nil
}

func writeHTTPTransferJSON(w http.ResponseWriter, status int, value httpTransferAck) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
