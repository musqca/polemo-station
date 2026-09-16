package station

import (
	"errors"
	"io"
	"time"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

var errTransferBatchNotFound = errors.New("transfer batch not found")

func (s *Service) createTransferBatch(batchID, deviceID, direction string, totalFiles int, totalBytes int64) error {
	if batchID == "" || totalFiles <= 0 {
		return nil
	}
	batch := TransferBatch{
		BatchID: batchID, DeviceID: deviceID, Direction: direction, State: "active",
		TotalFiles: totalFiles, TotalBytes: totalBytes,
	}
	return s.db.Where("batch_id = ?", batchID).Assign(batch).FirstOrCreate(&TransferBatch{BatchID: batchID}).Error
}

func (s *Service) updateTransferBatch(batchID string) {
	if batchID == "" {
		return
	}
	var batch TransferBatch
	if s.db.Where("batch_id = ?", batchID).First(&batch).Error != nil {
		return
	}
	type totals struct {
		Completed   int
		Failed      int
		Cancelled   int
		Transferred int64
	}
	var value totals
	if s.db.Model(&TransferTask{}).Where("batch_id = ?", batchID).Select(
		"SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END) AS completed, "+
			"SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END) AS failed, "+
			"SUM(CASE WHEN state = 'cancelled' THEN 1 ELSE 0 END) AS cancelled, "+
			"COALESCE(SUM(transferred_bytes), 0) AS transferred",
	).Scan(&value).Error != nil {
		return
	}
	batch.CompletedFiles = value.Completed
	batch.FailedFiles = value.Failed
	batch.TransferredBytes = value.Transferred
	terminal := value.Completed + value.Failed + value.Cancelled
	if terminal >= batch.TotalFiles {
		now := time.Now()
		batch.CompletedAt = &now
		switch {
		case value.Failed > 0:
			batch.State = "failed"
		case value.Cancelled > 0:
			batch.State = "cancelled"
		default:
			batch.State = "completed"
		}
	}
	if s.db.Save(&batch).Error != nil || batch.CompletedAt == nil {
		return
	}
	if batch.State == "failed" {
		return
	}
	_ = s.db.Where("batch_id = ?", batchID).Delete(&TransferTask{}).Error
}

func (s *Service) cancelTransferBatch(deviceID, batchID string) error {
	now := time.Now()
	var activeTasks []TransferTask
	if err := s.db.Where("batch_id = ? AND device_id = ? AND state NOT IN ?", batchID, deviceID, []string{"completed", "cancelled"}).Find(&activeTasks).Error; err != nil {
		return err
	}
	for i := range activeTasks {
		s.cancelledTransfers.Store(activeTasks[i].TaskID, true)
		if value, ok := s.activeTransfers.Load(activeTasks[i].TaskID); ok {
			if connection, ok := value.(io.Closer); ok {
				_ = connection.Close()
			}
		}
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&TransferTask{}).Where("batch_id = ? AND device_id = ? AND state NOT IN ?", batchID, deviceID, []string{"completed", "cancelled"}).Updates(map[string]any{"state": "cancelled", "last_error": ""}).Error; err != nil {
			return err
		}
		if err := tx.Model(&TransferBatch{}).Where("batch_id = ? AND device_id = ?", batchID, deviceID).Updates(map[string]any{"state": "cancelled", "completed_at": now}).Error; err != nil {
			return err
		}
		return tx.Where("batch_id = ?", batchID).Delete(&TransferTask{}).Error
	})
	if err == nil {
		s.emitTransferBatchChanged(deviceID)
	}
	return err
}

func (s *Service) deleteTransferBatch(batchID string) error {
	var batch TransferBatch
	if err := s.db.Where("batch_id = ?", batchID).First(&batch).Error; err != nil {
		return errTransferBatchNotFound
	}

	var activeTasks []TransferTask
	if err := s.db.Where("batch_id = ? AND state IN ?", batchID,
		[]string{"prepared", "uploading", "downloading", "paused"}).Find(&activeTasks).Error; err != nil {
		return err
	}
	for i := range activeTasks {
		s.cancelledTransfers.Store(activeTasks[i].TaskID, true)
		if value, ok := s.activeTransfers.Load(activeTasks[i].TaskID); ok {
			if connection, ok := value.(io.Closer); ok {
				_ = connection.Close()
			}
		}
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("batch_id = ?", batchID).Delete(&TransferTask{}).Error; err != nil {
			return err
		}
		return tx.Where("batch_id = ?", batchID).Delete(&TransferBatch{}).Error
	}); err != nil {
		return err
	}

	s.emitTransferBatchChanged(batch.DeviceID)
	return nil
}

func (s *Service) pauseTransferBatch(deviceID, batchID string) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var batch TransferBatch
		if err := tx.Where("batch_id = ? AND device_id = ?", batchID, deviceID).First(&batch).Error; err != nil {
			return err
		}
		if batch.State == "completed" || batch.State == "cancelled" {
			return errTransferNotActionable
		}
		if err := tx.Model(&TransferTask{}).
			Where("batch_id = ? AND device_id = ? AND state NOT IN ?", batchID, deviceID, []string{"completed", "cancelled", "failed"}).
			Updates(map[string]any{"state": "paused", "last_error": ""}).Error; err != nil {
			return err
		}
		if err := tx.Model(&TransferBatch{}).Where("batch_id = ? AND device_id = ?", batchID, deviceID).
			Updates(map[string]any{"state": "paused", "completed_at": nil}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.emitTransferBatchChanged(deviceID)
	return nil
}

func (s *Service) resumeTransferBatch(deviceID, batchID string) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var batch TransferBatch
		if err := tx.Where("batch_id = ? AND device_id = ?", batchID, deviceID).First(&batch).Error; err != nil {
			return err
		}
		if batch.State == "completed" || batch.State == "cancelled" {
			return errTransferNotActionable
		}
		if err := tx.Model(&TransferTask{}).
			Where("batch_id = ? AND device_id = ? AND state = ?", batchID, deviceID, "paused").
			Updates(map[string]any{"state": "prepared", "last_error": ""}).Error; err != nil {
			return err
		}
		if err := tx.Model(&TransferBatch{}).Where("batch_id = ? AND device_id = ?", batchID, deviceID).
			Updates(map[string]any{"state": "active", "completed_at": nil}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.emitTransferBatchChanged(deviceID)
	return nil
}

func (s *Service) retryTransferBatch(deviceID, batchID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var batch TransferBatch
		if err := tx.Where("batch_id = ? AND device_id = ? AND state = ?", batchID, deviceID, "failed").First(&batch).Error; err != nil {
			return err
		}
		if err := tx.Where("batch_id = ? AND state = ?", batchID, "failed").Delete(&TransferTask{}).Error; err != nil {
			return err
		}
		return tx.Model(&batch).Updates(map[string]any{"state": "active", "failed_files": 0, "completed_at": nil}).Error
	})
}

func (s *Service) ListTransferBatches() ([]TransferBatch, error) {
	var batches []TransferBatch
	err := s.db.Order("updated_at desc").Limit(100).Find(&batches).Error
	return batches, err
}

func (s *Service) createQueuedUploadTask(batchID, deviceID string, track Track) (TransferTask, error) {
	task := TransferTask{
		TaskID: newID("tr"), BatchID: batchID, DeviceID: deviceID, Direction: "upload",
		FileID: track.FileID, RelativePath: track.RelativePath, FileName: track.FileName,
		TotalBytes: track.Size, ExpectedHash: track.Hash, State: "queued",
	}
	err := s.db.Where("batch_id = ? AND device_id = ? AND file_id = ?", batchID, deviceID, track.FileID).FirstOrCreate(&task).Error
	return task, err
}

func (s *Service) DispatchPendingTransfers(deviceID string) {
	client, err := s.downloadClient(deviceID)
	if err != nil {
		return
	}
	var uploads []TransferTask
	if s.db.Joins("JOIN transfer_batches ON transfer_batches.batch_id = transfer_tasks.batch_id").
		Where("transfer_tasks.device_id = ? AND transfer_tasks.direction = ? AND transfer_tasks.state IN ? AND transfer_batches.state = ?", deviceID, "upload", []string{"queued", "prepared", "uploading"}, "active").
		Find(&uploads).Error == nil {
		for _, task := range uploads {
			var batch TransferBatch
			_ = s.db.Where("batch_id = ?", task.BatchID).First(&batch).Error
			client.SendActionMsg(&ws.Action{Action: "v1.transfer.upload.request", Data: UploadRequestCommand{
				FileID: task.FileID, BatchID: task.BatchID, BatchTotalFiles: batch.TotalFiles, BatchTotalBytes: batch.TotalBytes,
			}})
		}
	}
	// Downloads are deliberately serialized per device. Starting every task in
	// a large batch at once overwhelms mobile clients and leaves hundreds of
	// HTTP requests active when the WebSocket drops. The next task is dispatched
	// only after the current one reports completion.
	_ = s.dispatchNextDownload(deviceID)
}

// dispatchNextDownload starts at most one download for a device. The task is
// marked downloading before the command is queued so concurrent completion,
// reconnect, and retry paths cannot dispatch two tasks at once.
func (s *Service) dispatchNextDownload(deviceID string) error {
	if _, err := s.downloadClient(deviceID); err != nil {
		return err
	}

	s.transferMu.Lock()
	defer s.transferMu.Unlock()

	var active TransferTask
	if err := s.db.Joins("LEFT JOIN transfer_batches ON transfer_batches.batch_id = transfer_tasks.batch_id").
		Where("transfer_tasks.device_id = ? AND transfer_tasks.direction = ? AND transfer_tasks.state = ? AND (transfer_tasks.batch_id = '' OR transfer_batches.state = ? OR transfer_batches.batch_id IS NULL)", deviceID, "download", "downloading", "active").
		Order("transfer_tasks.created_at ASC, transfer_tasks.id ASC").First(&active).Error; err == nil {
		return nil
	}

	var task TransferTask
	query := s.db.Joins("LEFT JOIN transfer_batches ON transfer_batches.batch_id = transfer_tasks.batch_id").
		Where("transfer_tasks.device_id = ? AND transfer_tasks.direction = ? AND transfer_tasks.state = ? AND (transfer_tasks.batch_id = '' OR transfer_batches.state = ? OR transfer_batches.batch_id IS NULL)", deviceID, "download", "prepared", "active").
		Order("transfer_tasks.created_at ASC, transfer_tasks.id ASC")
	if err := query.First(&task).Error; err != nil {
		// A disconnect or a device-reported failure blocks the batch. On the
		// next successful device connection, retry that current task first.
		query = s.db.Joins("LEFT JOIN transfer_batches ON transfer_batches.batch_id = transfer_tasks.batch_id").
			Where("transfer_tasks.device_id = ? AND transfer_tasks.direction = ? AND transfer_tasks.state IN ? AND transfer_tasks.last_error <> '' AND (transfer_tasks.batch_id = '' OR transfer_batches.state = ? OR transfer_batches.batch_id IS NULL)", deviceID, "download", []string{"paused", "failed"}, "paused").
			Order("transfer_tasks.created_at ASC, transfer_tasks.id ASC")
		if err := query.First(&task).Error; err != nil {
			return nil
		}
		if err := s.db.Model(&TransferBatch{}).Where("batch_id = ?", task.BatchID).
			Updates(map[string]any{"state": "active", "completed_at": nil}).Error; err != nil {
			return err
		}
	}

	task.State = "downloading"
	task.LastError = ""
	if err := s.db.Save(&task).Error; err != nil {
		return err
	}
	s.emitTransferChanged(task)
	return s.dispatchDownload(task)
}

// pauseDeviceDownloads makes an interrupted HTTP download resumable. Prepared
// tasks are left untouched; only the in-flight task is paused and becomes the
// first task resumed after the next device hello.
func (s *Service) pauseDeviceDownloads(deviceID string) {
	var tasks []TransferTask
	if err := s.db.Where("device_id = ? AND direction = ? AND state = ?", deviceID, "download", "downloading").Find(&tasks).Error; err != nil {
		return
	}
	changed := false
	for i := range tasks {
		task := tasks[i]
		if err := s.db.Model(&TransferTask{}).Where("task_id = ?", task.TaskID).
			Updates(map[string]any{"state": "paused", "last_error": "device disconnected"}).Error; err != nil {
			continue
		}
		_ = s.db.Model(&TransferBatch{}).Where("batch_id = ? AND state = ?", task.BatchID, "active").
			Updates(map[string]any{"state": "paused", "completed_at": nil}).Error
		task.State = "paused"
		task.LastError = "device disconnected"
		s.emitTransferChanged(task)
		changed = true
	}
	if changed {
		s.emitTransferBatchChanged(deviceID)
	}
}

func (s *Service) blockDownloadBatch(batchID string) {
	if batchID == "" {
		return
	}
	if err := s.db.Model(&TransferBatch{}).Where("batch_id = ?", batchID).
		Updates(map[string]any{"state": "paused", "completed_at": nil}).Error; err != nil {
		return
	}
	var batch TransferBatch
	if s.db.Where("batch_id = ?", batchID).First(&batch).Error == nil {
		s.emitTransferBatchChanged(batch.DeviceID)
	}
}
