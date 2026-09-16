package internal

type StationTranscodeProfile struct {
	Codec      string `yaml:"codec"`
	Container  string `yaml:"container"`
	Bitrate    int    `yaml:"bitrate"`
	SampleRate int    `yaml:"sampleRate"`
	Channels   int    `yaml:"channels"`
	Version    int    `yaml:"version"`
}

type Configs struct {
	StationID            string `yaml:"stationId"`
	StationPairingToken  string `yaml:"stationPairingToken"`
	StationPublicBaseUrl string `yaml:"stationPublicBaseUrl"`
	StationFFmpegPath    string `yaml:"stationFFmpegPath" comment:"Optional external FFmpeg path. Leave empty to use FFmpeg from PATH."`

	StationTranscodeProfiles map[string]StationTranscodeProfile `yaml:"stationTranscodeProfiles"`

	StationMediaRoots []string `yaml:"stationMediaRoots" comment:"Only these server directories may be browsed and added as local media \nsources. Add your music mount points here, for example /Volumes/Music."`
}

func NewAppConfig() *Configs {
	return &Configs{
		StationID:            "polemo-station-dev",
		StationPairingToken:  "",
		StationPublicBaseUrl: "",
		StationFFmpegPath:    "",

		StationTranscodeProfiles: map[string]StationTranscodeProfile{
			"balanced": {
				Codec:      "aac",
				Container:  "mp4",
				Bitrate:    256000,
				SampleRate: 48000,
				Channels:   2,
				Version:    1,
			},
			"mobile": {
				Codec:      "aac",
				Container:  "mp4",
				Bitrate:    192000,
				SampleRate: 48000,
				Channels:   2,
				Version:    1,
			},
		},
	}
}
