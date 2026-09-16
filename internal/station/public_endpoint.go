package station

import (
	"errors"
	"net/url"
	"strings"
)

func parsePublicBaseURL(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", errors.New("public base URL is required")
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("public base URL must be an http:// or https:// URL without credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	base := strings.TrimRight(u.String(), "/")
	websocket := *u
	if websocket.Scheme == "https" {
		websocket.Scheme = "wss"
	} else {
		websocket.Scheme = "ws"
	}
	websocket.Path = strings.TrimRight(websocket.Path, "/") + "/ws"
	websocket.RawPath = ""
	websocket.RawQuery = ""
	websocket.Fragment = ""
	return base, websocket.String(), nil
}

func (s *Service) publicEndpointValue() (string, error) {
	if s.publicWebSocketURL == "" {
		return "", errors.New("public base URL is not configured")
	}
	return s.publicWebSocketURL, nil
}

// PublicBaseURL returns the normalized configured URL for the embedded web
// client. An empty value means the optional public QR endpoint is not set.
func (s *Service) PublicBaseURL() string {
	return s.publicBaseURL
}
