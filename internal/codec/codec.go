// Package codec implements the audio codecs of the convai.v1 protocol:
// PCM16, G.711A, G.711U, IMA-ADPCM and Opus (via libopus cgo).
// All codecs work on mono int16 PCM at their native sample rate.
package codec

// IDs match convai.v1 hello.audio_codec and the ESP32 convai_codec_id_e.
const (
	IDPCM16     = 0
	IDG711A     = 1
	IDG711U     = 2
	IDIMADPCM   = 3
	IDOpus      = 4
)

// Codec encodes/decodes mono PCM16 frames.
type Codec interface {
	ID() int
	Name() string
	SampleRate() int // native rate: 8000 or 16000
	// Encode converts PCM16 samples to the codec format.
	Encode(pcm []int16) ([]byte, error)
	// Decode converts codec bytes back to PCM16 samples.
	Decode(enc []byte) ([]int16, error)
	// Close releases resources (Opus). Safe to call multiple times.
	Close()
}

// New creates a codec instance by convai.v1 codec id.
func New(id int) (Codec, error) {
	switch id {
	case IDPCM16:
		return pcm16Codec{}, nil
	case IDG711A:
		return g711aCodec{}, nil
	case IDG711U:
		return g711uCodec{}, nil
	case IDIMADPCM:
		return &adpcmCodec{}, nil
	case IDOpus:
		return newOpusCodec()
	default:
		return nil, errUnknownCodec
	}
}

type codecError string

func (e codecError) Error() string { return string(e) }

const errUnknownCodec = codecError("unknown codec id")
