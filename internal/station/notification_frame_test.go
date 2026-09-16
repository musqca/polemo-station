package station

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	gws "github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/wonli/aqi/ws"
)

// Exercise the real socket writer: decoding the queued bytes alone misses an
// invalid continuation opcode, which causes native and browser clients to close.
func TestNotificationsAreStandaloneTextFrames(t *testing.T) {
	previousHub := ws.Hub
	t.Cleanup(func() { ws.Hub = previousHub })
	hub := ws.NewHubc()

	for _, kind := range []string{"library", "metadata", "upload-progress"} {
		t.Run(kind, func(t *testing.T) {
			server, peer := net.Pipe()
			client := &ws.Client{
				Conn: server, Send: make(chan []byte, 4),
				HttpRequest: &http.Request{URL: &url.URL{RawQuery: "role=station-web"}},
			}
			client.SetKey(metadataCursorCapability, "1")
			wake := &metadataCursorWake{}
			client.SetKey(metadataCursorWakeKey, wake)
			hub.Clients[client] = struct{}{}
			defer delete(hub.Clients, client)
			service := &Service{}
			service.onlineDevices.Store("phone", client)
			writerDone := make(chan struct{})
			go func() {
				defer close(writerDone)
				client.Write()
			}()
			defer func() {
				peer.Close()
				client.Disconnect()
				// A manually constructed client has no lifecycle context. Wake the
				// writer after closing the connection so its write returns and the
				// goroutine can finish deterministically.
				select {
				case client.Send <- []byte{}:
				default:
				}
				select {
				case <-writerDone:
				case <-time.After(time.Second):
					t.Error("socket writer did not stop")
				}
			}()
			if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				want := "v1.metadata.sync.available"
				switch kind {
				case "library":
					// Native connections do not receive dashboard invalidations.
					client.HttpRequest.URL.RawQuery = ""
					service.emitLibraryChanged("phone")
				case "metadata":
					service.broadcastMetadataUpdated("song")
				case "upload-progress":
					want = stationEventAction
					service.broadcastTransferEvent(StationEvent{Kind: "transfer"})
				}
				payload, op, err := wsutil.ReadServerData(peer)
				if err != nil {
					t.Fatalf("client rejected notification frame: %v", err)
				}
				if op != gws.OpText {
					t.Fatalf("notification opcode = %v, want text", op)
				}
				var action ws.Action
				if err := json.Unmarshal(payload, &action); err != nil {
					t.Fatal(err)
				}
				if action.Action != want {
					t.Fatalf("action = %q, want %q", action.Action, want)
				}
				wake.pending.Store(false) // The next metadata pull acknowledges it.
			}
		})
	}
}
