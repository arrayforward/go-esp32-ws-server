// mockbackends is a test double for the ASR / LLM / TTS gRPC backends.
// It implements the mediator.* contracts with canned behavior so the
// router gateway can be integration-tested without the real services:
//
//   - AsrService: counts 20ms pcm chunks; every 25 chunks (500 ms of
//     "speech") emits one is_final response with a canned sentence.
//   - LlmService.Generate: returns a canned Chinese reply.
//   - BusinessService.Control: logs and acks (interrupt/config_update).
//   - TtsService.Synth: returns 0.8 s of 16 kHz sine PCM16 mono.
//
// Usage: go run ./cmd/mockbackends [-asr :50051] [-llm :50052] [-tts :50061]
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"math"
	"net"
	"sync/atomic"

	"google.golang.org/grpc"

	"router/gen/mediator"
)

// ---------- ASR ----------

type asrServer struct {
	mediator.UnimplementedAsrServiceServer
	chunks int64
}

func (s *asrServer) StreamingRecognize(stream mediator.AsrService_StreamingRecognizeServer) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		n := atomic.AddInt64(&s.chunks, 1)
		log.Printf("[mock-asr] chunk %d (%d bytes, flags=%d)", n, len(req.Pcm), req.Flags)
		if n%25 == 0 {
			resp := &mediator.AsrResponse{Text: "你好，给我讲个故事", IsFinal: true}
			if err := stream.Send(resp); err != nil {
				return err
			}
			log.Printf("[mock-asr] emit is_final")
		}
	}
}

// ---------- LLM ----------

type llmServer struct {
	mediator.UnimplementedLlmServiceServer
}

func (s *llmServer) Generate(ctx context.Context, req *mediator.LlmRequest) (*mediator.LlmResponse, error) {
	log.Printf("[mock-llm] Generate method=%s text=%s session=%s", req.Method, req.Text, req.SessionId)
	return &mediator.LlmResponse{Text: "好呀，从前有座山，山里有座庙，庙里有个小荷在唱歌。"}, nil
}

func (s *llmServer) JudgeInterrupt(ctx context.Context, req *mediator.InterruptRequest) (*mediator.InterruptResponse, error) {
	return &mediator.InterruptResponse{Interrupt: false, Reason: "mock"}, nil
}

// ---------- Business ----------

type bizServer struct {
	mediator.UnimplementedBusinessServiceServer
}

func (s *bizServer) Control(ctx context.Context, req *mediator.ControlRequest) (*mediator.ControlResponse, error) {
	log.Printf("[mock-biz] Control session=%s cmd=%.120s", req.SessionId, req.Cmd)
	return &mediator.ControlResponse{Ack: "ok"}, nil
}

// ---------- TTS ----------

type ttsServer struct {
	mediator.UnimplementedTtsServiceServer
}

func (s *ttsServer) Synth(ctx context.Context, req *mediator.TtsRequest) (*mediator.TtsResponse, error) {
	log.Printf("[mock-tts] Synth session=%s text=%.60s", req.SessionId, req.Text)
	// 0.8 s of 440 Hz sine, 16 kHz PCM16 mono
	const n = 12800
	pcm := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(9000 * math.Sin(2*math.Pi*440*float64(i)/16000))
		pcm[i*2] = byte(v)
		pcm[i*2+1] = byte(v >> 8)
	}
	return &mediator.TtsResponse{Pcm: pcm}, nil
}

// ---------- main ----------

func main() {
	asrAddr := flag.String("asr", ":50051", "ASR listen address")
	llmAddr := flag.String("llm", ":50052", "LLM listen address")
	ttsAddr := flag.String("tts", ":50061", "TTS listen address")
	flag.Parse()

	serve := func(addr string, register func(*grpc.Server)) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("listen %s: %v", addr, err)
		}
		s := grpc.NewServer()
		register(s)
		go func() {
			log.Printf("[mock] serving on %s", addr)
			if err := s.Serve(ln); err != nil {
				log.Fatalf("serve %s: %v", addr, err)
			}
		}()
	}

	serve(*asrAddr, func(s *grpc.Server) { mediator.RegisterAsrServiceServer(s, &asrServer{}) })
	serve(*llmAddr, func(s *grpc.Server) {
		mediator.RegisterLlmServiceServer(s, &llmServer{})
		mediator.RegisterBusinessServiceServer(s, &bizServer{})
	})
	serve(*ttsAddr, func(s *grpc.Server) { mediator.RegisterTtsServiceServer(s, &ttsServer{}) })

	select {}
}
