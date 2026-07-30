# 02 - 技术设计：网关架构与会话/流水线（Design）

> SDD 阶段：设计（How）。

## 1. 总体架构

```
                        ┌──────────────────────────────────────────┐
ESP32-S3                │                router (Go)               │
   │   WS convai.v1     │                                          │
   │◀──────────────▶   │  internal/gateway    internal/pipeline   │
   │                    │  ├ server.go (HTTP▶WS) ├ clients(gRPC)   │
   │                    │  ├ session.go(会话)    ├ ASR 流收发       │
   │                    │  └ protocol.go(信封/头)├ turn: LLM→TTS   │
   │                    │  internal/codec        └ barge-in        │
   │                    │  5 种编解码 + 重采样                      │
   │                    └──────┬──────────┬──────────┬─────────────┘
   │                           │ gRPC     │ gRPC     │ gRPC
   │                      :50051 ASR  :50052 LLM  :50061 TTS
   │                    (mediator.AsrService / LlmService+BusinessService / TtsService)
```

分层职责：

- **gateway 包**：只做协议与连接（信封解析、音频头、状态机、并发写锁），不懂 AI。
- **pipeline 包**：只做 AI 接力（ASR/LLM/TTS 的 gRPC 客户端与轮次编排），不懂 WS 细节，
  通过 `gateway.Pipeline` 接口被回调（OnSessionStart/OnAudio/OnCancel/OnConfigUpdate/OnSessionEnd）。
- **codec 包**：纯算法，无网络依赖，测试向量与 ESP32 端一致。

## 2. 协议实现

### 2.1 文本信封

`{"type","seq","ts","body"}` —— `gateway/protocol.go` 的 Build/Parse 成对。
seq 每个方向各自单调递增（会话结构体 txSeq）；ts 毫秒时间戳。

### 2.2 二进制音频帧

13 字节大端头（`PackAudioHeader/UnpackAudioHeader`）：

```
[0]    u8   op        0x10 Frame / 0x11 Start / 0x12 End / 0x13 Cancel
[1:5]  u32  sequence  BE
[5:13] u64  timestamp BE (ms)
[13:]  编码负载
```

### 2.3 握手时序

```
设备 ──WS Upgrade(子协议 convai.v1)──▶
设备 ──TEXT hello {product_*, audio_codec:id, sample_rate}▶
     · codec.New(id) 失败 → TEXT hello_err 并断开
     ◀─TEXT hello_ack {session_id, server_time, audio_config:{frame_ms:20,codec:name,vad:"server"}}
     ◀─TEXT event {connected}
     ◀─TEXT status {listening}
     → pipeline.OnSessionStart（打开 ASR 流 + 接收协程）
```

### 2.4 传输安全（ws / wss）

- 提供 `-tls-cert` / `-tls-key`（或 `ROUTER_TLS_CERT/KEY`）后启用 WSS
  （`http.ListenAndServeTLS`），否则明文 ws。
- 设备端（esp_websocket_client）使用 wss 时需在固件配置 CA 证书
  （自签证书可将 PEM 嵌入 `GOLDIE_SERVER_URL=wss://...` 对应的 cert_pem），
  或使用受信 CA 签发的证书。
- 生产建议：网关保持明文 ws，前面挂 TLS 终结反代（nginx/caddy），
  证书轮换不触碰网关进程。

### 2.4 一轮对话时序

```
设备 ═BIN 0x10×N═▶ 解码 → 重采样16k → ASR 流(flags=4)
     ◀ASR is_final(text)═
     ◀─TEXT status {thinking}
     → LLM Generate(answer, text)
     ◀─TEXT text {reply}                    （AI 回复文本，设备可显示）
     → TTS Synth(reply) → PCM16 16k → 重采样到设备原生率
     ◀─BIN 0x11 Start
     ◀─TEXT status {answering}
     ◀─BIN 0x10 × N   （20ms/帧，间隔 15ms 节流）
     ◀─BIN 0x12 End
     ◀─TEXT status {answer_finished}
     ◀─TEXT status {listening}
```

打断：播放中收到 0x13 → LLM Control("interrupt") → status interrupted → listening。

## 3. 会话设计（session.go）

```go
type Session struct {
    ID     uint64
    conn   *websocket.Conn
    Device string            // hello.device_name，用作后端 session_id
    Codec  codec.Codec       // 协商后的编解码器（本会话唯一）
    wMu    sync.Mutex        // 所有 WS 写操作串行化（gorilla 不允许并发写）
    txSeq, audSeq uint32
}
```

- **单读协程**：`loop()` 唯一读者，文本/二进制分发；写可在多协程（ASR 回调、turn 协程），
  全部由 wMu 保护。
- **keepalive**：60s 读超时 + Pong 刷新。
- **结束路径**：读失败/bye → 关 codec → OnSessionEnd（关 ASR 流）→ 关连接。

## 4. 流水线设计（pipeline.go）

### 4.1 会话态

```go
type sessionState struct {
    asrStream mediator.AsrService_StreamingRecognizeClient
    asrCancel context.CancelFunc
    sid       string
    busy      bool   // 一轮对话互斥锁
}
```

### 4.2 后端契约（mediator.*）

| 服务 | 方法 | 用途 |
|---|---|---|
| AsrService | StreamingRecognize(stream {pcm,flags}) → (stream {text,is_final}) | 双工流识别，PCM16 16k 单声道 |
| LlmService | Generate({method:"answer",text,session_id}) → {text} | 一元，生成回答 |
| BusinessService | Control({cmd,session_id}) → {ack} | interrupt / config_update 透传 |
| TtsService | Synth({text,session_id}) → {pcm} | 一元合成，PCM16 16k 单声道 |

注意：ASR 硬性只收 16kHz；TTS 本地引擎固定出 16kHz —— **8k↔16k 重采样是网关责任**。

### 4.3 并发模型

- 每会话：1 个 WS 读协程（gateway）、1 个 ASR 接收协程（pipeline）。
- ASR is_final → `go runTurn(...)`，`busy` 标志防重入（忙则丢弃）。
- LLM/TTS 调用带 30s 超时；出错回 `error` 信封并恢复 listening。

### 4.4 关键取舍（测试网关级）

- TTS 用一元 Synth（整段合成完才播），不用流式——实现简单，首音延迟高但可接受。
- 下行帧 15ms 间隔节流（20ms 帧），避免设备环形缓冲溢出。
- ASR flags 默认 4（有声），可通过 `-asr-flags` 调整，联调时再定优。

## 5. 错误处理

| 场景 | 行为 |
|---|---|
| 不支持的 codec id | hello_err(UNSUPPORTED_CODEC) + 断开 |
| ASR 流打开失败 | 记日志，音频帧静默丢弃，会话其余功能可用 |
| LLM/TTS 错误 | error 信封 + 恢复 listening |
| WS 写失败（下行中） | 终止本轮，会话随连接断开回收 |
