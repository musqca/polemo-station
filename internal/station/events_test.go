package station

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/wonli/aqi/ws"
)

func TestStationEventsOnlyReachWebDashboardClients(t *testing.T) {
	previousHub := ws.Hub
	t.Cleanup(func() { ws.Hub = previousHub })
	hub := ws.NewHubc()
	webClient := &ws.Client{
		Send:        make(chan []byte, 1),
		HttpRequest: &http.Request{URL: &url.URL{RawQuery: "role=station-web"}},
	}
	deviceClient := &ws.Client{
		Send:        make(chan []byte, 1),
		HttpRequest: &http.Request{URL: &url.URL{}},
	}
	hub.Clients[webClient] = struct{}{}
	hub.Clients[deviceClient] = struct{}{}

	service := &Service{}
	service.emitTransferChanged(TransferTask{
		TaskID: "tr_1", DeviceID: "device-a", FileID: "file-a",
		Direction: "download", TotalBytes: 200, TransferredBytes: 100, State: "downloading",
	})

	select {
	case message := <-webClient.Send:
		var action ws.Action
		if err := json.Unmarshal(message, &action); err != nil {
			t.Fatal(err)
		}
		if action.Action != stationEventAction {
			t.Fatalf("action = %q", action.Action)
		}
		data, err := json.Marshal(action.Data)
		if err != nil {
			t.Fatal(err)
		}
		var event StationEvent
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		if event.Transfer == nil || event.Transfer.TransferredBytes != 100 {
			t.Fatalf("unexpected event: %+v", event)
		}
	default:
		t.Fatal("web dashboard did not receive station event")
	}

	select {
	case <-deviceClient.Send:
		t.Fatal("native device received dashboard-only event")
	default:
	}
}

func TestTransferProgressEventsAreCoalescedButTerminalStateIsImmediate(t *testing.T) {
	previousHub := ws.Hub
	t.Cleanup(func() { ws.Hub = previousHub })
	hub := ws.NewHubc()
	webClient := &ws.Client{
		Send:        make(chan []byte, 3),
		HttpRequest: &http.Request{URL: &url.URL{RawQuery: "role=station-web"}},
	}
	hub.Clients[webClient] = struct{}{}
	service := NewService(nil, "", t.TempDir())

	progress := TransferTask{TaskID: "tr-progress", DeviceID: "device-a", State: "uploading"}
	service.emitTransferChanged(progress)
	progress.TransferredBytes = 10
	service.emitTransferChanged(progress)
	progress.State = "completed"
	service.emitTransferChanged(progress)

	if messages := len(webClient.Send); messages != 2 {
		t.Fatalf("received %d events, want initial progress and terminal state", messages)
	}
	<-webClient.Send
	var action ws.Action
	if err := json.Unmarshal(<-webClient.Send, &action); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(action.Data)
	if err != nil {
		t.Fatal(err)
	}
	var event StationEvent
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Transfer == nil || event.Transfer.State != "completed" {
		t.Fatalf("terminal event = %+v, want completed transfer", event.Transfer)
	}
}

func TestTransferEventsDoNotBlockAFullDashboardQueue(t *testing.T) {
	previousHub := ws.Hub
	t.Cleanup(func() { ws.Hub = previousHub })
	hub := ws.NewHubc()
	webClient := &ws.Client{
		Send:        make(chan []byte, 1),
		HttpRequest: &http.Request{URL: &url.URL{RawQuery: "role=station-web"}},
	}
	hub.Clients[webClient] = struct{}{}
	webClient.Send <- []byte("queued action")
	service := NewService(nil, "", t.TempDir())

	done := make(chan struct{})
	go func() {
		service.emitTransferChanged(TransferTask{TaskID: "tr-full", DeviceID: "device-a", State: "uploading"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("transfer event blocked on a full dashboard queue")
	}
}
