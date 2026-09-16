package station

import (
	"strings"
	"time"

	"github.com/wonli/aqi/ws"
)

type deviceHello struct {
	Libraries    *[]deviceLibrary `json:"libraries,omitempty"`
	DeviceID     string           `json:"deviceId"`
	Name         string           `json:"name"`
	Platform     string           `json:"platform"`
	AppVersion   string           `json:"appVersion"`
	Capabilities map[string]any   `json:"capabilities"`
}

func (s *Service) DeviceHello(a *ws.Context) {
	var hello deviceHello
	if !decodeParams(a, &hello) || strings.TrimSpace(hello.DeviceID) == "" {
		a.SendCode(400, "invalid device")
		return
	}
	deviceID := strings.TrimSpace(hello.DeviceID)
	a.Client.SetKey("stationDeviceId", deviceID)
	a.Client.SetKey("libraryManagement", "")
	if hello.Capabilities["libraryManagement"] == true {
		a.Client.SetKey("libraryManagement", "1")
	}
	a.Client.SetKey(deviceLibrarySnapshotKey, nil)
	if hello.Capabilities["libraryManagement"] == true && hello.Libraries != nil {
		cacheDeviceLibraries(a.Client, deviceLibraryReply{Libraries: *hello.Libraries})
	}
	a.Client.SetKey(metadataCursorWakeKey, &metadataCursorWake{})
	a.Client.SetKey(metadataCursorCapability, "")
	if hello.Capabilities[metadataCursorCapability] == true {
		a.Client.SetKey(metadataCursorCapability, "1")
	}
	s.onlineDevices.Store(deviceID, a.Client)
	s.db.Where("device_id = ?", deviceID).Assign(Device{
		Name: hello.Name, Platform: hello.Platform, AppVersion: hello.AppVersion,
		IP: a.Client.IpAddress, Online: true, Hidden: false, LastSeen: time.Now(),
	}).FirstOrCreate(&Device{DeviceID: deviceID})
	a.Send(map[string]any{"deviceId": deviceID, "metadataCursor": true})
	// Send the current browser-player snapshot immediately after hello. The
	// device connection should not need to issue a second state request, and
	// the push also clears a stale card when the browser is idle.
	s.sendWebPlaybackState(a.Client)
	s.emitDeviceChanged(deviceID)
	if a.Client.GetKey(metadataCursorCapability).String() != "1" {
		go s.SyncDeviceMetadata(deviceID)
	}
	// Recover tasks left in downloading state by a Station restart or an
	// unobserved socket close before resuming only the current task.
	s.pauseDeviceDownloads(deviceID)
	s.DispatchPendingSyncOffers(deviceID)
	s.DispatchPendingTransfers(deviceID)
}
