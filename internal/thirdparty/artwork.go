package thirdparty

// Artwork is a downloaded JPEG or PNG from a metadata provider.
type Artwork struct {
	Data []byte
	MIME string
}
