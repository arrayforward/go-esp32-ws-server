# router — convai.v1 WebSocket 网关（Go）

ESP32-S3（goldie_esp32 固件）的测试网关。实现 `convai.v1` 线协议，
把设备音频接力到本地 ASR → LLM agent → TTS，并把合成语音按**设备相同格式**回发。

## 架构

```
ESP32-S3 ──WS convai.v1──▶ router
                             │ 解码(PCM16/G711A/G711U/IMA-ADPCM/Opus)
                             │ 重采样 →16k PCM16
                             ▼
                    ASR (mediator.AsrService, gRPC :50051)
                             │ 识别文本(is_final)
                             ▼
                    LLM agent feino (mediator.LlmService :50052)
                             │ answer 文本
                             ▼
                    TTS (mediator.TtsService :50061)
                             │ PCM16 16k
                             │ 重采样 →设备原生率 → 同格式编码
                             ▼
                    WS 二进制帧 0x11/0x10×N/0x12 回发设备
```

## 运行

```bash
# 依赖：Go 1.22+，libopus-dev，后端三个 gRPC 服务已启动
go build -o bin/router ./cmd/router

# 注意 feino 与 asr 默认都用 :50051，需错开（本网关默认 llm=:50052）
./bin/router \
  -listen :9000 \
  -asr 127.0.0.1:50051 \
  -llm 127.0.0.1:50052 \
  -tts 127.0.0.1:50061

# WSS（TLS）模式：同时提供证书与私钥即启用 wss://
# 证书请用 ECDSA P-256（端侧性能，见下），可用脚本生成：
./scripts/gen_cert.sh router.local
./bin/router -listen :9443 -tls-cert server_ca.pem -tls-key server.key
```

**端侧 TLS 性能设计**：ESP32-S3 算力有限，本网关 WSS 限定 MCU 友好的密码套件
（`ECDHE-ECDSA/RSA + AES-256/128-GCM`，优先 ECDSA）：

- **证书必须 ECDSA P-256 而非 RSA-2048**——Xtensa 无大数指令，ECDSA 握手快约 10 倍；
- **AES-GCM 套件**——ESP32-S3 有 AES 硬件加速，避免 ChaCha20（纯软件）；
- 自签证书把 `server_ca.pem` 拷到设备工程 `components/convai_ws/certs/` 即可被固件嵌入验签；
  IP/CN 不匹配时设备端 menuconfig 打开 `CONVAI_WSS_SKIP_CN_CHECK`。

环境变量可替代命令行：`ROUTER_LISTEN` / `ASR_ADDR` / `LLM_ADDR` / `TTS_ADDR` /
`ROUTER_TLS_CERT` / `ROUTER_TLS_KEY`。

设备端（goldie_esp32）menuconfig 设置 `GOLDIE_SERVER_URL=ws://<网关IP>:9000/`。

## 协议实现要点

- WS 子协议 `convai.v1`；hello 鉴权目前放开（测试网关，接受任意 product 凭证）。
- 文本信封 `{type,seq,ts,body}`；支持 hello/hello_ack、status、event、text、
  ping/pong、bye、config_update（透传给 feino BusinessService/Control 并回 ack）。
- 二进制音频帧：13 字节大端头（op/seq/ts）+ 编码负载。
  0x10 Frame / 0x11 Start / 0x12 End / 0x13 Cancel（barge-in → feino interrupt）。
- 对话轮次：ASR `is_final` 非空 → thinking → LLM answer → text 回发 →
  TTS → answering → 音频帧（20ms/帧，15ms 间隔节流）→ answer_finished → listening。
  播放中收到 0x13 或新语音可打断（单轮互斥，忙时丢弃新轮次）。

## 编解码

| id | 格式 | 原生率 | 实现 |
|---|---|---|---|
| 0 | PCM16 | 8k | 直通 |
| 1 | G.711A | 8k | 查表（与 ESP32 同算法，静音=0xD5） |
| 2 | G.711U | 8k | 查表（静音=0xFF） |
| 3 | IMA-ADPCM | 8k | 89 级步长表，低半字节在前 |
| 4 | Opus | 16k | cgo libopus，16kbps CBR 复杂度1 |

重采样：8k↔16k 线性插值/对均（测试网关级别精度）。

## 测试

```bash
go test ./internal/codec/ -v     # 7 项：注册表、PCM、G711A/U、ADPCM、Opus、重采样
go build ./...

# mock 后端 + 端云 E2E（配合 ai_ws_esp32/e2e_tests）
go build -o bin/mockbackends ./cmd/mockbackends
./bin/mockbackends -asr :51051 -llm :51052 -tts :51061 &
./bin/router -listen :9000 -asr 127.0.0.1:51051 -llm 127.0.0.1:51052 -tts 127.0.0.1:51061 &
# 然后在 ai_ws_esp32/e2e_tests 构建运行设备模拟端（linux target）
```

测试向量与 ESP32 端 `host_tests` 一致（静音码、误差容差、Opus 稳态能量比）。
`cmd/mockbackends` 提供 canned 版 ASR（每 500ms 出一句）/LLM（固定回复）/
TTS（0.8s 正弦）mediator.* 服务，用于无真实后端的集成联调。

## 待办（生产化）

- hello 鉴权（校验 product_key/secret）
- TTS 流式输出（当前一元 Synth，整段合成后才开始回发）
- 服务端 VAD 断句参数（-asr-flags）调优；打断时停止在途 TTS 帧队列
- WSS/TLS
