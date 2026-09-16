package station

import (
	"encoding/base64"
	"os"
	"time"

	"github.com/wonli/aqi/ws"
)

const stationEventAction = "v1.station.event"

// Transfer progress is visual feedback, unlike playback changes. Keep it
// infrequent enough that a busy upload cannot fill a dashboard WebSocket's
// outbound queue and delay a song-change notification.
const transferProgressEventInterval = time.Second

type StationEvent struct {
	Libraries  []deviceLibrary   `json:"libraries,omitempty"`
	Error      string            `json:"error,omitempty"`
	Kind       string            `json:"kind"`
	DeviceID   string            `json:"deviceId,omitempty"`
	Transfer   *TransferTask     `json:"transfer,omitempty"`
	ServerFile *ServerFileChange `json:"serverFile,omitempty"`
	Queue      []SyncQueueItem   `json:"queue,omitempty"`
	Playback   *DevicePlayback   `json:"playback,omitempty"`
}

type ServerFileChange struct {
	ServerFileID string `json:"serverFileId"`
	Hash         string `json:"hash"`
}

func (s *Service) broadcastEvent(event StationEvent) {
	if ws.Hub == nil {
		return
	}
	message := &ws.Action{Action: stationEventAction, Data: event}
	// Station events are UI invalidations. Native device connections receive
	// transfer commands separately and should not be flooded with dashboard
	// progress messages.
	for client := range ws.Hub.Clients {
		if client == nil || client.HttpRequest == nil || client.HttpRequest.URL.Query().Get("role") != "station-web" {
			continue
		}
		client.SendActionMsg(message)
	}
}

func (s *Service) emitDeviceChanged(deviceID string) {
	s.broadcastEvent(StationEvent{Kind: "device", DeviceID: deviceID})
}

func (s *Service) emitLibraryChanged(deviceID string) {
	if value, ok := s.onlineDevices.Load(deviceID); ok {
		if client, ok := value.(*ws.Client); ok {
			s.notifyMetadataCursor(client)
		}
	}
	s.broadcastEvent(StationEvent{Kind: "library", DeviceID: deviceID})
}

func (s *Service) emitSyncQueueChanged() {
	queue, err := s.ListSyncQueue()
	if err != nil {
		return
	}
	s.broadcastEvent(StationEvent{Kind: "sync_queue", Queue: queue})
}

func (s *Service) emitTransferChanged(task TransferTask) {
	if s.shouldBroadcastTransferEvent(task) {
		s.broadcastTransferEvent(StationEvent{Kind: "transfer", DeviceID: task.DeviceID, Transfer: &task})
	}
	s.updateTransferBatch(task.BatchID)
}

// broadcastTransferEvent never waits on a dashboard's outbound WebSocket
// queue. Progress is disposable because the next snapshot is authoritative;
// blocking here, however, delays state-changing actions such as playback.
func (s *Service) broadcastTransferEvent(event StationEvent) {
	if ws.Hub == nil {
		return
	}
	message := (&ws.Action{Action: stationEventAction, Data: event}).Encode()
	for client := range ws.Hub.Clients {
		if client == nil || client.HttpRequest == nil || client.HttpRequest.URL.Query().Get("role") != "station-web" {
			continue
		}
		select {
		case client.Send <- message:
		default:
		}
	}
}

func (s *Service) emitTransferBatchChanged(deviceID string) {
	s.broadcastEvent(StationEvent{Kind: "transfer_batch", DeviceID: deviceID})
}

func (s *Service) shouldBroadcastTransferEvent(task TransferTask) bool {
	terminal := task.State == "completed" || task.State == "failed" || task.State == "cancelled"
	now := time.Now()

	s.transferEventMu.Lock()
	defer s.transferEventMu.Unlock()
	if s.transferEventAt == nil {
		s.transferEventAt = make(map[string]time.Time)
	}
	if terminal {
		delete(s.transferEventAt, task.TaskID)
		return true
	}
	if last, ok := s.transferEventAt[task.TaskID]; ok && now.Sub(last) < transferProgressEventInterval {
		return false
	}
	s.transferEventAt[task.TaskID] = now
	return true
}

func (s *Service) emitPlaybackChanged(playback DevicePlayback) {
	s.broadcastEvent(StationEvent{Kind: "playback", DeviceID: playback.DeviceID, Playback: &playback})
}

func (s *Service) emitServerFileChanged(file ServerFile) {
	s.broadcastEvent(StationEvent{
		Kind: "server_file",
		ServerFile: &ServerFileChange{
			ServerFileID: file.ServerFileID,
			Hash:         file.Hash,
		},
	})
}

func (s *Service) broadcastMetadataUpdated(serverFileID string) {
	// New clients only need an invalidation, not a materialized per-song payload.
	var legacy *ws.Action
	s.onlineDevices.Range(func(_, value any) bool {
		client, ok := value.(*ws.Client)
		if !ok || client == nil {
			return true
		}
		if client.GetKey(metadataCursorCapability).String() == "1" {
			s.notifyMetadataCursor(client)
			return true
		}
		if legacy == nil {
			view, err := s.metadataPushView(serverFileID)
			if err != nil {
				return true
			}
			legacy = &ws.Action{Action: "v1.metadata.updated", Data: view}
		}
		client.SendActionMsg(legacy)
		return true
	})
}

func (s *Service) metadataPushView(serverFileID string) (ServerFileView, error) {
	view, err := s.GetServerFile(serverFileID)
	if err != nil {
		return ServerFileView{}, err
	}
	for index := range view.Resources {
		resource := &view.Resources[index]
		if resource.Type != "cover" || resource.Deleted || resource.StoragePath == "" {
			continue
		}
		if bytes, readErr := os.ReadFile(resource.StoragePath); readErr == nil {
			resource.Data = "data:" + resource.MIME + ";base64," + base64.StdEncoding.EncodeToString(bytes)
		}
	}
	return view, nil
}
