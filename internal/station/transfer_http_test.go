package station

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/libtnb/sqlite"
	"gorm.io/gorm"
)

func TestHTTPUploadResumesAndFinalizes(t *testing.T) {
	service := newTransferTestService(t)
	content := bytes.Repeat([]byte("polemo-http-upload"), 4096)
	hashBytes := sha256.Sum256(content)
	hash := hex.EncodeToString(hashBytes[:])
	serverFileID, err := serverFileIDForReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	const deviceID = "device-http"
	const batchID = "batch-http"
	track := Track{DeviceID: deviceID, FileID: serverFileID, RelativePath: "Album/song.flac", FileName: "song.flac", Size: int64(len(content))}
	if err := service.db.Create(&track).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.createTransferBatch(batchID, deviceID, "upload", 1, int64(len(content))); err != nil {
		t.Fatal(err)
	}
	prepared, err := service.prepareUpload(deviceID, UploadPrepareRequest{
		FileID: serverFileID, RelativePath: track.RelativePath, FileName: track.FileName,
		Size: int64(len(content)), Hash: hash, ServerFileID: serverFileID, BatchID: batchID,
	})
	if err != nil {
		t.Fatal(err)
	}
	middle := len(content) / 2
	first := httptest.NewRequest(http.MethodPut, "/?deviceId="+deviceID, bytes.NewReader(content[:middle]))
	first.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", middle-1, len(content)))
	firstResult := httptest.NewRecorder()
	service.UploadHTTP(firstResult, first, prepared.TaskID)
	if firstResult.Code != http.StatusOK {
		t.Fatalf("first chunk status=%d body=%s", firstResult.Code, firstResult.Body.String())
	}
	var firstAck httpTransferAck
	if json.Unmarshal(firstResult.Body.Bytes(), &firstAck) != nil || firstAck.ReceivedBytes != int64(middle) || firstAck.Complete {
		t.Fatalf("unexpected first ack: %+v", firstAck)
	}
	resumed, err := service.prepareUpload(deviceID, UploadPrepareRequest{
		FileID: serverFileID, RelativePath: track.RelativePath, FileName: track.FileName,
		Size: int64(len(content)), Hash: hash, ServerFileID: serverFileID, BatchID: batchID,
	})
	if err != nil || resumed.TaskID != prepared.TaskID || resumed.TransferredBytes != int64(middle) {
		t.Fatalf("resume response=%+v err=%v", resumed, err)
	}
	second := httptest.NewRequest(http.MethodPut, "/?deviceId="+deviceID, bytes.NewReader(content[middle:]))
	second.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", middle, len(content)-1, len(content)))
	secondResult := httptest.NewRecorder()
	service.UploadHTTP(secondResult, second, prepared.TaskID)
	if secondResult.Code != http.StatusOK {
		t.Fatalf("second chunk status=%d body=%s", secondResult.Code, secondResult.Body.String())
	}
	var completed httpTransferAck
	if json.Unmarshal(secondResult.Body.Bytes(), &completed) != nil || !completed.Complete || completed.Hash != hash {
		t.Fatalf("unexpected completion: %+v", completed)
	}
}

func TestUploadProtocolRequiresBatchID(t *testing.T) {
	request := UploadPrepareRequest{
		FileID:       "sf1_" + fmt.Sprintf("%064x", 1),
		ServerFileID: "sf1_" + fmt.Sprintf("%064x", 1),
		FileName:     "song.flac",
		Size:         1,
		Hash:         fmt.Sprintf("%064x", 1),
	}
	if validUploadRequest(request) {
		t.Fatal("upload preparation without batchId must be rejected")
	}
	request.BatchID = "batch-required"
	if !validUploadRequest(request) {
		t.Fatal("valid batched upload preparation was rejected")
	}
}

func TestHTTPDownloadHonorsRange(t *testing.T) {
	service := newTransferTestService(t)
	content := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	storage := filepath.Join(t.TempDir(), "song.flac")
	if err := os.WriteFile(storage, content, 0644); err != nil {
		t.Fatal(err)
	}
	file := ServerFile{ServerFileID: "sf-range", StoragePath: storage, FileName: "song.flac", Size: int64(len(content)), Hash: fmt.Sprintf("%064x", 1), Version: 1}
	task := TransferTask{TaskID: "tr-range", BatchID: "batch-range", DeviceID: "device-range", Direction: "download", ServerFileID: file.ServerFileID, FileName: file.FileName, TotalBytes: file.Size, ExpectedHash: file.Hash, State: "prepared"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.createTransferBatch(task.BatchID, task.DeviceID, "download", 1, task.TotalBytes); err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/?deviceId=device-range", nil)
	request.Header.Set("Range", "bytes=10-")
	result := httptest.NewRecorder()
	service.DownloadHTTP(result, request, task.TaskID)
	if result.Code != http.StatusPartialContent || !bytes.Equal(result.Body.Bytes(), content[10:]) {
		t.Fatalf("range status=%d body=%q", result.Code, result.Body.Bytes())
	}
}

func TestCompletedBatchDeletesTaskDetails(t *testing.T) {
	service := newTransferTestService(t)
	if err := service.createTransferBatch("batch-done", "device-a", "upload", 2, 30); err != nil {
		t.Fatal(err)
	}
	tasks := []TransferTask{
		{TaskID: "task-1", BatchID: "batch-done", DeviceID: "device-a", Direction: "upload", State: "completed", TotalBytes: 10, TransferredBytes: 10},
		{TaskID: "task-2", BatchID: "batch-done", DeviceID: "device-a", Direction: "upload", State: "completed", TotalBytes: 20, TransferredBytes: 20},
	}
	if err := service.db.Create(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	service.updateTransferBatch("batch-done")
	var batch TransferBatch
	if err := service.db.Where("batch_id = ?", "batch-done").First(&batch).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	_ = service.db.Model(&TransferTask{}).Where("batch_id = ?", "batch-done").Count(&count).Error
	if batch.State != "completed" || batch.CompletedFiles != 2 || count != 0 {
		t.Fatalf("batch=%+v remaining tasks=%d", batch, count)
	}
}

func newTransferTestService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	dsn := fmt.Sprintf("file:transfer-%s?mode=memory&cache=shared", newID("test"))
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	service := NewService(database, "", filepath.Join(root, "library"))
	if err := service.Migrate(); err != nil {
		t.Fatal(err)
	}
	return service
}
