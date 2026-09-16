package station

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

const metadataCursorCapability = "metadataCursor"
const metadataCursorBatchFiles = 32
const metadataCursorBatchBytes = 512 * 1024

type metadataCursorWake struct{ pending atomic.Bool }

const metadataCursorWakeKey = "metadataCursorWake"

type metadataCursorRequest struct {
	Cursor string `json:"cursor"`
}
type metadataCursorItem struct {
	*ServerFileView
	ServerFileID string `json:"serverFileId"`
	MetadataURL  string `json:"metadataUrl,omitempty"`
}

type metadataCursorReply struct {
	Total   int64                `json:"total"`
	Cursor  string               `json:"cursor"`
	Items   []metadataCursorItem `json:"items"`
	HasMore bool                 `json:"hasMore"`
	Reset   bool                 `json:"reset"`
}
type metadataCursorChange struct {
	Cursor       int64
	ServerFileID string
}

func (s *Service) PullMetadataCursor(a *ws.Context) {
	deviceID := clientDeviceID(a)
	var request metadataCursorRequest
	if deviceID == "" || !decodeParams(a, &request) {
		a.SendCode(400, "invalid metadata cursor request")
		return
	}
	if wake, ok := a.Client.GetKey(metadataCursorWakeKey).Raw().(*metadataCursorWake); ok {
		wake.pending.Store(false)
	}
	reply, err := s.metadataCursorBatch(deviceID, request.Cursor)
	if err != nil {
		a.SendCode(500, "metadata batch unavailable")
		return
	}
	a.Send(reply)
}

func (s *Service) metadataCursorBatch(deviceID, cursor string) (metadataCursorReply, error) {
	reply := metadataCursorReply{Items: make([]metadataCursorItem, 0)}
	var epoch string
	if err := s.db.Table("metadata_sync_state").Where("id = 1").Pluck("epoch", &epoch).Error; err != nil {
		return reply, err
	}
	var high int64
	if err := s.db.Table("metadata_changes").Select("COALESCE(MAX(cursor),0)").Scan(&high).Error; err != nil {
		return reply, err
	}
	after := int64(0)
	if cursor != "" {
		prefix, number, ok := strings.Cut(cursor, ":")
		value, err := strconv.ParseInt(number, 10, 64)
		if !ok || err != nil || prefix != epoch || value < 0 || value > high {
			reply.Reset = true
		} else {
			after = value
		}
	}
	var changes []metadataCursorChange
	// Collapse repeated edits to the same song within the committed prefix.
	base := s.db.Table("metadata_changes AS changes").
		Where("changes.cursor > ? AND changes.cursor <= ? AND (changes.device_id = '' OR changes.device_id = ?)", after, high, deviceID).
		Where("EXISTS (SELECT 1 FROM tracks WHERE tracks.device_id = ? AND tracks.server_file_id = changes.server_file_id)", deviceID)
	if err := base.Session(&gorm.Session{}).Select("COUNT(DISTINCT changes.server_file_id)").Scan(&reply.Total).Error; err != nil {
		return reply, err
	}
	err := base.Session(&gorm.Session{}).Select("changes.server_file_id, MAX(changes.cursor) AS cursor").
		Group("changes.server_file_id").Order("MAX(changes.cursor) ASC").Limit(metadataCursorBatchFiles + 1).Scan(&changes).Error
	if err != nil {
		return reply, err
	}
	bytes := 0
	for index, change := range changes {
		if index == metadataCursorBatchFiles {
			reply.HasMore = true
			break
		}
		// GetServerFile leaves artwork as authenticated resource URLs. Never call
		// metadataPushView here: its Base64 covers would fill the control socket.
		view, err := s.GetServerFile(change.ServerFileID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			after = change.Cursor
			continue
		}
		if err != nil {
			return reply, err
		}
		for i := range view.Resources {
			if view.Resources[i].Type == "cover" {
				view.Resources[i].Data = ""
			}
		}
		item := metadataCursorItem{ServerFileView: &view, ServerFileID: view.ServerFileID}
		encoded, err := json.Marshal(item)
		if err != nil {
			return reply, err
		}
		if len(encoded) > metadataCursorBatchBytes-1024 {
			// A song with a large resource history uses the existing HTTP
			// endpoint too; even one oversized song cannot monopolize WS.
			item = metadataCursorItem{ServerFileID: view.ServerFileID, MetadataURL: "/api/station/files/" + view.ServerFileID}
			encoded, err = json.Marshal(item)
			if err != nil {
				return reply, err
			}
		}
		if len(reply.Items) > 0 && bytes+len(encoded) > metadataCursorBatchBytes-1024 {
			reply.HasMore = true
			break
		}
		reply.Items = append(reply.Items, item)
		bytes += len(encoded)
		after = change.Cursor
	}
	if !reply.HasMore {
		after = high
	}
	reply.Cursor = epoch + ":" + strconv.FormatInt(after, 10)
	return reply, nil
}

// Coalesce invalidations. The client drains pages at its own pace; control
// messages never sit behind thousands of unsolicited per-song payloads.
func (s *Service) notifyMetadataCursor(client *ws.Client) {
	if client == nil || client.GetKey(metadataCursorCapability).String() != "1" {
		return
	}
	wake, _ := client.GetKey(metadataCursorWakeKey).Raw().(*metadataCursorWake)
	if wake != nil && wake.pending.Swap(true) {
		return
	}
	message := (&ws.Action{Action: "v1.metadata.sync.available", Data: map[string]bool{"available": true}}).Encode()
	select {
	case client.Send <- message:
	default:
		if wake != nil {
			wake.pending.Store(false)
		}
	}
}

// Explicit metadata pulls and completed downloads need a fresh association
// entry even when this phone had already passed the song's original change.
func (s *Service) enqueueDeviceMetadata(deviceID string, files []ServerFile) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, file := range files {
			if err := tx.Exec("INSERT INTO metadata_changes(server_file_id,device_id) VALUES (?,?)", file.ServerFileID, deviceID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
