# 01 - 需求规格：convai.v1 WebSocket 网关（Spec）

> SDD 阶段：规格（What & Why）。只描述"做什么"，不涉及"怎么做"。

## 1. 背景

ESP32-S3 端侧固件（goldie_esp32）已实现 convai.v1 设备端协议（WebSocket 子协议，
JSON 文本信封 + 13 字节头二进制音频帧，5 种可协商音频编解码）。
为对端侧做端到端功能测试，需要一个网关：

- 扮演 cloud_gateway 角色与设备握手、收发音频；
- 把设备语音接力到本地识别/对话/合成服务（ASR → LLM → TTS）；
- 将 AI 语音以**与设备上行完全相同的编解码格式**回发，验证设备解码播放链路。

## 2. 术语

| 术语 | 含义 |
|---|---|
| convai.v1 | 设备↔网关的 WS 线协议（文本信封 + 二进制音频帧） |
| 上行 | 设备 → 网关（mic 音频） |
| 下行 | 网关 → 设备（TTS 音频） |
| 一轮对话（turn） | ASR 出最终文本 → LLM 生成回复 → TTS 播完的完整过程 |
| barge-in | 用户在 AI 播放语音时打断（设备发 0x13 Cancel） |
| mediator 契约 | 三个后端服务提供的简化 gRPC 接口（mediator.*） |

## 3. 功能需求

### FR-1 协议接入

- 监听 WS（默认 `:9000`），要求子协议 `convai.v1`。
- 实现消息：hello/hello_ack、hello_err、status、event、text、ping/pong、bye、
  config_update/config_update_ack、error、function_call_output（接收记录）。
- 二进制音频帧：13 字节大端头（u8 op | u32 seq | u64 ts_ms）+ 编码负载；
  op = 0x10 Frame / 0x11 Start / 0x12 End / 0x13 Cancel。
- 测试阶段 hello 不鉴权（接受任意 product 凭证），不支持格式回 hello_err。

### FR-2 全格式音频编解码

网关必须支持设备端全部 5 种格式（id 与 hello.audio_codec 一致）：

| id | 格式 | 原生采样率 | 要求 |
|---|---|---|---|
| 0 | PCM16 | 8kHz | 直通 |
| 1 | G.711A | 8kHz | 与设备同算法（静音=0xD5） |
| 2 | G.711U | 8kHz | 静音=0xFF |
| 3 | IMA-ADPCM | 8kHz | 4:1，低半字节在前，状态跨帧 |
| 4 | Opus | 16kHz | libopus，16kbps CBR 复杂度 1 |

### FR-3 对话流水线

```
上行帧 → 解码 → 重采样 16k PCM16 → ASR 流式识别
ASR is_final(非空) → LLM 生成回答(method=answer) → TTS 合成(PCM16 16k)
→ 重采样回设备原生率 → 同格式编码 → 下行帧(20ms/帧)
```

- 一轮对话状态序列：thinking →（text 回发）→ answering → answer_finished → listening。
- 下行音频按 op 序列：0x11 Start → 0x10 Frame×N → 0x12 End，发送节奏接近实时。
- 单轮互斥：一轮未完成时新到的最终文本丢弃（记日志），不并发生成。

### FR-4 打断（barge-in）

收到 0x13 Cancel：转发 LLM 服务的 interrupt 指令（取消在途生成），
设备侧状态：interrupted → listening。

### FR-5 配置透传

设备的 `config_update`（人设/音色 JSON）原样转发给 LLM 服务的业务控制接口，
回复 `config_update_ack`。

## 4. 非功能需求

- **格式一致性**：同一会话内下行格式 == 上行协商格式（这是核心验收点）。
- **可测试**：编解码与重采样为独立包，测试向量与 ESP32 端 `host_tests` 一致。
- **配置外置**：监听地址、三个后端地址均可命令行/环境变量配置，不写死。
- **资源回收**：会话结束释放 codec 实例与 ASR 流。

## 5. 验收标准

| # | 验收项 | 验证方式 | 状态 |
|---|---|---|---|
| AC-1 | 5 种格式可实例化、可查询 | TestRegistry | ✅ |
| AC-2 | PCM16 往返完全一致 | TestPCM16Roundtrip | ✅ |
| AC-3 | G.711A 静音 0xD5 / 0xD5→8；G.711U 静音 0xFF；往返保号限差 | TestG711* | ✅ |
| AC-4 | ADPCM 160 样本→80 字节（4:1）；正弦往返有界误差 | TestADPCM* | ✅ |
| AC-5 | Opus 10 帧流往返帧长正确、稳态能量比 ≈1.0 | TestOpusRoundtrip | ✅ |
| AC-6 | 8k↔16k 重采样样本数与数值正确 | TestResample | ✅ |
| AC-7 | 整体编译通过 | go build ./... | ✅ |
| AC-8 | 端到端：设备 hello→说话→听到同格式回复 | 上板联调 | ⏳ 待设备接入 |
