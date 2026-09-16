package station

import (
	"strings"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

type DownloadFailureReport struct {
	TaskID string `json:"taskId"`
	Error  string `json:"error"`
}

type UploadFailureReport struct {
	TaskID string `json:"taskId"`
	Error  string `json:"error"`
}

type DownloadProgressReport struct {
	TaskID        string `json:"taskId"`
	ReceivedBytes int64  `json:"receivedBytes"`
	Complete      bool   `json:"complete"`
	Error         string `json:"error,omitempty"`
}

func (s *Service) DownloadProgress(a *ws.Context) {
	deviceID := clientDeviceID(a)
	var report DownloadProgressReport
	if deviceID == "" || !decodeParams(a, &report) || report.TaskID == "" || report.ReceivedBytes < 0 {
		a.SendCode(400, "invalid download progress")
		return
	}
	var task TransferTask
	if s.db.Where("task_id = ? AND device_id = ? AND direction = ?", report.TaskID, deviceID, "download").First(&task).Error != nil {
		a.SendCode(404, "transfer not found")
		return
	}
	if report.ReceivedBytes > task.TotalBytes {
		a.SendCode(409, "invalid download offset")
		return
	}
	task.TransferredBytes = report.ReceivedBytes
	if report.Error != "" {
		task.State = "failed"
		task.LastError = report.Error
	} else if report.Complete {
		if report.ReceivedBytes != task.TotalBytes {
			a.SendCode(409, "incomplete download")
			return
		}
		task.State = "completed"
		task.LastError = ""
	} else {
		task.State = "downloading"
		task.LastError = ""
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if report.Complete && report.Error == "" {
			if err := tx.Model(&Track{}).Where("device_id = ? AND file_id = ?", task.DeviceID, task.FileID).Updates(map[string]any{
				"applied_version": task.TargetVersion, "hash": task.ExpectedHash, "sync_state": "synced",
			}).Error; err != nil {
				return err
			}
		}
		return tx.Save(&task).Error
	})
	if err != nil {
		a.SendCode(500, "transfer update failed")
		return
	}
	s.emitTransferChanged(task)
	if report.Error != "" {
		// Apply this after the aggregate update so even a failure of the final
		// task leaves the batch resumable after the next device connection.
		s.blockDownloadBatch(task.BatchID)
	}
	if report.Complete && report.Error == "" {
		s.emitLibraryChanged(task.DeviceID)
		var file ServerFile
		if s.db.Where("server_file_id = ?", task.ServerFileID).First(&file).Error == nil {
			s.broadcastMetadataForDevice(task.DeviceID, []ServerFile{file})
		}
		_ = s.dispatchNextDownload(task.DeviceID)
	}
	a.Send(map[string]any{"accepted": true})
}

func (s *Service) DownloadFailed(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var report DownloadFailureReport
	if !decodeParams(a, &report) || strings.TrimSpace(report.TaskID) == "" {
		a.SendCode(400, "invalid transfer failure")
		return
	}
	message := strings.TrimSpace(report.Error)
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		message = "download failed"
	}
	var task TransferTask
	if err := s.db.Where(
		"task_id = ? AND device_id = ? AND direction = ?",
		report.TaskID, deviceID, "download",
	).First(&task).Error; err != nil {
		a.SendCode(404, "transfer not found")
		return
	}
	if task.State == "prepared" || task.State == "downloading" || task.State == "paused" {
		task.State = "failed"
		task.LastError = message
		if err := s.db.Save(&task).Error; err != nil {
			a.SendCode(500, "transfer update failed")
			return
		}
		s.emitTransferChanged(task)
		s.blockDownloadBatch(task.BatchID)
	}
	a.Send(map[string]any{"accepted": true})
}

func (s *Service) UploadFailed(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var report UploadFailureReport
	if !decodeParams(a, &report) || strings.TrimSpace(report.TaskID) == "" {
		a.SendCode(400, "invalid transfer failure")
		return
	}
	message := strings.TrimSpace(report.Error)
	if len(message) > 512 {
		message = message[:512]
	}
	var task TransferTask
	if err := s.db.Where("task_id = ? AND device_id = ? AND direction = ?", report.TaskID, deviceID, "upload").First(&task).Error; err != nil {
		a.SendCode(404, "transfer not found")
		return
	}
	if task.State == "prepared" || task.State == "uploading" || task.State == "paused" {
		task.State = "failed"
		task.LastError = message
		if err := s.db.Save(&task).Error; err != nil {
			a.SendCode(500, "transfer update failed")
			return
		}
		s.emitTransferChanged(task)
	}
	a.Send(map[string]any{"accepted": true})
}
