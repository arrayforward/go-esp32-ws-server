package gateway

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"convai.v1"},
	CheckOrigin:  func(r *http.Request) bool { return true },
}

// Pipeline processes one device session (ASR -> LLM -> TTS).
type Pipeline interface {
	// OnSessionStart is called after hello_ack.
	OnSessionStart(s *Session)
	// OnAudio is called with decoded mono PCM16 @16kHz for every device frame.
	OnAudio(s *Session, pcm []int16)
	// OnCancel handles barge-in (audio op 0x13).
	OnCancel(s *Session)
	// OnConfigUpdate forwards the raw config_update envelope body.
	OnConfigUpdate(s *Session, body []byte)
	// OnSessionEnd releases resources.
	OnSessionEnd(s *Session)
}

// Server is the convai.v1 WebSocket gateway.
type Server struct {
	Addr     string
	Pipe     Pipeline
	TLSCert  string // PEM cert path; empty = plain ws
	TLSKey   string // PEM key path
	sessionN uint64
}

func (srv *Server) Run() error {
	http.HandleFunc("/", srv.handleWS)
	if srv.TLSCert != "" && srv.TLSKey != "" {
		log.Printf("[gateway] listening on wss://%s (subprotocol convai.v1)", srv.Addr)
		return srv.runTLS()
	}
	log.Printf("[gateway] listening on ws://%s (subprotocol convai.v1)", srv.Addr)
	return http.ListenAndServe(srv.Addr, nil)
}

// runTLS serves WSS with MCU-friendly cipher suites: ESP32-S3 has AES
// hardware acceleration, and ECDSA P-256 handshakes are ~10x cheaper than
// RSA-2048 on Xtensa. Prefer ECDHE-ECDSA + AES-256/128-GCM.
func (srv *Server) runTLS() error {
	cert, err := tls.LoadX509KeyPair(srv.TLSCert, srv.TLSKey)
	if err != nil {
		return err
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	return http.Serve(tls.NewListener(ln, cfg), nil)
}

func (srv *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[gateway] upgrade failed: %v", err)
		return
	}
	id := atomic.AddUint64(&srv.sessionN, 1)
	s := newSession(id, conn, srv.Pipe)
	log.Printf("[session %d] connected from %s", id, r.RemoteAddr)
	s.loop()
}
