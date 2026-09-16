package station

import "github.com/wonli/aqi/ws"

type webSnapshot struct {
	Devices   []Device         `json:"devices"`
	Files     []ServerFileView `json:"files"`
	Sources   []MediaSource    `json:"sources"`
	Transfers []TransferTask   `json:"transfers"`
	Batches   []TransferBatch  `json:"batches"`
	Queue     []SyncQueueItem  `json:"queue"`
	Playback  []DevicePlayback `json:"playback"`
}

type webDeviceFilesRequest struct {
	DeviceID string `json:"deviceId"`
}

type webDeviceUploadRequest struct {
	DeviceID string `json:"deviceId"`
	FileID   string `json:"fileId"`
}

type webDeviceBatchRequest struct {
	DeviceID string `json:"deviceId"`
}

type webDevicePinRequest struct {
	DeviceID string `json:"deviceId"`
	Pinned   bool   `json:"pinned"`
}

type webQueueItemRequest struct {
	QueueItemID string `json:"queueItemId"`
}

func isStationWeb(a *ws.Context) bool {
	return a != nil && a.Client != nil && a.Client.HttpRequest != nil && a.Client.HttpRequest.URL.Query().Get("role") == "station-web"
}

func (s *Service) WebSnapshot(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	devices, err := s.ListDevices()
	if err != nil {
		a.SendCode(500, "devices unavailable")
		return
	}
	files, err := s.ListServerFiles()
	if err != nil {
		a.SendCode(500, "library unavailable")
		return
	}
	sources, err := s.ListMediaSources()
	if err != nil {
		a.SendCode(500, "media sources unavailable")
		return
	}
	transfers, err := s.ListTransfers()
	if err != nil {
		a.SendCode(500, "transfers unavailable")
		return
	}
	batches, err := s.ListTransferBatches()
	if err != nil {
		a.SendCode(500, "transfer batches unavailable")
		return
	}
	queue, err := s.ListSyncQueue()
	if err != nil {
		a.SendCode(500, "sync queue unavailable")
		return
	}
	a.Send(webSnapshot{Devices: devices, Files: files, Sources: sources, Transfers: transfers, Batches: batches, Queue: queue, Playback: s.ListDevicePlayback()})
}

func (s *Service) WebDeviceFiles(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request webDeviceFilesRequest
	if !decodeParams(a, &request) || request.DeviceID == "" {
		a.SendCode(400, "invalid device")
		return
	}
	files, err := s.ListTracks(request.DeviceID)
	if err != nil {
		a.SendCode(500, "files unavailable")
		return
	}
	a.Send(map[string]any{"files": files})
}

func (s *Service) WebSetDevicePinned(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request webDevicePinRequest
	if !decodeParams(a, &request) || request.DeviceID == "" {
		a.SendCode(400, "invalid device")
		return
	}
	if err := s.SetDevicePinned(request.DeviceID, request.Pinned); err != nil {
		a.SendCode(404, "device not found")
		return
	}
	a.Send(map[string]any{"pinned": request.Pinned})
}

func (s *Service) WebHideDeviceFromList(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request webDeviceBatchRequest
	if !decodeParams(a, &request) || request.DeviceID == "" {
		a.SendCode(400, "invalid device")
		return
	}
	if err := s.HideDeviceFromList(request.DeviceID); err != nil {
		a.SendCode(404, "device not found")
		return
	}
	a.Send(map[string]any{"removed": true})
}

func (s *Service) WebRequestDeviceUpload(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request webDeviceUploadRequest
	if !decodeParams(a, &request) || request.DeviceID == "" || request.FileID == "" {
		a.SendCode(400, "invalid upload request")
		return
	}
	if err := s.RequestDeviceUpload(request.DeviceID, request.FileID); err != nil {
		a.SendCode(409, err.Error())
		return
	}
	a.Send(map[string]any{"accepted": true})
}

func (s *Service) WebRequestDeviceUploads(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request webDeviceBatchRequest
	if !decodeParams(a, &request) || request.DeviceID == "" {
		a.SendCode(400, "invalid device")
		return
	}
	client, err := s.downloadClient(request.DeviceID)
	if err != nil {
		a.SendCode(409, err.Error())
		return
	}
	tracks, err := s.uploadCandidates(request.DeviceID)
	if err != nil {
		a.SendCode(500, "device library unavailable")
		return
	}
	preview := DeviceSyncPreview{Count: len(tracks)}
	for _, track := range tracks {
		preview.Size += track.Size
	}
	if len(tracks) > 0 {
		preview.BatchID = newID("bt")
		if err := s.createTransferBatch(preview.BatchID, request.DeviceID, "upload", preview.Count, preview.Size); err != nil {
			a.SendCode(500, "upload batch unavailable")
			return
		}
		for _, track := range tracks {
			if _, err := s.createQueuedUploadTask(preview.BatchID, request.DeviceID, track); err != nil {
				continue
			}
			client.SendActionMsg(&ws.Action{Action: "v1.transfer.upload.request", Data: UploadRequestCommand{
				FileID: track.FileID, BatchID: preview.BatchID, BatchTotalFiles: preview.Count, BatchTotalBytes: preview.Size,
			}})
		}
	}
	a.Send(preview)
}

func (s *Service) WebPushDeviceMetadata(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request webDeviceBatchRequest
	if !decodeParams(a, &request) || request.DeviceID == "" {
		a.SendCode(400, "invalid device")
		return
	}
	client, err := s.downloadClient(request.DeviceID)
	if err != nil {
		a.SendCode(409, err.Error())
		return
	}
	files, err := s.deviceMetadataFiles(request.DeviceID)
	if err != nil {
		a.SendCode(500, "metadata unavailable")
		return
	}
	if client.GetKey(metadataCursorCapability).String() == "1" {
		if err := s.enqueueDeviceMetadata(request.DeviceID, files); err != nil {
			a.SendCode(500, "metadata unavailable")
			return
		}
		a.Send(DeviceSyncPreview{Count: len(files)})
		s.notifyMetadataCursor(client)
		return
	}
	views, err := s.metadataBatchViews(files)
	if err != nil {
		a.SendCode(500, "metadata unavailable")
		return
	}
	preview := DeviceSyncPreview{Count: len(views)}
	if len(views) > 0 {
		preview.BatchID = newID("bm")
		go s.broadcastMetadataBatch(client, preview.BatchID, views)
	}
	a.Send(preview)
}

func (s *Service) WebAddSyncQueueItem(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request SyncQueueAddRequest
	if !decodeParams(a, &request) {
		a.SendCode(400, "invalid sync queue item")
		return
	}
	item, err := s.AddSyncQueueItem(request)
	if err != nil {
		a.SendCode(400, err.Error())
		return
	}
	a.Send(map[string]any{"item": item})
}

func (s *Service) WebRemoveSyncQueueItem(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request webQueueItemRequest
	if !decodeParams(a, &request) {
		a.SendCode(400, "invalid sync queue item")
		return
	}
	if err := s.RemoveSyncQueueItem(request.QueueItemID); err != nil {
		a.SendCode(400, err.Error())
		return
	}
	a.Send(map[string]any{"removed": true})
}

func (s *Service) WebDispatchSyncQueue(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request TargetedSyncRequest
	if !decodeParams(a, &request) {
		a.SendCode(400, "invalid sync selection")
		return
	}
	offers, err := s.DispatchTargetedSyncQueue(request)
	if err != nil {
		a.SendCode(400, err.Error())
		return
	}
	a.Send(map[string]any{"offers": offers})
}
