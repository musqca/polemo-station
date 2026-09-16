package station

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

type DeviceSyncRequest struct {
	Confirm         bool     `json:"confirm"`
	TargetLibraryID string   `json:"targetLibraryId,omitempty"`
	ServerFileIDs   []string `json:"serverFileIds,omitempty"`
	IncludeExisting bool     `json:"includeExisting,omitempty"`
}
type DeviceSyncPreview struct {
	BatchID      string `json:"batchId,omitempty"`
	Count        int    `json:"count"`
	Size         int64  `json:"size"`
	SkippedCount int    `json:"skippedCount,omitempty"`
}

func (s *Service) RequestDeviceMetadata(a *ws.Context) {
	deviceID, request, ok := deviceSyncContext(a)
	if !ok {
		return
	}
	files, err := s.deviceMetadataFiles(deviceID)
	if err != nil {
		a.SendCode(500, "metadata unavailable")
		return
	}
	if a.Client.GetKey(metadataCursorCapability).String() == "1" {
		if request.Confirm {
			if err := s.enqueueDeviceMetadata(deviceID, files); err != nil {
				a.SendCode(500, "metadata unavailable")
				return
			}
		}
		a.Send(DeviceSyncPreview{Count: len(files)})
		if request.Confirm {
			s.notifyMetadataCursor(a.Client)
		}
		return
	}
	views, err := s.metadataBatchViews(files)
	if err != nil {
		a.SendCode(500, "metadata unavailable")
		return
	}
	preview := DeviceSyncPreview{Count: len(views)}
	if request.Confirm && len(files) > 0 {
		preview.BatchID = newID("bm")
	}
	a.Send(preview)
	if request.Confirm {
		// Do not keep the request dispatcher occupied while the device drains a
		// potentially large metadata batch. The batch sender applies backpressure
		// on the websocket, and exits automatically if the client disconnects.
		go s.broadcastMetadataBatch(a.Client, preview.BatchID, views)
	}
}

type MetadataBatchCommand struct {
	ServerFileView
	BatchID      string `json:"batchId,omitempty"`
	BatchCurrent int    `json:"batchCurrent,omitempty"`
	BatchTotal   int    `json:"batchTotal,omitempty"`
}

func (s *Service) metadataBatchViews(files []ServerFile) ([]ServerFileView, error) {
	views := make([]ServerFileView, 0, len(files))
	for _, file := range files {
		view, err := s.metadataPushView(file.ServerFileID)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) broadcastMetadataBatch(client *ws.Client, batchID string, views []ServerFileView) {
	if client == nil {
		return
	}
	for index, view := range views {
		message := (&ws.Action{
			Action: "v1.metadata.updated",
			Data: MetadataBatchCommand{
				ServerFileView: view,
				BatchID:        batchID, BatchCurrent: index + 1, BatchTotal: len(views),
			},
		}).Encode()
		// SendMessage is deliberately non-blocking for disposable progress
		// events, but metadata updates are durable work: dropping one leaves the
		// mobile progress counter below batchTotal forever. Apply backpressure
		// here and stop promptly if the device disconnects.
		select {
		case client.Send <- message:
		case <-client.Context().Done():
			return
		}
	}
}

func (s *Service) RequestDeviceDownloads(a *ws.Context) {
	deviceID, request, ok := deviceSyncContext(a)
	if !ok {
		return
	}
	if len(request.ServerFileIDs) > 500 {
		a.SendCode(400, "too many selected files")
		return
	}
	files, skipped, err := s.availableDeviceDownloads(deviceID, request)
	if err != nil {
		a.SendCode(500, "library unavailable")
		return
	}
	preview := previewForFiles(files)
	preview.SkippedCount = skipped
	if !request.Confirm || len(files) == 0 {
		a.Send(preview)
		return
	}
	if len(files) > 500 {
		a.SendCode(400, "library is too large to sync in one batch")
		return
	}
	ids := make([]string, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.ServerFileID)
	}
	offers, err := s.CreateSyncOffers(CreateSyncOfferRequest{DeviceIDs: []string{deviceID}, ServerFileIDs: ids, TargetLibraryID: request.TargetLibraryID})
	if err != nil {
		log.Printf("station device download offer failed device=%s: %v", deviceID, err)
		a.SendCode(500, "sync offer failed")
		return
	}
	if len(offers) > 0 {
		preview.BatchID = offers[0].OfferID
	}
	a.Send(preview)
}

func (s *Service) RequestDeviceUploads(a *ws.Context) {
	deviceID, request, ok := deviceSyncContext(a)
	if !ok {
		return
	}
	tracks, err := s.uploadCandidates(deviceID)
	if err != nil {
		a.SendCode(500, "device library unavailable")
		return
	}
	preview := DeviceSyncPreview{Count: len(tracks)}
	for _, track := range tracks {
		preview.Size += track.Size
	}
	if !request.Confirm || len(tracks) == 0 {
		a.Send(preview)
		return
	}
	preview.BatchID = newID("bt")
	if err := s.createTransferBatch(preview.BatchID, deviceID, "upload", preview.Count, preview.Size); err != nil {
		a.SendCode(500, "upload batch unavailable")
		return
	}
	a.Send(preview)
	for _, track := range tracks {
		if _, err := s.createQueuedUploadTask(preview.BatchID, deviceID, track); err != nil {
			continue
		}
		a.Client.SendActionMsg(&ws.Action{Action: "v1.transfer.upload.request", Data: UploadRequestCommand{
			FileID: track.FileID, BatchID: preview.BatchID, BatchTotalFiles: preview.Count, BatchTotalBytes: preview.Size,
		}})
	}
}

func deviceSyncContext(a *ws.Context) (string, DeviceSyncRequest, bool) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return "", DeviceSyncRequest{}, false
	}
	var request DeviceSyncRequest
	if !decodeParams(a, &request) {
		a.SendCode(400, "invalid sync request")
		return "", DeviceSyncRequest{}, false
	}
	return deviceID, request, true
}

func (s *Service) deviceMetadataFiles(deviceID string) ([]ServerFile, error) {
	var tracks []Track
	if err := s.db.Where("device_id = ? AND server_file_id <> ''", deviceID).Find(&tracks).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tracks))
	for _, track := range tracks {
		ids = append(ids, track.ServerFileID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var files []ServerFile
	return files, s.db.Where("server_file_id IN ? AND deleted_at IS NULL", ids).Find(&files).Error
}

// A catalog row can outlive a moved/deleted media source. Check source access
// before offering a batch, so one stale row cannot prevent all other downloads.
func (s *Service) availableDeviceDownloads(deviceID string, requests ...DeviceSyncRequest) ([]ServerFile, int, error) {
	var candidates []ServerFile
	var err error
	if len(requests) > 0 && (requests[0].IncludeExisting || len(requests[0].ServerFileIDs) > 0 || requests[0].TargetLibraryID != "") {
		query := s.db.Where("deleted_at IS NULL")
		if len(requests[0].ServerFileIDs) > 0 {
			query = query.Where("server_file_id IN ?", requests[0].ServerFileIDs)
		}
		err = query.Find(&candidates).Error
	} else {
		candidates, err = s.downloadCandidates(deviceID)
	}
	if err != nil {
		return nil, 0, err
	}
	available := make([]ServerFile, 0, len(candidates))
	skipped := 0
	for _, file := range candidates {
		audio, openErr := s.OpenAudio(file.ServerFileID)
		if openErr != nil {
			if errors.Is(openErr, os.ErrNotExist) || errors.Is(openErr, os.ErrPermission) {
				skipped++
				log.Printf("station download skipped unavailable audio file=%s name=%q: %v", file.ServerFileID, file.FileName, openErr)
				continue
			}
			return nil, skipped, fmt.Errorf("open download source %s: %w", file.ServerFileID, openErr)
		}
		if err := audio.Close(); err != nil {
			return nil, skipped, err
		}
		available = append(available, file)
	}
	return available, skipped, nil
}

func (s *Service) downloadCandidates(deviceID string) ([]ServerFile, error) {
	var files []ServerFile
	if err := s.db.Where("deleted_at IS NULL").Find(&files).Error; err != nil {
		return nil, err
	}
	result := make([]ServerFile, 0, len(files))
	for _, file := range files {
		var track Track
		found := s.db.Where("device_id = ? AND server_file_id = ?", deviceID, file.ServerFileID).Limit(1).Find(&track)
		if found.Error != nil {
			return nil, found.Error
		}
		if found.RowsAffected == 0 || track.AppliedVersion < file.Version || track.Hash != file.Hash {
			result = append(result, file)
		}
	}
	return result, nil
}

func (s *Service) uploadCandidates(deviceID string) ([]Track, error) {
	var tracks []Track
	if err := s.db.Where("device_id = ?", deviceID).Find(&tracks).Error; err != nil {
		return nil, err
	}
	result := make([]Track, 0, len(tracks))
	for _, track := range tracks {
		if track.Size <= 0 || track.FileID == "" {
			continue
		}
		var file ServerFile
		err := s.db.Where("server_file_id = ? AND deleted_at IS NULL", track.ServerFileID).First(&file).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (track.Hash == "" || track.Hash != file.Hash)) {
			result = append(result, track)
		} else if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func previewForFiles(files []ServerFile) DeviceSyncPreview {
	preview := DeviceSyncPreview{Count: len(files)}
	for _, file := range files {
		preview.Size += file.Size
	}
	return preview
}
