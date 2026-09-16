package station

import "testing"

func TestParsePublicBaseURL(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"https://station.onejss.com/v1/", "https://station.onejss.com/v1"},
		{"http://127.0.0.1:20156", "http://127.0.0.1:20156"},
	}
	for _, test := range tests {
		got, _, err := parsePublicBaseURL(test.input)
		if err != nil || got != test.want {
			t.Fatalf("parsePublicBaseURL(%q) = %q, %v", test.input, got, err)
		}
	}
	if _, _, err := parsePublicBaseURL("https://station.onejss.com/v1?x=1"); err == nil {
		t.Fatal("query URL unexpectedly accepted")
	}
}

func TestServiceStoresNormalizedPublicEndpoints(t *testing.T) {
	service := &Service{}
	if err := service.SetPublicBaseURL("https://station.onejss.com/v1/"); err != nil {
		t.Fatal(err)
	}
	if got := service.PublicBaseURL(); got != "https://station.onejss.com/v1" {
		t.Fatalf("PublicBaseURL() = %q", got)
	}
	if got, err := service.publicEndpointValue(); err != nil || got != "wss://station.onejss.com/v1/ws" {
		t.Fatalf("publicEndpointValue() = %q, %v", got, err)
	}

	if err := service.SetPublicBaseURL("http://192.168.31.129:20156"); err != nil {
		t.Fatal(err)
	}
	if got, err := service.publicEndpointValue(); err != nil || got != "ws://192.168.31.129:20156/ws" {
		t.Fatalf("publicEndpointValue() = %q, %v", got, err)
	}
}

func TestWebsocketEndpointFromBase(t *testing.T) {
	_, got, err := parsePublicBaseURL("https://station.onejss.com/v1")
	if err != nil || got != "wss://station.onejss.com/v1/ws" {
		t.Fatalf("websocket endpoint = %q", got)
	}
}
