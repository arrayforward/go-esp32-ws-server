//go:build !cgo

package codec

import "errors"

// Stub for CGO-less builds (e.g. Windows without libopus): Opus is
// unavailable; PCM16/G711A/G711U/IMA-ADPCM keep working, which covers
// the E2E gateway tests.
func newOpusCodec() (Codec, error) {
	return nil, errors.New("opus: built without cgo/libopus")
}
