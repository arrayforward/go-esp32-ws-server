package gateway

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"router/internal/codec"
)

// Session is one device connection.
type Session struct {
	ID     uint64
	conn   *websocket.Conn
	pipe   Pipeline
	Device string

	Codec codec.Codec

	wMu     sync.Mutex // serializes all writes to conn
	txSeq   uint32
	audSeq  uint32
	started bool
}

func newSession(id uint64, conn *websocket.Conn, pipe Pipeline) *Session {
	return &Session{ID: id, conn: conn, pipe: pipe}
}

// SendText sends a typed envelope. Safe for concurrent use.
func (s *Session) SendText(typ string, body any) error {
	s.txSeq++
	data, err := BuildEnvelope(typ, s.txSeq, body)
	if err != nil {
		return err
	}
	s.wMu.Lock()
	defer s.wMu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

// SendAudioFrame sends one encoded audio frame (op 0x10).
func (s *Session) SendAudioFrame(payload []byte) error {
	s.audSeq++
	buf := make([]byte, AudioHdrLen+len(payload))
	PackAudioHeader(buf, OpAudioFrame, s.audSeq, nowMs())
	copy(buf[AudioHdrLen:], payload)
	s.wMu.Lock()
	defer s.wMu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, buf)
}

// SendAudioOp sends a bare audio op (Start/End/Cancel).
func (s *Session) SendAudioOp(op byte) error {
	s.audSeq++
	buf := make([]byte, AudioHdrLen)
	PackAudioHeader(buf, op, s.audSeq, nowMs())
	s.wMu.Lock()
	defer s.wMu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, buf)
}

// SendStatus is a shortcut for the status envelope.
func (s *Session) SendStatus(st string) error {
	return s.SendText("status", map[string]string{"status": st})
}

// SendEvent is a shortcut for the event envelope.
func (s *Session) SendEvent(ev, details string) error {
	return s.SendText("event", map[string]string{"event": ev, "details": details})
}

// SendAIReply sends the complete AI reply text to the device.
func (s *Session) SendAIReply(text string) error {
	return s.SendText("text", map[string]string{"text": text})
}

func (s *Session) loop() {
	defer func() {
		if s.Codec != nil {
			s.Codec.Close()
		}
		s.pipe.OnSessionEnd(s)
		s.conn.Close()
		log.Printf("[session %d] closed", s.ID)
	}()

	s.conn.SetReadLimit(64 * 1024)
	s.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	s.conn.SetPongHandler(func(string) error {
		return s.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	for {
		mt, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.TextMessage:
			if err := s.handleText(data); err != nil {
				log.Printf("[session %d] text error: %v", s.ID, err)
			}
		case websocket.BinaryMessage:
			if err := s.handleBinary(data); err != nil {
				log.Printf("[session %d] binary error: %v", s.ID, err)
			}
		}
	}
}

func (s *Session) handleText(data []byte) error {
	env, err := ParseEnvelope(data)
	if err != nil {
		return err
	}
	switch env.Type {
	case "hello":
		return s.handleHello(env.Body)
	case "ping":
		return s.SendText("pong", nil)
	case "bye":
		return s.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"),
			time.Now().Add(time.Second))
	case "config_update":
		s.pipe.OnConfigUpdate(s, env.Body)
		return s.SendText("config_update_ack", map[string]any{"result": "ok", "applied_at": nowMs()})
	case "function_call_output", "ack":
		log.Printf("[session %d] %s: %s", s.ID, env.Type, string(env.Body))
		return nil
	default:
		log.Printf("[session %d] ignoring type %s", s.ID, env.Type)
		return nil
	}
}

func (s *Session) handleHello(body json.RawMessage) error {
	var h helloBody
	if err := json.Unmarshal(body, &h); err != nil {
		return err
	}
	c, err := codec.New(h.AudioCodec)
	if err != nil {
		s.SendText("hello_err", map[string]string{
			"code": "UNSUPPORTED_CODEC", "message": err.Error()})
		return fmt.Errorf("unsupported codec %d", h.AudioCodec)
	}
	s.Codec = c
	s.Device = h.DeviceName
	sid := fmt.Sprintf("sess_%d_%d", s.ID, time.Now().Unix())

	s.SendText("hello_ack", map[string]any{
		"session_id":  sid,
		"server_time": nowMs(),
		"audio_config": map[string]any{
			"frame_ms": 20,
			"codec":    c.Name(),
			"vad":      "server",
		},
	})
	s.SendEvent("connected", "session established")
	s.SendStatus(StatusListening)
	s.started = true
	s.pipe.OnSessionStart(s)
	log.Printf("[session %d] hello: device=%s codec=%s sr=%d",
		s.ID, h.DeviceName, c.Name(), c.SampleRate())
	return nil
}

func (s *Session) handleBinary(data []byte) error {
	op, _, _, err := UnpackAudioHeader(data)
	if err != nil {
		return err
	}
	switch op {
	case OpAudioFrame:
		if s.Codec == nil {
			return nil
		}
		pcm, err := s.Codec.Decode(data[AudioHdrLen:])
		if err != nil {
			return err
		}
		s.pipe.OnAudio(s, codec.ResampleTo16k(pcm, s.Codec.SampleRate()))
	case OpAudioStart:
		// device-side VAD start marker; no action needed (server VAD)
	case OpAudioEnd:
		// device-side VAD end marker; server VAD decides
	case OpAudioCancel:
		s.pipe.OnCancel(s)
	default:
		log.Printf("[session %d] unknown audio op 0x%02x", s.ID, op)
	}
	return nil
}
