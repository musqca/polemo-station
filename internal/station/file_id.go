package station

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

const (
	serverFileIDDomain      = "polemo-server-file-id-v1"
	serverFileIDPrefix      = "sf1_"
	serverFileIDWindowSize  = int64(64 * 1024)
	serverFileIDSampleCount = int64(8)
	serverFileIDFullReadMax = int64(1024 * 1024)
)

// serverFileIDForPath implements Polemo's cross-platform, deterministic
// sampled file identity. Keep this byte layout in sync with the Dart client.
func serverFileIDForPath(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return "", err
	}
	return serverFileIDForReader(file, stat.Size())
}

func serverFileIDForReader(reader io.ReaderAt, size int64) (string, error) {
	if size < 0 {
		return "", fmt.Errorf("negative file size: %d", size)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(serverFileIDDomain))
	_, _ = hash.Write([]byte{0})
	writeUint64(hash, uint64(size))

	if size <= serverFileIDFullReadMax {
		if err := hashSample(hash, reader, 0, size); err != nil {
			return "", err
		}
	} else {
		available := size - serverFileIDWindowSize
		for index := int64(1); index <= serverFileIDSampleCount; index++ {
			offset := available * index / (serverFileIDSampleCount + 1)
			if err := hashSample(hash, reader, offset, serverFileIDWindowSize); err != nil {
				return "", err
			}
		}
	}
	return serverFileIDPrefix + hex.EncodeToString(hash.Sum(nil)), nil
}

func hashSample(hash io.Writer, reader io.ReaderAt, offset, length int64) error {
	writeUint64(hash, uint64(offset))
	var lengthBytes [4]byte
	binary.BigEndian.PutUint32(lengthBytes[:], uint32(length))
	_, _ = hash.Write(lengthBytes[:])
	if length == 0 {
		return nil
	}
	buffer := make([]byte, int(length))
	read, err := reader.ReadAt(buffer, offset)
	if err != nil && err != io.EOF {
		return err
	}
	if int64(read) != length {
		return io.ErrUnexpectedEOF
	}
	_, err = hash.Write(buffer)
	return err
}

func writeUint64(writer io.Writer, value uint64) {
	var bytes [8]byte
	binary.BigEndian.PutUint64(bytes[:], value)
	_, _ = writer.Write(bytes[:])
}
