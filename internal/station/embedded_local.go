package station

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bogem/id3v2/v2"
	"github.com/go-flac/flacpicture"
	"github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
	"gorm.io/gorm"
)

type localEmbeddedMetadata struct {
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Track       int
	Disc        int
	Lyrics      []localEmbeddedText
	Pictures    []localEmbeddedPicture
}

type localEmbeddedText struct {
	Data     string
	Language string
	Source   string
}

type localEmbeddedPicture struct {
	Data []byte
	MIME string
}

func (s *Service) importLocalEmbeddedMetadata(path, serverFileID string) {
	defer func() { _, _ = s.EnqueueScrape([]string{serverFileID}) }()
	metadata := readLocalEmbeddedMetadata(path)
	if sidecar := readSidecarLyrics(path); sidecar != "" {
		metadata.Lyrics = append([]localEmbeddedText{{Data: sidecar, Source: "sidecar"}}, metadata.Lyrics...)
	}
	if metadata.Title == "" && metadata.Artist == "" && metadata.Album == "" &&
		len(metadata.Lyrics) == 0 && len(metadata.Pictures) == 0 {
		return
	}
	now := time.Now()
	snapshot := metadataSnapshot{
		Title: metadata.Title, Artist: metadata.Artist, Album: metadata.Album,
		AlbumArtist: metadata.AlbumArtist, Track: metadata.Track, Disc: metadata.Disc,
	}
	var resources []MetadataResource
	if metadata.Title != "" || metadata.Artist != "" || metadata.Album != "" ||
		metadata.AlbumArtist != "" || metadata.Track > 0 || metadata.Disc > 0 {
		if encoded, err := json.Marshal(snapshot); err == nil {
			resource := newTextResource(serverFileID, "metadata", "json", "", string(encoded), false, now)
			resource.Source = "embedded"
			resource.LastUseAt = nil
			resources = append(resources, resource)
		}
	}
	for _, value := range metadata.Lyrics {
		lyrics := normalizeLyrics(value.Data)
		if lyrics == "" {
			continue
		}
		format := "plain"
		if strings.Contains(lyrics, "[") && strings.Contains(lyrics, ":") {
			format = "lrc"
		}
		resource := newTextResource(serverFileID, "lyrics", format, value.Language, lyrics, false, now)
		resource.Source = value.Source
		if resource.Source == "" {
			resource.Source = "embedded"
		}
		resource.LastUseAt = nil
		resources = append(resources, resource)
	}
	for _, picture := range metadata.Pictures {
		if len(picture.Data) == 0 || int64(len(picture.Data)) > maxCoverSize ||
			(picture.MIME != "image/jpeg" && picture.MIME != "image/png") {
			continue
		}
		resource := newBinaryResource(serverFileID, "cover", picture.MIME, picture.Data, false, now)
		resource.Source = "embedded"
		resource.LastUseAt = nil
		extension := ".jpg"
		if picture.MIME == "image/png" {
			extension = ".png"
		}
		resource.StoragePath = filepath.Join(s.storageRoot, "artwork", resource.ResourceHash+extension)
		if _, err := os.Stat(resource.StoragePath); errors.Is(err, os.ErrNotExist) {
			if os.WriteFile(resource.StoragePath, picture.Data, 0644) != nil {
				continue
			}
		}
		resources = append(resources, resource)
	}
	_ = s.db.Transaction(func(tx *gorm.DB) error {
		for index := range resources {
			if err := appendResource(tx, &resources[index]); err != nil {
				return err
			}
		}
		updates := map[string]any{}
		if metadata.Title != "" {
			updates["title"] = metadata.Title
		}
		if metadata.Artist != "" {
			updates["artist"] = metadata.Artist
		}
		if metadata.Album != "" {
			updates["album"] = metadata.Album
		}
		if metadata.AlbumArtist != "" {
			updates["album_artist"] = metadata.AlbumArtist
		}
		if metadata.Track > 0 {
			updates["track_number"] = metadata.Track
		}
		if metadata.Disc > 0 {
			updates["disc_number"] = metadata.Disc
		}
		if len(metadata.Lyrics) > 0 {
			updates["lyrics"] = normalizeLyrics(metadata.Lyrics[0].Data)
		}
		if len(updates) == 0 {
			return nil
		}
		return tx.Model(&ServerFile{}).Where("server_file_id = ?", serverFileID).Updates(updates).Error
	})
}

func readLocalEmbeddedMetadata(path string) localEmbeddedMetadata {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return readMP3Metadata(path)
	case ".flac":
		return readFLACMetadata(path)
	default:
		return localEmbeddedMetadata{}
	}
}

func readMP3Metadata(path string) localEmbeddedMetadata {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true, ParseFrames: []string{
		"Title", "Artist", "Album/Movie/Show title", "Band/orchestra/accompaniment",
		"Track number/Position in set", "Part of a set", "Attached picture",
		"Unsynchronised lyrics/text transcription",
	}})
	if err != nil {
		return localEmbeddedMetadata{}
	}
	defer tag.Close()
	result := localEmbeddedMetadata{
		Title: tag.Title(), Artist: tag.Artist(), Album: tag.Album(),
		AlbumArtist: tag.GetTextFrame(tag.CommonID("Band/orchestra/accompaniment")).Text,
		Track:       parseTagNumber(tag.GetTextFrame(tag.CommonID("Track number/Position in set")).Text),
		Disc:        parseTagNumber(tag.GetTextFrame(tag.CommonID("Part of a set")).Text),
	}
	for _, frame := range tag.GetFrames(tag.CommonID("Unsynchronised lyrics/text transcription")) {
		if lyrics, ok := frame.(id3v2.UnsynchronisedLyricsFrame); ok {
			result.Lyrics = append(result.Lyrics, localEmbeddedText{Data: lyrics.Lyrics, Language: lyrics.Language, Source: "embedded"})
		}
	}
	for _, frame := range tag.GetFrames(tag.CommonID("Attached picture")) {
		if picture, ok := frame.(id3v2.PictureFrame); ok {
			result.Pictures = append(result.Pictures, localEmbeddedPicture{Data: picture.Picture, MIME: strings.ToLower(picture.MimeType)})
		}
	}
	return result
}

func readFLACMetadata(path string) localEmbeddedMetadata {
	file, err := os.Open(path)
	if err != nil {
		return localEmbeddedMetadata{}
	}
	defer file.Close()
	parsed, err := flac.ParseMetadata(file)
	if err != nil {
		return localEmbeddedMetadata{}
	}
	var result localEmbeddedMetadata
	for _, block := range parsed.Meta {
		switch block.Type {
		case flac.VorbisComment:
			comment, parseErr := flacvorbis.ParseFromMetaDataBlock(*block)
			if parseErr != nil {
				continue
			}
			result.Title = firstVorbis(comment, "TITLE")
			result.Artist = firstVorbis(comment, "ARTIST")
			result.Album = firstVorbis(comment, "ALBUM")
			result.AlbumArtist = firstVorbis(comment, "ALBUMARTIST")
			result.Track = parseTagNumber(firstVorbis(comment, "TRACKNUMBER"))
			result.Disc = parseTagNumber(firstVorbis(comment, "DISCNUMBER"))
			for _, key := range []string{"LYRICS", "UNSYNCEDLYRICS"} {
				if values, _ := comment.Get(key); len(values) > 0 {
					for _, value := range values {
						result.Lyrics = append(result.Lyrics, localEmbeddedText{Data: value, Source: "embedded"})
					}
				}
			}
		case flac.Picture:
			picture, parseErr := flacpicture.ParseFromMetaDataBlock(*block)
			if parseErr == nil {
				result.Pictures = append(result.Pictures, localEmbeddedPicture{Data: picture.ImageData, MIME: strings.ToLower(picture.MIME)})
			}
		}
	}
	return result
}

func firstVorbis(comment *flacvorbis.MetaDataBlockVorbisComment, key string) string {
	values, _ := comment.Get(key)
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func parseTagNumber(value string) int {
	value = strings.TrimSpace(strings.SplitN(value, "/", 2)[0])
	number, _ := strconv.Atoi(value)
	return max(number, 0)
}

func readSidecarLyrics(audioPath string) string {
	for _, extension := range []string{".lrc", ".txt"} {
		path := strings.TrimSuffix(audioPath, filepath.Ext(audioPath)) + extension
		info, err := os.Stat(path)
		if err != nil || info.Size() <= 0 || info.Size() > 2*1024*1024 {
			continue
		}
		if data, readErr := os.ReadFile(path); readErr == nil {
			return normalizeLyrics(string(data))
		}
	}
	return ""
}
