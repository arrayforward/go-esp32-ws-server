# 04 - 实现指南（Implementation Guide）

> SDD 阶段：任务（Tasks）。按本文可从零复现本网关。

## 1. 环境准备（WSL2 Ubuntu 22.04）

```bash
# Go 1.22+（go.dev 被墙时用国内镜像）
curl -sL -o /tmp/go.tgz https://golang.google.cn/dl/go1.22.12.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf /tmp/go.tgz
export PATH=$PATH:/usr/local/go/bin:~/go/bin
export GOPROXY=https://goproxy.cn,direct        # 模块代理，必须

# protoc + Go 插件 + opus 开发库
sudo apt-get install -y protobuf-compiler libopus-dev pkg-config
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
```

## 2. 生成 gRPC stub

三个后端 proto（都属 package mediator，消息无重名，可生成到同一 Go 包）：

```bash
R=/mnt/d/dev/router
mkdir -p $R/proto $R/gen/mediator
cp /mnt/d/vit/asr/proto/mediator_asr.proto $R/proto/
cp /mnt/d/vit/tts/proto/mediator_tts.proto $R/proto/
cp /mnt/d/agent/feino/proto/agent.proto   $R/proto/
# 删除原 go_package（agent.proto 指向 feino 仓库），统一插入：
#   option go_package = "router/gen/mediator;mediator";
protoc --go_out=$R --go-grpc_out=$R -I $R/proto \
  $R/proto/mediator_asr.proto $R/proto/mediator_tts.proto $R/proto/agent.proto
# 产物落在 $R/router/gen/mediator 时移动到 $R/gen/mediator
```

后端契约要点（复现时必须遵守）：

- `mediator.AsrService/StreamingRecognize`：双工流；`AsrRequest{pcm, flags}`，
  pcm 为 PCM16 LE 16kHz 单声道；`AsrResponse{text, is_final}`。
- `mediator.LlmService/Generate`：一元；method="answer"；feino 需 `MINIMAX_API_KEY`，
  且**与 ASR 默认同端口 :50051，必须用 GRPC_ADDR 错开**（本网关默认 :50052）。
- `mediator.BusinessService/Control`：cmd="interrupt"（打断）；cmd=config_update 原文（人设透传）。
- `mediator.TtsService/Synth`：一元；返回 PCM16 LE 16kHz 单声道。

## 3. 文件清单与职责

```
router/
├── go.mod                          # module router; 依赖 gorilla/websocket, grpc, protobuf
├── cmd/router/main.go              # 旗标/环境变量 → pipeline.New → gateway.Server.Run
├── proto/                          # 三份后端 proto 的副本（生成用）
├── gen/mediator/*.pb.go            # protoc 产物（勿手改）
├── internal/
│   ├── codec/
│   │   ├── codec.go                # Codec 接口 + id 常量 + New() 工厂
│   │   ├── pcm.go                  # 直通
│   │   ├── g711a.go                # A-law（与 ESP32 同算法：段码+0xD5/0x55 掩码）
│   │   ├── g711u.go                # μ-law（BIAS=132 CLIP=32635 seg_end 表）
│   │   ├── adpcm.go                # IMA-ADPCM（89 步长表/16 索引表，enc/dec 双状态）
│   │   ├── opus.go                 # cgo libopus：16k/mono/16kbps/CBR/复杂度1
│   │   ├── resample.go             # 8k↔16k 线性插值/对均，16k 恒等直通
│   │   └── codec_test.go           # 7 项自测（与 ESP32 host_tests 同向量）
│   ├── gateway/
│   │   ├── protocol.go             # 信封 Build/Parse + 13B 音频头 pack/unpack + hello/status 常量
│   │   ├── server.go               # HTTP→WS upgrade（子协议 convai.v1）+ Pipeline 接口定义
│   │   └── session.go              # 会话：读写并发控制、消息分发、hello 协商、音频帧入口
│   └── pipeline/
│       └── pipeline.go             # gRPC 拨号、ASR 流管理、runTurn（LLM→TTS→编码回发）
├── docs/                           # 本文档库
└── README.md
```

## 4. 关键实现片段（照抄级）

### 4.1 WS 服务端（gorilla/websocket）

```go
var upgrader = websocket.Upgrader{
    Subprotocols: []string{"convai.v1"},              // 设备要求该子协议
    CheckOrigin:  func(r *http.Request) bool { return true },
}
// 注意：gorilla 连接不允许并发写 → 所有写方法共用一把 sync.Mutex（Session.wMu）
```

### 4.2 Opus cgo 封装（容易踩的坑）

- 类型名是 `OpusEncoder/OpusDecoder`（不是 opus_encoder_t）。
- `OPUS_SET_BITRATE(...)` 等是**宏**（变参 ctl 的包装），cgo 不能直接调，
  必须在 C 前言里写普通函数封装：

```c
static int gw_enc_ctl(OpusEncoder *st, int bitrate, int complexity) {
    int rc = opus_encoder_ctl(st, OPUS_SET_BITRATE(bitrate));
    if (rc != OPUS_OK) return rc;
    rc = opus_encoder_ctl(st, OPUS_SET_COMPLEXITY(complexity));
    if (rc != OPUS_OK) return rc;
    return opus_encoder_ctl(st, OPUS_SET_VBR(0));
}
```

- Go 切片与 C 指针互转用 `unsafe.Pointer(&pcm[0])`，空切片先判长度。

### 4.3 一轮对话（runTurn）骨架

```go
s.SendStatus("thinking")
llm := mediator.NewLlmServiceClient(p.llmConn)
resp, err := llm.Generate(ctx, &mediator.LlmRequest{Method:"answer", Text:text, SessionId:s.Device})
s.SendAIReply(resp.Text)
tts := mediator.NewTtsServiceClient(p.ttsConn)
audio, err := tts.Synth(ctx, &mediator.TtsRequest{Text:resp.Text, SessionId:s.Device})
pcm := codec.ResampleFrom16k(bytesToPCM(audio.Pcm), s.Codec.SampleRate())
s.SendAudioOp(0x11); s.SendStatus("answering")
for 帧 in 按20ms切片(尾帧补零) { s.SendAudioFrame(s.Codec.Encode(帧)); time.Sleep(15ms) }
s.SendAudioOp(0x12); s.SendStatus("answer_finished"); s.SendStatus("listening")
```

## 5. 构建与测试

```bash
cd /mnt/d/dev/router
go mod tidy
go build ./...                  # 全量编译
go test ./internal/codec/ -v    # 7 项编解码自测
go build -o bin/router ./cmd/router
```

## 6. 已知坑

1. **go.dev 访问失败** → 用 golang.google.cn / goproxy.cn。
2. **feino 与 ASR 同端口 50051** → 启 feino 前 `export GRPC_ADDR=:50052`。
3. **ASR 只收 16kHz**（其他直接报错）、TTS 本地只出 16kHz → 重采样必须在网关做。
4. **cgo 宏不可调**（OPUS_SET_*）→ C 函数封装（见 4.2）。
5. **gorilla/websocket 并发写 panic** → 全局写锁。
6. **ADPCM 状态共享** → 必须每会话实例，否则跨会话解码出噪声。
7. **有损格式测试**：Opus 有编解码时延，禁止逐样本断言，用多帧稳态能量比。
8. **下行洪峰**：TTS 整段拿到后若不节流会瞬间灌爆设备缓冲 → 15ms/帧节流 + 设备端 PRIMING。
9. **WSS**：网关用 `-tls-cert/-tls-key` 启 TLS；设备端 esp_websocket_client 用 wss 需配置
   CA（自签证书须嵌入固件）。生产更推荐明文 ws + 前置 TLS 终结反代。

## 7. 端到端联调步骤

```bash
# 1) 后端（WSL 内）
/mnt/d/vit/asr/build/asr_server ...          # :50051
cd /mnt/d/agent/feino && source config.env && GRPC_ADDR=:50052 ./bin/feino-server
/mnt/d/vit/tts/build/tts_server              # :50061（需 TTS_LOCAL_* 环境变量）

# 2) 网关
./bin/router -listen :9000

# 3) 设备
# goldie_esp32: idf.py menuconfig → GOLDIE_WIFI_SSID/PASSWORD、
# GOLDIE_SERVER_URL=ws://<网关IP>:9000/ → idf.py flash monitor
# 期望日志：hello → hello_ack → listening → 说话 → thinking → text → answering → 播放
```
