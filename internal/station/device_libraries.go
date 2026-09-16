package station

import (
	"errors"
	"fmt"
	"time"

	"github.com/wonli/aqi/ws"
)

type deviceLibraryRequest struct {
	Refresh   bool   `json:"refresh,omitempty"`
	DeviceID  string `json:"deviceId"`
	Operation string `json:"operation"`
	LibraryID string `json:"libraryId,omitempty"`
	Name      string `json:"name,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}
type deviceLibraryReply struct {
	RequestID string          `json:"requestId"`
	Error     string          `json:"error,omitempty"`
	Libraries []deviceLibrary `json:"libraries"`
	Library   *deviceLibrary  `json:"library,omitempty"`
}
type deviceLibrary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Writable bool   `json:"writable"`
}
type pendingLibraryRequest struct {
	client *ws.Client
	reply  chan deviceLibraryReply
}

// A snapshot belongs to one live socket, never to a durable device ID.
// Reconnection therefore cannot reuse a previous installation's libraries.
const deviceLibrarySnapshotKey = "deviceLibrarySnapshot"

func cacheDeviceLibraries(client *ws.Client, reply deviceLibraryReply) {
	if reply.Error != "" {
		return
	}
	reply.RequestID = ""
	reply.Library = nil
	client.SetKey(deviceLibrarySnapshotKey, reply)
}

func (s *Service) requestDeviceLibraries(request deviceLibraryRequest) (deviceLibraryReply, error) {
	value, ok := s.onlineDevices.Load(request.DeviceID)
	client, valid := value.(*ws.Client)
	if !ok || !valid || client == nil {
		return deviceLibraryReply{}, errors.New("deviceOffline")
	}
	if client.GetKey("libraryManagement").String() != "1" {
		return deviceLibraryReply{}, errors.New("appUpdateRequired")
	}
	request.RequestID = newID("lr")
	pending := &pendingLibraryRequest{client: client, reply: make(chan deviceLibraryReply, 1)}
	s.libraryRequests.Store(request.RequestID, pending)
	defer s.libraryRequests.Delete(request.RequestID)
	client.SendActionMsg(&ws.Action{Action: "v1.device.libraries", Data: request})
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case reply := <-pending.reply:
		if reply.Error != "" {
			return reply, errors.New(reply.Error)
		}
		return reply, nil
	case <-client.Context().Done():
		return deviceLibraryReply{}, errors.New("deviceOffline")
	case <-timer.C:
		return deviceLibraryReply{}, errors.New("deviceTimeout")
	}
}
func (s *Service) WebDeviceLibraries(a *ws.Context) {
	if !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request deviceLibraryRequest
	if !decodeParams(a, &request) || request.DeviceID == "" {
		a.SendCode(400, "invalid library request")
		return
	}
	switch request.Operation {
	case "list", "create", "rename", "delete":
	default:
		a.SendCode(400, "invalid library operation")
		return
	}
	if value, ok := s.onlineDevices.Load(request.DeviceID); ok {
		if client, valid := value.(*ws.Client); valid && client != nil {
			if request.Operation == "list" && !request.Refresh {
				if snapshot, found := client.GetKey(deviceLibrarySnapshotKey).Raw().(deviceLibraryReply); found {
					a.Send(snapshot)
					return
				}
			} else if request.Operation != "list" {
				client.SetKey(deviceLibrarySnapshotKey, nil)
			}
		}
	}
	reply, err := s.requestDeviceLibraries(request)
	if err != nil {
		a.SendCode(400, err.Error())
		return
	}
	a.Send(reply)
}
func (s *Service) DeviceLibrariesResponse(a *ws.Context) {
	var reply deviceLibraryReply
	if clientDeviceID(a) == "" || !decodeParams(a, &reply) {
		a.SendCode(400, "invalid library response")
		return
	}
	value, ok := s.libraryRequests.Load(reply.RequestID)
	if !ok {
		a.Send(map[string]bool{"expired": true})
		return
	}
	pending := value.(*pendingLibraryRequest)
	if pending.client != a.Client {
		a.SendCode(403, "unexpected device")
		return
	}
	cacheDeviceLibraries(a.Client, reply)
	select {
	case pending.reply <- reply:
	default:
	}
	a.Send(map[string]bool{"accepted": true})
}

type TargetedSyncRequest struct {
	TargetLibraries map[string]string `json:"targetLibraries"`
	ServerFileIDs   []string          `json:"serverFileIds"`
}

// Dispatch only the selection shown in this browser. Successful devices are
// removed immediately, so a later device failure does not replay their queue.
func (s *Service) DispatchTargetedSyncQueue(request TargetedSyncRequest) ([]SyncOffer, error) {
	if len(request.TargetLibraries) == 0 || len(request.TargetLibraries) > 32 || len(request.ServerFileIDs) == 0 || len(request.ServerFileIDs) > 500 {
		return nil, errors.New("invalid sync selection")
	}
	// Validate every target against the live phone before dispatching any offers.
	for deviceID, libraryID := range request.TargetLibraries {
		if libraryID == "" {
			return nil, errors.New("libraryMissing")
		}
		reply, err := s.requestDeviceLibraries(deviceLibraryRequest{DeviceID: deviceID, Operation: "list"})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", deviceID, err)
		}
		found := false
		for _, library := range reply.Libraries {
			if library.ID == libraryID && library.Writable {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("libraryMissing")
		}
	}
	offers := []SyncOffer{}
	for deviceID, libraryID := range request.TargetLibraries {
		var items []SyncQueueItem
		if err := s.db.Where("device_id = ? AND server_file_id IN ?", deviceID, request.ServerFileIDs).Find(&items).Error; err != nil {
			return offers, err
		}
		if len(items) == 0 {
			continue
		}
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ServerFileID)
		}
		created, err := s.CreateSyncOffers(CreateSyncOfferRequest{DeviceIDs: []string{deviceID}, ServerFileIDs: ids, TargetLibraryID: libraryID})
		if err != nil {
			return offers, err
		}
		offers = append(offers, created...)
		if err = s.db.Where("queue_item_id IN ?", queueIDs(items)).Delete(&SyncQueueItem{}).Error; err != nil {
			return offers, err
		}
		s.emitSyncQueueChanged()
	}
	return offers, nil
}

// Phone database changes are pushed independently of playback and track snapshots.
func (s *Service) DeviceLibrariesChanged(a *ws.Context) {
	deviceID := clientDeviceID(a)
	current, ok := s.onlineDevices.Load(deviceID)
	var reply deviceLibraryReply
	if deviceID == "" || !ok || current != a.Client || !decodeParams(a, &reply) {
		a.SendCode(400, "invalid library update")
		return
	}
	cacheDeviceLibraries(a.Client, reply)
	s.broadcastEvent(StationEvent{Kind: "device_libraries", DeviceID: deviceID, Libraries: reply.Libraries})
	a.Send(map[string]bool{"accepted": true})
}
