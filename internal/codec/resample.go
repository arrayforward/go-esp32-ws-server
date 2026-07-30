package codec

// Resample converts mono PCM16 between 8000 and 16000 Hz.
// Linear interpolation up, pairwise average down — cheap and adequate
// for a voice test gateway.

// ResampleTo16k upsamples 8 kHz PCM16 to 16 kHz (identity if already 16 kHz).
func ResampleTo16k(pcm []int16, srcRate int) []int16 {
	if srcRate == 16000 {
		return pcm
	}
	out := make([]int16, len(pcm)*2)
	for i := 0; i < len(pcm); i++ {
		cur := int32(pcm[i])
		var next int32
		if i+1 < len(pcm) {
			next = int32(pcm[i+1])
		} else {
			next = cur
		}
		out[i*2] = int16(cur)
		out[i*2+1] = int16((cur + next) / 2)
	}
	return out
}

// ResampleFrom16k downsamples 16 kHz PCM16 to 8 kHz (identity if target is 16 kHz).
func ResampleFrom16k(pcm []int16, dstRate int) []int16 {
	if dstRate == 16000 {
		return pcm
	}
	n := len(pcm) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16((int32(pcm[i*2]) + int32(pcm[i*2+1])) / 2)
	}
	return out
}
