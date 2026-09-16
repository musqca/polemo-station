package station

import (
	"strings"
	"time"

	"github.com/wonli/aqi/ws"
)

// DevicePlayback is live UI state only. It deliberately is not persisted: a
// disconnected device must never look as though it is still playing.
type DevicePlayback struct {
	DeviceID     string    `json:"deviceId"`
	FileID       string    `json:"fileId,omitempty"`
	ServerFileID string    `json:"serverFileId,omitempty"`
	FileName     string    `json:"fileName,omitempty"`
	Title        string    `json:"title,omitempty"`
	Artist       string    `json:"artist,omitempty"`
	Album        string    `json:"album,omitempty"`
	Playing      bool      `json:"playing"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type devicePlaybackRequest struct {
	FileID       string `json:"fileId"`
	ServerFileID string `json:"serverFileId"`
	FileName     string `json:"fileName"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	Album        string `json:"album"`
	Playing      bool   `json:"playing"`
}

type devicePlaybackControlRequest struct {
	DeviceID string `json:"deviceId"`
	Action   string `json:"action"`
}

type webPlaybackRequest struct {
	ServerFileID string `json:"serverFileId"`
	FileName     string `json:"fileName"`
	Format       string `json:"format"`
	Size         int64  `json:"size"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	Album        string `json:"album"`
	Playing      bool   `json:"playing"`
}

// DevicePlaybackControl forwards a playback command from the Station web UI
// to a connected native device.
func (s *Service) DevicePlaybackControl(a *ws.Context) {
	if a == nil || !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request devicePlaybackControlRequest
	if !decodeParams(a, &request) || request.DeviceID == "" ||
		(request.Action != "previous" && request.Action != "toggle" && request.Action != "next") {
		a.SendCode(400, "invalid playback control")
		return
	}
	client, err := s.downloadClient(request.DeviceID)
	if err != nil {
		a.SendCode(409, err.Error())
		return
	}
	client.SendActionMsg(&ws.Action{Action: "v1.device.playback.control", Data: request})
	a.Send(map[string]any{"accepted": true})
}

// WebPlaybackControl broadcasts a playback command from a native device to
// every authenticated Station web player connected to this Station.
func (s *Service) WebPlaybackControl(a *ws.Context) {
	if !s.canControlWebPlayback(a) {
		a.SendCode(403, "superadmin app authorization required")
		return
	}
	if clientDeviceID(a) == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var request devicePlaybackControlRequest
	if !decodeParams(a, &request) ||
		(request.Action != "previous" && request.Action != "toggle" && request.Action != "next") {
		a.SendCode(400, "invalid playback control")
		return
	}
	s.dispatchWebPlaybackControl(request)
	a.Send(map[string]any{"accepted": true})
}

// WebPlayback shares the browser player's current track with native devices
// so the Station connection page can act as a remote control with context.
func (s *Service) WebPlayback(a *ws.Context) {
	if a == nil || !isStationWeb(a) {
		a.SendCode(403, "station web required")
		return
	}
	var request webPlaybackRequest
	if !decodeParams(a, &request) || strings.TrimSpace(request.Title) == "" {
		a.SendCode(400, "invalid playback state")
		return
	}
	request.ServerFileID = strings.TrimSpace(request.ServerFileID)
	request.FileName = strings.TrimSpace(request.FileName)
	request.Title = strings.TrimSpace(request.Title)
	request.Artist = strings.TrimSpace(request.Artist)
	request.Album = strings.TrimSpace(request.Album)
	s.webPlaybackMu.Lock()
	state := request
	s.webPlayback = &state
	s.webPlaybackMu.Unlock()
	message := &ws.Action{Action: "v1.station.web.playback", Data: request}
	if ws.Hub != nil {
		for client := range ws.Hub.Clients {
			if client == nil || client.HttpRequest == nil ||
				client.HttpRequest.URL.Query().Get("role") == "station-web" {
				continue
			}
			if client.GetKey("stationDeviceId") != nil {
				client.SendActionMsg(message)
			}
		}
	}
	a.Send(map[string]any{"accepted": true})
}

// WebPlaybackState lets a native device recover the last browser-player
// state after connecting, instead of relying on an event that may have been
// emitted before the device was online.
func (s *Service) WebPlaybackState(a *ws.Context) {
	if clientDeviceID(a) == "" {
		a.SendCode(401, "device hello required")
		return
	}
	s.webPlaybackMu.RLock()
	state := s.webPlayback
	if state == nil {
		state = &webPlaybackRequest{}
	}
	response := *state
	s.webPlaybackMu.RUnlock()
	a.Send(response)
}

func (s *Service) sendWebPlaybackState(client *ws.Client) {
	if client == nil {
		return
	}
	s.webPlaybackMu.RLock()
	state := s.webPlayback
	if state == nil {
		state = &webPlaybackRequest{}
	}
	response := *state
	s.webPlaybackMu.RUnlock()
	client.SendActionMsg(&ws.Action{Action: "v1.station.web.playback", Data: response})
}

func (s *Service) DevicePlayback(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var request devicePlaybackRequest
	if !decodeParams(a, &request) {
		a.SendCode(400, "invalid playback state")
		return
	}
	state := DevicePlayback{
		DeviceID: deviceID, FileID: strings.TrimSpace(request.FileID), ServerFileID: strings.TrimSpace(request.ServerFileID),
		FileName: strings.TrimSpace(request.FileName), Title: strings.TrimSpace(request.Title), Artist: strings.TrimSpace(request.Artist),
		Album: strings.TrimSpace(request.Album), Playing: request.Playing, UpdatedAt: time.Now(),
	}
	if state.FileID == "" && state.ServerFileID == "" {
		s.nowPlaying.Delete(deviceID)
	} else {
		s.nowPlaying.Store(deviceID, state)
	}
	s.emitPlaybackChanged(state)
	a.Send(map[string]any{"accepted": true})
}

func (s *Service) ListDevicePlayback() []DevicePlayback {
	states := make([]DevicePlayback, 0)
	s.nowPlaying.Range(func(_, value any) bool {
		if state, ok := value.(DevicePlayback); ok {
			states = append(states, state)
		}
		return true
	})
	return states
}

func (s *Service) canControlWebPlayback(a *ws.Context) bool {
	identity, err := s.clientIdentity(a)
	return err == nil && identity.Kind == "app" && identity.Account.Role == AuthorizationRoleSuper
}

func (s *Service) dispatchWebPlaybackControl(request devicePlaybackControlRequest) {
	if ws.Hub == nil {
		return
	}
	message := &ws.Action{Action: "v1.station.web.playback.control", Data: request}
	for client := range ws.Hub.Clients {
		if client == nil || client.HttpRequest == nil || client.HttpRequest.URL.Query().Get("role") != "station-web" {
			continue
		}
		client.SendActionMsg(message)
	}
}
