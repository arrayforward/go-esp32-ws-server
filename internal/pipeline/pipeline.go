// Package pipeline wires a device session to ASR -> LLM -> TTS services.
package pipeline

import (
	"context"
	"log"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"router/gen/mediator"
	"router/internal/codec"
	"router/internal/gateway"
)

// Config holds backend addresses.
type Config struct {
	ASRAddr string // mediator.AsrService
	LLMAddr string // mediator.LlmService + BusinessService
	TTSAddr string // mediator.TtsService
	ASRFlags uint32 // flags sent with each pcm chunk (default 4 = 有声)
}

// Pipeline implements gateway.Pipeline.
type Pipeline struct {
	cfg Config

	asrConn *grpc.ClientConn
	llmConn *grpc.ClientConn
	ttsConn *grpc.ClientConn

	mu   sync.Mutex
	sess map[uint64]*sessionState
}

type sessionState struct {
	asrStream mediator.AsrService_StreamingRecognizeClient
	asrCtx    context.Context
	asrCancel context.CancelFunc
	sid       string
	turnMu    sync.Mutex // one AI turn in flight
	busy      bool
}

func New(cfg Config) (*Pipeline, error) {
	if cfg.ASRFlags == 0 {
		cfg.ASRFlags = 4
	}
	p := &Pipeline{cfg: cfg, sess: make(map[uint64]*sessionState)}
	var err error
	dial := func(addr string) (*grpc.ClientConn, error) {
		return grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	if p.asrConn, err = dial(cfg.ASRAddr); err != nil {
		return nil, err
	}
	if p.llmConn, err = dial(cfg.LLMAddr); err != nil {
		return nil, err
	}
	if p.ttsConn, err = dial(cfg.TTSAddr); err != nil {
		return nil, err
	}
	log.Printf("[pipeline] backends: asr=%s llm=%s tts=%s", cfg.ASRAddr, cfg.LLMAddr, cfg.TTSAddr)
	return p, nil
}

func (p *Pipeline) Close() {
	p.asrConn.Close()
	p.llmConn.Close()
	p.ttsConn.Close()
}

func (p *Pipeline) OnSessionStart(s *gateway.Session) {
	st := &sessionState{sid: s.Device}
	var err error
	st.asrCtx, st.asrCancel = context.WithCancel(context.Background())
	st.asrStream, err = mediator.NewAsrServiceClient(p.asrConn).StreamingRecognize(st.asrCtx)
	if err != nil {
		log.Printf("[session %d] asr stream open failed: %v", s.ID, err)
		st.asrStream = nil
	}
	p.mu.Lock()
	p.sess[s.ID] = st
	p.mu.Unlock()

	if st.asrStream != nil {
		go p.asrRecvLoop(s, st)
	}
}

func (p *Pipeline) OnSessionEnd(s *gateway.Session) {
	p.mu.Lock()
	st, ok := p.sess[s.ID]
	delete(p.sess, s.ID)
	p.mu.Unlock()
	if ok {
		if st.asrStream != nil {
			st.asrStream.CloseSend()
		}
		st.asrCancel()
	}
}

func (p *Pipeline) get(s *gateway.Session) *sessionState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sess[s.ID]
}

func (p *Pipeline) OnAudio(s *gateway.Session, pcm []int16) {
	st := p.get(s)
	if st == nil || st.asrStream == nil {
		return
	}
	buf := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		buf[i*2] = byte(v)
		buf[i*2+1] = byte(v >> 8)
	}
	if err := st.asrStream.Send(&mediator.AsrRequest{Pcm: buf, Flags: p.cfg.ASRFlags}); err != nil {
		log.Printf("[session %d] asr send: %v", s.ID, err)
	}
}

func (p *Pipeline) OnCancel(s *gateway.Session) {
	log.Printf("[session %d] barge-in cancel", s.ID)
	_, err := mediator.NewBusinessServiceClient(p.llmConn).Control(
		context.Background(), &mediator.ControlRequest{Cmd: "interrupt", SessionId: s.Device})
	if err != nil {
		log.Printf("[session %d] interrupt: %v", s.ID, err)
	}
	s.SendStatus(gateway.StatusInterrupted)
	s.SendStatus(gateway.StatusListening)
}

func (p *Pipeline) OnConfigUpdate(s *gateway.Session, body []byte) {
	ack, err := mediator.NewBusinessServiceClient(p.llmConn).Control(
		context.Background(), &mediator.ControlRequest{Cmd: string(body), SessionId: s.Device})
	if err != nil {
		log.Printf("[session %d] config_update forward: %v", s.ID, err)
		return
	}
	log.Printf("[session %d] config_update -> llm ack=%s", s.ID, ack.Ack)
}

func (p *Pipeline) asrRecvLoop(s *gateway.Session, st *sessionState) {
	for {
		resp, err := st.asrStream.Recv()
		if err != nil {
			return
		}
		if !resp.IsFinal {
			continue
		}
		if resp.Text == "" {
			continue
		}
		log.Printf("[session %d] asr final: %s", s.ID, resp.Text)
		go p.runTurn(s, st, resp.Text)
	}
}

// runTurn: one full AI turn — LLM answer then TTS playback to the device.
func (p *Pipeline) runTurn(s *gateway.Session, st *sessionState, text string) {
	st.turnMu.Lock()
	if st.busy {
		st.turnMu.Unlock()
		log.Printf("[session %d] turn busy, dropping: %s", s.ID, text)
		return
	}
	st.busy = true
	st.turnMu.Unlock()
	defer func() { st.busy = false }()

	s.SendStatus(gateway.StatusThinking)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	llmResp, err := mediator.NewLlmServiceClient(p.llmConn).Generate(ctx,
		&mediator.LlmRequest{Method: "answer", Text: text, SessionId: s.Device})
	if err != nil {
		log.Printf("[session %d] llm: %v", s.ID, err)
		s.SendText("error", map[string]string{"code": "LLM_ERROR", "message": err.Error()})
		s.SendStatus(gateway.StatusListening)
		return
	}
	reply := llmResp.Text
	if reply == "" {
		s.SendStatus(gateway.StatusListening)
		return
	}
	log.Printf("[session %d] llm reply: %s", s.ID, reply)
	s.SendAIReply(reply)

	ttsResp, err := mediator.NewTtsServiceClient(p.ttsConn).Synth(ctx,
		&mediator.TtsRequest{Text: reply, SessionId: s.Device})
	if err != nil {
		log.Printf("[session %d] tts: %v", s.ID, err)
		s.SendText("error", map[string]string{"code": "TTS_ERROR", "message": err.Error()})
		s.SendStatus(gateway.StatusListening)
		return
	}

	// TTS returns PCM16 16 kHz mono; resample to the device codec rate.
	pcm, err := bytesToPCM(ttsResp.Pcm)
	if err != nil || len(pcm) == 0 {
		s.SendStatus(gateway.StatusListening)
		return
	}
	pcm = codec.ResampleFrom16k(pcm, s.Codec.SampleRate())

	s.SendAudioOp(gateway.OpAudioStart)
	s.SendStatus(gateway.StatusAnswering)

	frameSamples := s.Codec.SampleRate() / 50 // 20 ms
	for i := 0; i < len(pcm); i += frameSamples {
		end := i + frameSamples
		if end > len(pcm) {
			// zero-pad the tail to a full frame
			tail := make([]int16, frameSamples)
			copy(tail, pcm[i:])
			pcm = append(pcm[:i], tail...)
			end = len(pcm)
		}
		pkt, err := s.Codec.Encode(pcm[i:end])
		if err != nil {
			log.Printf("[session %d] encode: %v", s.ID, err)
			break
		}
		if err := s.SendAudioFrame(pkt); err != nil {
			return
		}
		time.Sleep(15 * time.Millisecond) // pace near real-time
	}

	s.SendAudioOp(gateway.OpAudioEnd)
	s.SendStatus(gateway.StatusAnswerFinished)
	s.SendStatus(gateway.StatusListening)
}

func bytesToPCM(b []byte) ([]int16, error) {
	n := len(b) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(b[i*2]) | int16(b[i*2+1])<<8
	}
	return out, nil
}
