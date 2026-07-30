package codec

// pcm16Codec: pass-through, no compression.
type pcm16Codec struct{}

func (pcm16Codec) ID() int          { return IDPCM16 }
func (pcm16Codec) Name() string     { return "pcm16" }
func (pcm16Codec) SampleRate() int  { return 8000 }
func (pcm16Codec) Close()           {}

func (pcm16Codec) Encode(pcm []int16) ([]byte, error) {
	out := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		out[i*2] = byte(s)
		out[i*2+1] = byte(s >> 8)
	}
	return out, nil
}

func (pcm16Codec) Decode(enc []byte) ([]int16, error) {
	n := len(enc) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(enc[i*2]) | int16(enc[i*2+1])<<8
	}
	return out, nil
}
