package codec

// G.711 A-law (ITU-T). Same algorithm as the ESP32 side
// (convai_codec_g711a.c) so silence vector 0 -> 0xD5 holds.

type g711aCodec struct{}

func (g711aCodec) ID() int         { return IDG711A }
func (g711aCodec) Name() string    { return "g711a" }
func (g711aCodec) SampleRate() int { return 8000 }
func (g711aCodec) Close()          {}

func alawEncodeSample(pcmVal int16) uint8 {
	var mask int
	v := int(pcmVal)
	if v >= 0 {
		mask = 0xD5
	} else {
		mask = 0x55
		v = -v - 8
	}
	v >>= 3
	if v > 4095 {
		v = 4095
	}
	seg := 0
	for v >= 32 && seg < 7 {
		seg++
		v >>= 1
	}
	aval := uint8((seg << 4) | ((v >> 1) & 0x0F))
	return aval ^ uint8(mask)
}

func alawDecodeSample(a uint8) int16 {
	a ^= 0x55
	t := int16(a&0x0F) << 4
	seg := int16(a&0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= uint(seg - 1)
	}
	if a&0x80 != 0 {
		return t
	}
	return -t
}

func (g711aCodec) Encode(pcm []int16) ([]byte, error) {
	out := make([]byte, len(pcm))
	for i, s := range pcm {
		out[i] = alawEncodeSample(s)
	}
	return out, nil
}

func (g711aCodec) Decode(enc []byte) ([]int16, error) {
	out := make([]int16, len(enc))
	for i, b := range enc {
		out[i] = alawDecodeSample(b)
	}
	return out, nil
}
