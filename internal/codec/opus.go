package codec

/*
#cgo pkg-config: opus
#include <stdlib.h>
#include <string.h>
#include <opus.h>

static int gw_enc_ctl(OpusEncoder *st, int bitrate, int complexity) {
	int rc;
	rc = opus_encoder_ctl(st, OPUS_SET_BITRATE(bitrate));
	if (rc != OPUS_OK) return rc;
	rc = opus_encoder_ctl(st, OPUS_SET_COMPLEXITY(complexity));
	if (rc != OPUS_OK) return rc;
	return opus_encoder_ctl(st, OPUS_SET_VBR(0));
}
*/
import "C"
import (
	"errors"
	"unsafe"
)

// Opus via system libopus (cgo). 16 kHz mono, 20 ms frames, 16 kbps CBR,
// complexity 1 — mirrors the ESP32 adapter parameters.
type opusCodec struct {
	enc *C.OpusEncoder
	dec *C.OpusDecoder
}

const (
	opusSampleRate = 16000
	opusBitrate    = 16000
	opusComplexity = 1
	opusMaxPacket  = 256
)

func newOpusCodec() (Codec, error) {
	var err C.int
	c := &opusCodec{}
	c.enc = C.opus_encoder_create(opusSampleRate, 1, C.OPUS_APPLICATION_VOIP, &err)
	if err != C.OPUS_OK || c.enc == nil {
		return nil, errors.New("opus_encoder_create failed")
	}
	c.dec = C.opus_decoder_create(opusSampleRate, 1, &err)
	if err != C.OPUS_OK || c.dec == nil {
		C.opus_encoder_destroy(c.enc)
		return nil, errors.New("opus_decoder_create failed")
	}
	C.gw_enc_ctl(c.enc, opusBitrate, opusComplexity)
	return c, nil
}

func (c *opusCodec) ID() int         { return IDOpus }
func (c *opusCodec) Name() string    { return "opus" }
func (c *opusCodec) SampleRate() int { return opusSampleRate }

func (c *opusCodec) Close() {
	if c.enc != nil {
		C.opus_encoder_destroy(c.enc)
		c.enc = nil
	}
	if c.dec != nil {
		C.opus_decoder_destroy(c.dec)
		c.dec = nil
	}
}

func (c *opusCodec) Encode(pcm []int16) ([]byte, error) {
	if c.enc == nil {
		return nil, errors.New("opus encoder closed")
	}
	out := make([]byte, opusMaxPacket)
	n := C.opus_encode(c.enc,
		(*C.opus_int16)(unsafe.Pointer(&pcm[0])),
		C.int(len(pcm)),
		(*C.uchar)(unsafe.Pointer(&out[0])),
		C.opus_int32(len(out)))
	if n < 0 {
		return nil, errors.New("opus_encode failed")
	}
	return out[:n], nil
}

func (c *opusCodec) Decode(enc []byte) ([]int16, error) {
	if c.dec == nil {
		return nil, errors.New("opus decoder closed")
	}
	out := make([]int16, 5760) // up to 120 ms
	n := C.opus_decode(c.dec,
		(*C.uchar)(unsafe.Pointer(&enc[0])),
		C.opus_int32(len(enc)),
		(*C.opus_int16)(unsafe.Pointer(&out[0])),
		C.int(len(out)), 0)
	if n < 0 {
		return nil, errors.New("opus_decode failed")
	}
	return out[:n], nil
}
