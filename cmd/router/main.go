package main

import (
	"flag"
	"log"
	"os"

	"router/internal/gateway"
	"router/internal/pipeline"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	listen := flag.String("listen", envOr("ROUTER_LISTEN", ":9000"), "WebSocket listen address")
	asr := flag.String("asr", envOr("ASR_ADDR", "127.0.0.1:50051"), "ASR gRPC address (mediator.AsrService)")
	llm := flag.String("llm", envOr("LLM_ADDR", "127.0.0.1:50052"), "LLM gRPC address (mediator.LlmService)")
	tts := flag.String("tts", envOr("TTS_ADDR", "127.0.0.1:50061"), "TTS gRPC address (mediator.TtsService)")
	flags := flag.Uint("asr-flags", 4, "flags field sent with each ASR pcm chunk")
	tlsCert := flag.String("tls-cert", envOr("ROUTER_TLS_CERT", ""), "TLS cert PEM path (enables wss when set with -tls-key)")
	tlsKey := flag.String("tls-key", envOr("ROUTER_TLS_KEY", ""), "TLS key PEM path (enables wss when set with -tls-cert)")
	flag.Parse()

	pipe, err := pipeline.New(pipeline.Config{
		ASRAddr: *asr, LLMAddr: *llm, TTSAddr: *tts, ASRFlags: uint32(*flags),
	})
	if err != nil {
		log.Fatalf("pipeline init: %v", err)
	}
	defer pipe.Close()

	srv := &gateway.Server{Addr: *listen, Pipe: pipe, TLSCert: *tlsCert, TLSKey: *tlsKey}
	log.Fatal(srv.Run())
}
