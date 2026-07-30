package codec

// G.711 u-law (ITU-T). Silence encodes to 0xFF.

type g711uCodec struct{}

func (g711uCodec) ID() int         { return IDG711U }
func (g711uCodec) Name() string    { return "g711u" }
func (g711uCodec) SampleRate() int { return 8000 }
func (g711uCodec) Close()          {}

const (
	mulawBias = 132
	mulawClip = 32635
)

var mulawSegEnd = [8]int{0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF, 0x1FFF, 0x3FFF, 0x7FFF}

func mulawEncodeSample(pcmVal int16) uint8 {
	var mask uint8
	v := int(pcmVal)
	if v < 0 {
		v = mulawBias - v
		mask = 0x7F
	} else {
		v = mulawBias + v
		mask = 0xFF
	}
	if v > mulawClip {
		v = mulawClip
	}
	seg := 0
	for seg = 0; seg < 8; seg++ {
		if v <= mulawSegEnd[seg] {
			break
		}
	}
	if seg >= 8 {
		return 0x7F ^ mask
	}
	uval := uint8((seg << 4) | ((v >> uint(seg+3)) & 0xF))
	return uval ^ mask
}

func mulawDecodeSample(u uint8) int16 {
	u = ^u
	t := (int(u&0x0F) << 3) + mulawBias
	t <<= uint((u & 0x70) >> 4)
	if u&0x80 != 0 {
		return int16(mulawBias - t)
	}
	return int16(t - mulawBias)
}

func (g711uCodec) Encode(pcm []int16) ([]byte, error) {
	out := make([]byte, len(pcm))
	for i, s := range pcm {
		out[i] = mulawEncodeSample(s)
	}
	return out, nil
}

func (g711uCodec) Decode(enc []byte) ([]int16, error) {
	out := make([]int16, len(enc))
	for i, b := range enc {
		out[i] = mulawDecodeSample(b)
	}
	return out, nil
}
