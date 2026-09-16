package station

import (
	"bytes"
	"testing"
)

func TestServerFileIDGoldenVectors(t *testing.T) {
	tests := []struct {
		name string
		size int
		want string
	}{
		{name: "empty", size: 0, want: "sf1_39caaa76c782ba7d7cd57b8a85687d466952a7218e89f8e3bacd2e73821a8033"},
		{name: "one-byte", size: 1, want: "sf1_485437d6c1e675b0c64af2c52326ea4ffe3047f7039ae3cae9944f8c1d31470b"},
		{name: "window", size: 64 * 1024, want: "sf1_1563f2f0dff178cdb29ae0bf547b1e1ec4be06cbbd4523b512b173546af97094"},
		{name: "one-megabyte", size: 1024 * 1024, want: "sf1_677e81a28a5c16da6a98b137da3ba0cbaf1c61fed5cfbfd374a165e247bb5f05"},
		{name: "sampled", size: 1024*1024 + 1, want: "sf1_298ebc9879766a7c5cfa26ba9790ffef603c25d684ddddce4351eaf7e095ca87"},
		{name: "two-megabytes", size: 2 * 1024 * 1024, want: "sf1_86d6db616aef82d59a77fda6c91169f245cb60f1060cc42470f1216a12d6b74b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := deterministicFixture(test.size)
			got, err := serverFileIDForReader(bytes.NewReader(content), int64(len(content)))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %s want %s", got, test.want)
			}
			t.Logf("%s=%s", test.name, got)
		})
	}
}

func deterministicFixture(size int) []byte {
	content := make([]byte, size)
	for index := range content {
		content[index] = byte((index*31 + index/251 + 17) % 256)
	}
	return content
}

func TestServerFileIDIgnoresUnsampledBytes(t *testing.T) {
	content := deterministicFixture(2 * 1024 * 1024)
	baseline, err := serverFileIDForReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	content[0] ^= 0xff
	content[len(content)-1] ^= 0xff
	got, err := serverFileIDForReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if got != baseline {
		t.Fatalf("header or tail changed sampled id: %s != %s", got, baseline)
	}
}
