package codec

// IMA/DVI ADPCM: 4 bits/sample, 4:1 compression, table driven.
// Low nibble first, same layout as the ESP32 codec_ima_adpcm.c.

type adpcmState struct {
	predictor  int32
	stepIndex  int32
	nibbleBuf  uint8
	hasNibble  bool
}

type adpcmCodec struct {
	enc adpcmState
	dec adpcmState
}

func (c *adpcmCodec) ID() int         { return IDIMADPCM }
func (c *adpcmCodec) Name() string    { return "ima_adpcm" }
func (c *adpcmCodec) SampleRate() int { return 8000 }
func (c *adpcmCodec) Close()          {}

var imaStepTable = [89]int16{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17,
	19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118,
	130, 143, 157, 173, 190, 209, 230, 253, 279, 307,
	337, 371, 408, 449, 494, 544, 598, 658, 724, 796,
	876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066,
	2272, 2499, 2749, 3024, 3327, 3660, 4026, 4428, 4871, 5358,
	5894, 6484, 7132, 7845, 8630, 9493, 10442, 11487, 12635, 13899,
	15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767,
}

var imaIndexTable = [16]int8{
	-1, -1, -1, -1, 2, 4, 6, 8,
	-1, -1, -1, -1, 2, 4, 6, 8,
}

func clamp16(v int32) int32 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return v
}

func clampIndex(v int32) int32 {
	if v < 0 {
		return 0
	}
	if v > 88 {
		return 88
	}
	return v
}

func imaEncodeSample(s *adpcmState, sample int16) uint8 {
	step := int32(imaStepTable[s.stepIndex])
	diff := int32(sample) - s.predictor
	var nibble uint8
	if diff < 0 {
		nibble = 8
		diff = -diff
	}
	vpdiff := step >> 3
	if diff >= step {
		nibble |= 4
		diff -= step
		vpdiff += step
	}
	if diff >= step>>1 {
		nibble |= 2
		diff -= step >> 1
		vpdiff += step >> 1
	}
	if diff >= step>>2 {
		nibble |= 1
		vpdiff += step >> 2
	}
	if nibble&8 != 0 {
		s.predictor = clamp16(s.predictor - vpdiff)
	} else {
		s.predictor = clamp16(s.predictor + vpdiff)
	}
	s.stepIndex = clampIndex(s.stepIndex + int32(imaIndexTable[nibble]))
	return nibble & 0x0F
}

func imaDecodeSample(s *adpcmState, nibble uint8) int16 {
	step := int32(imaStepTable[s.stepIndex])
	vpdiff := step >> 3
	if nibble&4 != 0 {
		vpdiff += step
	}
	if nibble&2 != 0 {
		vpdiff += step >> 1
	}
	if nibble&1 != 0 {
		vpdiff += step >> 2
	}
	if nibble&8 != 0 {
		s.predictor = clamp16(s.predictor - vpdiff)
	} else {
		s.predictor = clamp16(s.predictor + vpdiff)
	}
	s.stepIndex = clampIndex(s.stepIndex + int32(imaIndexTable[nibble&0x0F]))
	return int16(s.predictor)
}

func (c *adpcmCodec) Encode(pcm []int16) ([]byte, error) {
	out := make([]byte, 0, (len(pcm)+1)/2)
	s := &c.enc
	for _, sample := range pcm {
		nib := imaEncodeSample(s, sample)
		if !s.hasNibble {
			s.nibbleBuf = nib
			s.hasNibble = true
		} else {
			out = append(out, s.nibbleBuf|(nib<<4))
			s.hasNibble = false
		}
	}
	if s.hasNibble {
		out = append(out, s.nibbleBuf)
		s.hasNibble = false
	}
	return out, nil
}

func (c *adpcmCodec) Decode(enc []byte) ([]int16, error) {
	out := make([]int16, 0, len(enc)*2)
	s := &c.dec
	for _, b := range enc {
		out = append(out, imaDecodeSample(s, b&0x0F))
		out = append(out, imaDecodeSample(s, (b>>4)&0x0F))
	}
	return out, nil
}
