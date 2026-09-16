package station

import (
	"errors"
	"io"

	"github.com/wonli/aqi/ws"
)

var errTransferNotActionable = errors.New("transfer is not actionable")

func (s *Service) RetryTransfer(taskID string) error {
	var task TransferTask
	if err := s.db.Where("task_id = ? AND direction = ?", taskID, "download").First(&task).Error; err != nil {
		return err
	}
	if task.State == "completed" {
		return errTransferNotActionable
	}
	var track Track
	if err := s.db.Where("device_id = ? AND file_id = ?", task.DeviceID, task.FileID).First(&track).Error; err != nil {
		return err
	}
	var serverFile ServerFile
	if err := s.db.Where("server_file_id = ? AND deleted_at IS NULL", task.ServerFileID).First(&serverFile).Error; err != nil {
		return err
	}
	if track.AppliedVersion >= serverFile.Version && track.Hash == serverFile.Hash {
		return errors.New("device already has the latest version")
	}
	// Verify that the device is reachable before mutating the task. A failed
	// retry therefore leaves the previous state and error visible in the UI.
	if _, err := s.downloadClient(task.DeviceID); err != nil {
		return err
	}
	s.cancelledTransfers.Delete(task.TaskID)
	task.ServerFileID = serverFile.ServerFileID
	task.FileName = serverFile.FileName
	task.ExpectedHash = serverFile.Hash
	task.TotalBytes = serverFile.Size
	task.BaseVersion = track.AppliedVersion
	task.TargetVersion = serverFile.Version
	task.State = "prepared"
	task.LastError = ""
	if err := s.db.Save(&task).Error; err != nil {
		return err
	}
	if task.BatchID != "" {
		if err := s.db.Model(&TransferBatch{}).Where("batch_id = ? AND state IN ?", task.BatchID, []string{"failed", "paused"}).
			Updates(map[string]any{"state": "active", "completed_at": nil}).Error; err != nil {
			return err
		}
	}
	s.emitTransferChanged(task)
	return s.dispatchNextDownload(task.DeviceID)
}

func (s *Service) CancelTransfer(taskID string) error {
	var task TransferTask
	if err := s.db.Where("task_id = ?", taskID).First(&task).Error; err != nil {
		return err
	}
	if task.State == "completed" {
		return errTransferNotActionable
	}
	if task.State == "cancelled" {
		return nil
	}
	s.cancelledTransfers.Store(task.TaskID, true)
	task.State = "cancelled"
	task.LastError = ""
	if err := s.db.Save(&task).Error; err != nil {
		s.cancelledTransfers.Delete(task.TaskID)
		return err
	}
	s.emitTransferChanged(task)
	if value, ok := s.activeTransfers.Load(task.TaskID); ok {
		if connection, ok := value.(io.Closer); ok {
			_ = connection.Close()
		}
	}
	return nil
}

func (s *Service) CancelTransferRequest(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var request struct {
		TaskID string `json:"taskId"`
	}
	if !decodeParams(a, &request) || request.TaskID == "" {
		a.SendCode(400, "invalid transfer")
		return
	}
	var task TransferTask
	if err := s.db.Select("device_id").Where("task_id = ?", request.TaskID).First(&task).Error; err != nil {
		a.SendCode(404, "transfer not found")
		return
	}
	if task.DeviceID != deviceID {
		a.SendCode(403, "transfer belongs to another device")
		return
	}
	if err := s.CancelTransfer(request.TaskID); err != nil {
		a.SendCode(409, err.Error())
		return
	}
	a.Send(map[string]any{"cancelled": true})
}

func (s *Service) CancelTransferBatchRequest(a *ws.Context) {
	deviceID := clientDeviceID(a)
	var request struct {
		BatchID string `json:"batchId"`
	}
	if deviceID == "" || !decodeParams(a, &request) || request.BatchID == "" {
		a.SendCode(400, "invalid transfer batch")
		return
	}
	if err := s.cancelTransferBatch(deviceID, request.BatchID); err != nil {
		a.SendCode(409, err.Error())
		return
	}
	a.Send(map[string]any{"cancelled": true})
}

func (s *Service) DeleteTransferBatchRequest(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request struct {
		BatchID string `json:"batchId"`
	}
	if !decodeParams(a, &request) || request.BatchID == "" {
		a.SendCode(400, "invalid transfer batch")
		return
	}
	if err := s.deleteTransferBatch(request.BatchID); err != nil {
		a.SendCode(409, err.Error())
		return
	}
	a.Send(map[string]any{"deleted": true})
}

func (s *Service) PauseTransferBatchRequest(a *ws.Context) {
	deviceID := clientDeviceID(a)
	var request struct {
		BatchID string `json:"batchId"`
	}
	if deviceID == "" || !decodeParams(a, &request) || request.BatchID == "" {
		a.SendCode(400, "invalid transfer batch")
		return
	}
	if err := s.pauseTransferBatch(deviceID, request.BatchID); err != nil {
		a.SendCode(409, err.Error())
		return
	}
	a.Send(map[string]any{"paused": true})
}

func (s *Service) ResumeTransferBatchRequest(a *ws.Context) {
	deviceID := clientDeviceID(a)
	var request struct {
		BatchID string `json:"batchId"`
	}
	if deviceID == "" || !decodeParams(a, &request) || request.BatchID == "" {
		a.SendCode(400, "invalid transfer batch")
		return
	}
	if err := s.resumeTransferBatch(deviceID, request.BatchID); err != nil {
		a.SendCode(409, err.Error())
		return
	}
	s.DispatchPendingTransfers(deviceID)
	a.Send(map[string]any{"resumed": true})
}

func (s *Service) RetryTransferBatchRequest(a *ws.Context) {
	deviceID := clientDeviceID(a)
	var request struct {
		BatchID string `json:"batchId"`
	}
	if deviceID == "" || !decodeParams(a, &request) || request.BatchID == "" {
		a.SendCode(400, "invalid transfer batch")
		return
	}
	if err := s.retryTransferBatch(deviceID, request.BatchID); err != nil {
		a.SendCode(409, err.Error())
		return
	}
	a.Send(map[string]any{"accepted": true})
}

func (s *Service) transferCancelled(taskID string) bool {
	_, cancelled := s.cancelledTransfers.Load(taskID)
	if cancelled {
		return true
	}
	var task TransferTask
	return s.db.Select("state").Where("task_id = ?", taskID).First(&task).Error == nil && task.State == "cancelled"
}
