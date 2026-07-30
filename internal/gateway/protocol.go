package gateway

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"
)

// convai.v1 binary audio ops
const (
	OpAudioFrame  = 0x10
	OpAudioStart  = 0x11
	OpAudioEnd    = 0x12
	OpAudioCancel = 0x13
	AudioHdrLen   = 13
)

// Envelope is the convai.v1 text frame: {"type","seq","ts","body"}.
type Envelope struct {
	Type string          `json:"type"`
	Seq  uint32          `json:"seq"`
	Ts   uint64          `json:"ts"`
	Body json.RawMessage `json:"body"`
}

func nowMs() uint64 { return uint64(time.Now().UnixMilli()) }

// BuildEnvelope serializes an envelope; body may be nil (-> {}).
func BuildEnvelope(typ string, seq uint32, body any) ([]byte, error) {
	var raw json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		raw = b
	} else {
		raw = json.RawMessage(`{}`)
	}
	return json.Marshal(Envelope{Type: typ, Seq: seq, Ts: nowMs(), Body: raw})
}

// ParseEnvelope decodes a text frame.
func ParseEnvelope(data []byte) (*Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	if e.Type == "" {
		return nil, errors.New("missing type")
	}
	return &e, nil
}

// PackAudioHeader writes the 13-byte big-endian audio header.
func PackAudioHeader(hdr []byte, op byte, seq uint32, ts uint64) {
	hdr[0] = op
	binary.BigEndian.PutUint32(hdr[1:5], seq)
	binary.BigEndian.PutUint64(hdr[5:13], ts)
}

// UnpackAudioHeader parses the 13-byte header; returns op, seq, ts.
func UnpackAudioHeader(data []byte) (byte, uint32, uint64, error) {
	if len(data) < AudioHdrLen {
		return 0, 0, 0, errors.New("short audio frame")
	}
	return data[0], binary.BigEndian.Uint32(data[1:5]), binary.BigEndian.Uint64(data[5:13]), nil
}

// hello body from the device
type helloBody struct {
	ProductID     string `json:"product_id"`
	ProductKey    string `json:"product_key"`
	ProductSecret string `json:"product_secret"`
	DeviceName    string `json:"device_name"`
	AudioCodec    int    `json:"audio_codec"`
	SampleRate    int    `json:"sample_rate"`
}

// status strings matching convai_status_e
const (
	StatusIdle           = "idle"
	StatusListening      = "listening"
	StatusThinking       = "thinking"
	StatusAnswering      = "answering"
	StatusInterrupted    = "interrupted"
	StatusAnswerFinished = "answer_finished"
)
