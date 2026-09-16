package fanarttv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAlbumCoverUsesPersonalKeyAndDownloadsArtwork(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("client-key") != "personal-key" {
			t.Fatalf("client-key = %q, want personal-key", request.Header.Get("client-key"))
		}
		switch request.URL.Path {
		case "/music/albums/group-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"albums":[{"release_group_id":"group-1","albumcover":[{"url":"` + server.URL + `/cover.jpg"}]}]}`))
		case "/cover.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	data, mime, err := NewClient(WithBaseURL(server.URL), WithAPIKey("personal-key")).AlbumCover(context.Background(), "group-1")
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/jpeg" || len(data) == 0 {
		t.Fatalf("cover = %d bytes %q, want JPEG", len(data), mime)
	}
}
