package station

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

type ResourceMergeRequest struct {
	ServerFileID string               `json:"serverFileId"`
	Resources    []ResourceMergeInput `json:"resources"`
}

type ResourceMergeBatchRequest struct {
	Items []ResourceMergeRequest `json:"items"`
}

const (
	maxResourceMergeBatchItems     = 100
	maxResourceMergeBatchResources = 256
)

type ResourceMergeInput struct {
	Type         string `json:"type"`
	ResourceHash string `json:"resourceHash"`
	Data         string `json:"data"`
	Source       string `json:"source"`
	Format       string `json:"format"`
	Language     string `json:"language"`
}

func (s *Service) MergeDeviceResourceBatch(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var request ResourceMergeBatchRequest
	if !decodeParams(a, &request) || len(request.Items) == 0 || len(request.Items) > maxResourceMergeBatchItems {
		a.SendCode(400, "invalid resource merge batch")
		return
	}
	totalResources := 0
	for _, item := range request.Items {
		totalResources += len(item.Resources)
		if !serverFileIDPattern.MatchString(item.ServerFileID) || len(item.Resources) == 0 || len(item.Resources) > 32 || totalResources > maxResourceMergeBatchResources {
			a.SendCode(400, "invalid resource merge batch")
			return
		}
	}
	accepted := 0
	for _, item := range request.Items {
		count, err := s.mergeDeviceResources(deviceID, item)
		if err != nil {
			a.SendCode(400, err.Error())
			return
		}
		accepted += count
	}
	a.Send(map[string]any{
		"accepted":  accepted,
		"processed": len(request.Items),
	})
}

func (s *Service) mergeDeviceResources(deviceID string, request ResourceMergeRequest) (int, error) {
	accepted := 0
	for _, input := range request.Resources {
		resource, binary, err := mergedResource(request.ServerFileID, deviceID, input)
		if err != nil {
			return accepted, err
		}
		var existing MetadataResource
		err = s.db.Where("server_file_id = ? AND resource_hash = ?", request.ServerFileID, resource.ResourceHash).First(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return accepted, err
		}
		if len(binary) > 0 {
			extension := ".jpg"
			if resource.MIME == "image/png" {
				extension = ".png"
			}
			resource.StoragePath = filepath.Join(s.storageRoot, "artwork", resource.ResourceHash+extension)
			if _, statErr := os.Stat(resource.StoragePath); errors.Is(statErr, os.ErrNotExist) {
				if err := os.WriteFile(resource.StoragePath, binary, 0644); err != nil {
					return accepted, err
				}
			}
		}
		if err := s.db.Create(&resource).Error; err != nil {
			return accepted, err
		}
		accepted++
	}
	if accepted > 0 {
		s.broadcastMetadataUpdated(request.ServerFileID)
	}
	return accepted, nil
}

func mergedResource(serverFileID, deviceID string, input ResourceMergeInput) (MetadataResource, []byte, error) {
	kind := strings.ToLower(strings.TrimSpace(input.Type))
	if kind == "lrc" {
		kind = "lyrics"
	}
	if kind != "metadata" && kind != "lyrics" && kind != "cover" {
		return MetadataResource{}, nil, errors.New("unsupported resource type")
	}
	format := strings.TrimSpace(input.Format)
	language := strings.TrimSpace(input.Language)
	data := input.Data
	var binary []byte
	var mimeType string
	if kind == "lyrics" {
		data = normalizeLyrics(data)
	}
	if kind == "cover" {
		separator := strings.Index(data, ",")
		if !strings.HasPrefix(data, "data:image/") || separator < 0 || !strings.Contains(data[:separator], ";base64") {
			return MetadataResource{}, nil, errors.New("invalid cover data")
		}
		mimeType = strings.Split(strings.TrimPrefix(data[:separator], "data:"), ";")[0]
		if mimeType != "image/jpeg" && mimeType != "image/png" {
			return MetadataResource{}, nil, errors.New("unsupported cover mime")
		}
		var err error
		binary, err = base64.StdEncoding.DecodeString(data[separator+1:])
		if err != nil || int64(len(binary)) > maxCoverSize {
			return MetadataResource{}, nil, errors.New("invalid cover bytes")
		}
		format = mimeType
	}

	hash := sha256.New()
	_, _ = hash.Write([]byte(kind + "\x00"))
	if kind == "cover" {
		_, _ = hash.Write([]byte(mimeType + "\x00"))
		_, _ = hash.Write(binary)
	} else {
		_, _ = hash.Write([]byte(format + "\x00" + language + "\x00" + data))
	}
	actualHash := hex.EncodeToString(hash.Sum(nil))
	if actualHash != strings.ToLower(input.ResourceHash) {
		return MetadataResource{}, nil, errors.New("resource hash mismatch")
	}
	resourceData := data
	if kind == "cover" {
		resourceData = ""
	}
	return MetadataResource{
		ResourceID: newID("mr"), ServerFileID: serverFileID, Type: kind,
		ResourceHash: actualHash, Data: resourceData,
		MIME: mimeType, Source: "device:" + safePathSegment(deviceID), Format: format,
		Language: language, CreatedAt: time.Now(),
	}, binary, nil
}
