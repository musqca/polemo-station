package station

import (
	"encoding/json"
	"testing"

	"github.com/wonli/aqi/ws"
)

type testTransferCloser struct{ closed bool }

func (closer *testTransferCloser) Close() error {
	closer.closed = true
	return nil
}

func TestRetryPreparedDownloadReusesTaskAndDispatchesCommand(t *testing.T) {
	service := newTransferTestService(t)
	track := Track{
		DeviceID: "device-a", FileID: "file-a", RelativePath: "Album/song.flac",
		ServerFileID: "sf-a", AppliedVersion: 1, Hash: "old", SyncState: "server_newer",
	}
	serverFile := ServerFile{
		ServerFileID: "sf-a", FileName: "song.flac", Size: 1234,
		Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Version: 2,
	}
	task := TransferTask{
		TaskID: "tr-a", Direction: "download", DeviceID: track.DeviceID, FileID: track.FileID,
		BatchID: "so-a", ServerFileID: serverFile.ServerFileID, State: "prepared",
	}
	if err := service.db.Create(&track).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&serverFile).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	client := &ws.Client{Send: make(chan []byte, 1)}
	service.onlineDevices.Store(track.DeviceID, client)

	if err := service.RetryTransfer(task.TaskID); err != nil {
		t.Fatal(err)
	}
	var updated TransferTask
	if err := service.db.Where("task_id = ?", task.TaskID).First(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if updated.State != "downloading" || updated.LastError != "" || updated.TargetVersion != 2 || updated.TotalBytes != 1234 {
		t.Fatalf("unexpected retried task: %+v", updated)
	}
	select {
	case message := <-client.Send:
		var action ws.Action
		if err := json.Unmarshal(message, &action); err != nil {
			t.Fatal(err)
		}
		if action.Action != "v1.transfer.download.request" {
			t.Fatalf("action = %q", action.Action)
		}
		data, ok := action.Data.(map[string]any)
		if !ok || data["batchId"] != task.BatchID {
			t.Fatalf("batchId was not dispatched: %#v", action.Data)
		}
	default:
		t.Fatal("download command was not dispatched")
	}
}

func TestCancelDownloadMarksTaskAndClosesConnection(t *testing.T) {
	service := newTransferTestService(t)
	task := TransferTask{TaskID: "tr-cancel", Direction: "download", DeviceID: "device-a", State: "downloading"}
	if err := service.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	closer := &testTransferCloser{}
	service.activeTransfers.Store(task.TaskID, closer)

	if err := service.CancelTransfer(task.TaskID); err != nil {
		t.Fatal(err)
	}
	var updated TransferTask
	if err := service.db.Where("task_id = ?", task.TaskID).First(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if updated.State != "cancelled" || !closer.closed || !service.transferCancelled(task.TaskID) {
		t.Fatalf("cancel did not take effect: task=%+v closed=%v", updated, closer.closed)
	}
}

func TestDeleteTransferBatchRemovesTasksAndClosesConnection(t *testing.T) {
	service := newTransferTestService(t)
	batch := TransferBatch{BatchID: "bt-delete", DeviceID: "device-a", Direction: "download", State: "paused", TotalFiles: 1, TotalBytes: 42}
	task := TransferTask{TaskID: "tr-delete", BatchID: batch.BatchID, DeviceID: batch.DeviceID, Direction: "download", State: "paused", TotalBytes: 42}
	if err := service.db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	closer := &testTransferCloser{}
	service.activeTransfers.Store(task.TaskID, closer)

	if err := service.deleteTransferBatch(batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if !closer.closed || !service.transferCancelled(task.TaskID) {
		t.Fatalf("delete did not close the active transfer: closed=%v cancelled=%v", closer.closed, service.transferCancelled(task.TaskID))
	}
	var deletedBatch TransferBatch
	if err := service.db.Where("batch_id = ?", batch.BatchID).First(&deletedBatch).Error; err == nil {
		t.Fatal("transfer batch still exists")
	}
	var deletedTask TransferTask
	if err := service.db.Where("task_id = ?", task.TaskID).First(&deletedTask).Error; err == nil {
		t.Fatal("transfer task still exists")
	}
}

func TestCancelTransferBatchMarksTasksCancelledAndClosesConnection(t *testing.T) {
	service := newTransferTestService(t)
	batch := TransferBatch{BatchID: "bt-cancel", DeviceID: "device-a", Direction: "download", State: "active", TotalFiles: 1, TotalBytes: 42}
	task := TransferTask{TaskID: "tr-cancel-batch", BatchID: batch.BatchID, DeviceID: batch.DeviceID, Direction: "download", State: "downloading", TotalBytes: 42}
	if err := service.db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	closer := &testTransferCloser{}
	service.activeTransfers.Store(task.TaskID, closer)

	if err := service.cancelTransferBatch(batch.DeviceID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if !closer.closed || !service.transferCancelled(task.TaskID) {
		t.Fatalf("batch cancel did not close the active transfer: closed=%v cancelled=%v", closer.closed, service.transferCancelled(task.TaskID))
	}
	var cancelledBatch TransferBatch
	if err := service.db.Where("batch_id = ?", batch.BatchID).First(&cancelledBatch).Error; err != nil {
		t.Fatal(err)
	}
	if cancelledBatch.State != "cancelled" {
		t.Fatalf("batch state = %q, want cancelled", cancelledBatch.State)
	}
	var deletedTask TransferTask
	if err := service.db.Where("task_id = ?", task.TaskID).First(&deletedTask).Error; err == nil {
		t.Fatal("cancelled transfer task still exists")
	}
}

func TestPauseAndResumeUploadBatchPreservesTask(t *testing.T) {
	service := newTransferTestService(t)
	batch := TransferBatch{BatchID: "bt-pause", DeviceID: "device-a", Direction: "upload", State: "active", TotalFiles: 1, TotalBytes: 42}
	task := TransferTask{TaskID: "tr-pause", BatchID: batch.BatchID, DeviceID: batch.DeviceID, Direction: "upload", FileID: "file-a", State: "uploading", TotalBytes: 42, TransferredBytes: 21}
	if err := service.db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	if err := service.pauseTransferBatch(batch.DeviceID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	var pausedTask TransferTask
	var pausedBatch TransferBatch
	if err := service.db.Where("task_id = ?", task.TaskID).First(&pausedTask).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Where("batch_id = ?", batch.BatchID).First(&pausedBatch).Error; err != nil {
		t.Fatal(err)
	}
	if pausedTask.State != "paused" || pausedTask.TransferredBytes != 21 || pausedBatch.State != "paused" {
		t.Fatalf("unexpected paused state: task=%+v batch=%+v", pausedTask, pausedBatch)
	}

	if err := service.resumeTransferBatch(batch.DeviceID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	var resumedTask TransferTask
	var resumedBatch TransferBatch
	if err := service.db.Where("task_id = ?", task.TaskID).First(&resumedTask).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Where("batch_id = ?", batch.BatchID).First(&resumedBatch).Error; err != nil {
		t.Fatal(err)
	}
	if resumedTask.State != "prepared" || resumedTask.TransferredBytes != 21 || resumedBatch.State != "active" {
		t.Fatalf("unexpected resumed state: task=%+v batch=%+v", resumedTask, resumedBatch)
	}
}
