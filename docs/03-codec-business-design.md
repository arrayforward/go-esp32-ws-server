# 03 - 编解码器选择与调用的业务设计（Codec Business Design）

> 本文是网关侧"选哪个编解码器、在哪里调用、状态怎么管"的完整业务设计，
> 配合代码 `internal/codec/` + `internal/gateway/session.go` + `internal/pipeline/pipeline.go` 阅读。

## 1. 业务约束（输入条件）

1. 设备上行格式**由设备在 hello 中声明**（`audio_codec: 0..4`），网关必须适配，不能强求。
2. 后端 ASR/TTS **只懂 PCM16 16kHz 单声道**（ASR 硬校验 16k；TTS 本地引擎固定 16k 输出）。
3. 下行格式**必须与上行相同**——这是端到端验证设备解码链路的业务目标。
4. 设备端各格式的原生采样率不同：PCM16/G711A/G711U/ADPCM = 8kHz，Opus = 16kHz。

由 2+4 推出网关必备两个能力：**格式转换（编解码）** 和 **采样率转换（8k↔16k 重采样）**。

## 2. 编解码器选择流程（协商）

```
                    ┌─────────────────────────────┐
设备 hello ─────────▶│ audio_codec id (0..4)       │
                    │  + sample_rate (信息性)      │
                    └──────────┬──────────────────┘
                               ▼ codec.New(id)
                    ┌──────────────────────────────┐
                    │ 成功: session.Codec = 实例    │──▶ hello_ack.audio_config.codec = name
                    │       （双向都使用它）         │    （回告设备网关实际使用的格式名）
                    │ 失败: hello_err + 断开       │
                    └──────────────────────────────┘
```

- **选择时机**：hello 处理时一次性确定，存于 `Session.Codec`，整个会话生命周期不变。
  （设备端支持运行时 `convai_set_codec` 切换，但协议上切换发生在重连/hello 时；
  本网关不做会话中切换——如需支持，监听 config_update 中的 codec 字段重建实例即可。）
- **选择实现**：`codec.New(id)` 工厂按 id 返回实现 `Codec` 接口的实例：

```go
type Codec interface {
    ID() int
    Name() string
    SampleRate() int                 // 8k 或 16k —— 决定是否需要重采样
    Encode(pcm []int16) ([]byte, error)
    Decode(enc []byte) ([]int16, error)
    Close()                          // Opus 释放 libopus 句柄；其余空实现
}
```

注册表（`codec.go`）：

| id | 构造 | 原生率 | 实例状态 |
|---|---|---|---|
| 0 | pcm16Codec{}（值类型） | 8k | 无 |
| 1 | g711aCodec{}（值类型） | 8k | 无 |
| 2 | g711uCodec{}（值类型） | 8k | 无 |
| 3 | &adpcmCodec{} | 8k | **有**：enc/dec 各一份 {predictor, stepIndex, 半字节缓冲} |
| 4 | newOpusCodec() | 16k | **有**：libopus 编码器+解码器句柄（cgo） |

设计要点：
- 无状态格式用**值类型**（零分配、可共享）；有状态格式必须**每会话独立实例**——
  ADPCM 是增量编码，跨会话共享状态会产生噪声；Opus 句柄不可并发共用。
- Opus 创建失败（libopus 缺失）时 hello_err，网关仍可为其他格式服务。

## 3. 上行调用点（设备 → ASR）

位置：`session.go handleBinary()`（WS 读协程内，每帧一次）

```
BIN 帧
 ├─ op=0x10 Frame:
 │    pcm_native = session.Codec.Decode(payload)        // ① 格式解码（原生率）
 │    pcm16k     = codec.ResampleTo16k(pcm_native,
 │                     session.Codec.SampleRate())       // ② 采样率转换（8k→16k，16k 直通）
 │    pipeline.OnAudio(session, pcm16k)                  // ③ 转 PCM16 LE bytes → ASR 流
 ├─ op=0x11/0x12: 忽略（本网关用服务端 VAD，设备侧 VAD 标记不采用）
 └─ op=0x13 Cancel: pipeline.OnCancel → LLM interrupt
```

要点：
- ①②在 WS 读协程内同步执行——5 种格式单帧解码都是微秒级，不阻塞读循环。
- ADPCM 的 Decode 使用**会话级 dec 状态**（跨帧连续），不能用临时实例。
- ②是恒等优化：Opus 原生 16k 时 ResampleTo16k 直接返回原切片，零拷贝。

## 4. 下行调用点（TTS → 设备）

位置：`pipeline.go runTurn()`（turn 协程内，一轮一次）

```
ttsResp.pcm (PCM16 16k LE)
 → pcm = bytesToPCM()
 → pcm = codec.ResampleFrom16k(pcm, session.Codec.SampleRate())   // ① 16k→原生率（16k 直通）
 → SendAudioOp(0x11 Start) + status answering
 → 按 frameSamples = SampleRate/50 (20ms) 切片:
      尾帧不足 → 补零凑整
      pkt = session.Codec.Encode(frame)                           // ② 格式编码（原生率）
      SendAudioFrame(pkt);  sleep 15ms                            // ③ 节流
 → SendAudioOp(0x12 End) + status answer_finished + listening
```

要点：
- ②编码使用**会话级 enc 状态**（ADPCM 跨帧连续；Opus CBR 每帧独立但句柄复用）。
- ③15ms 间隔 + 20ms 帧 = 1.33 倍实时速度内推送，防止设备环形缓冲溢出；
  设备侧有 PRIMING 缓冲（160ms）吸收抖动。
- 尾帧补零保证解码端帧长一致（ADPCM 奇数样本会多解出一个样本，补零无害）。

## 5. 状态与生命周期管理

```
会话建立(hello)     会话运行                会话结束
codec.New(id)  →   每帧 Decode（上行）      Codec.Close()
                   每轮 Encode（下行）   →   ASR 流 CloseSend/cancel
                   （enc/dec 状态自持）      conn.Close
```

- **每会话一套**：Session.Codec 在 hello 时创建、disconnect 时 Close，绝不共享。
- **并发安全**：上行 Decode 只在 WS 读协程；下行 Encode 只在 turn 协程；
  同一 Codec 实例的 enc/dec 状态字段互不干扰（ADPCM 结构上分离 enc/dec；
  Opus 编码器/解码器是不同句柄）。busy 标志保证同一时刻只有一个 turn 在 Encode。
- **资源对称**：newOpusCodec 失败要回滚已建句柄；Close 幂等（指针置 nil）。

## 6. 重采样业务规则

| 方向 | 规则 | 实现 |
|---|---|---|
| 上行 8k→16k | 线性插值：out[2i]=in[i], out[2i+1]=(in[i]+in[i+1])/2 | ResampleTo16k |
| 下行 16k→8k | 对均抽取：out[i]=(in[2i]+in[2i+1])/2 | ResampleFrom16k |
| 原生率=16k | 恒等直通（零拷贝） | 同上函数内分支 |

测试网关精度足够；生产应换多相滤波器（如 libsamplerate/自实现 polyphase）。

## 7. 一致性验证（两端向量对齐）

网关 `internal/codec/codec_test.go` 与 ESP32 `host_tests/main/test_main.c` 使用**相同判据**：

| 项 | 判据 |
|---|---|
| G.711A | 静音=0xD5，0xD5→8，往返保号、误差 ≤ 40+\|x\|/8 |
| G.711U | 静音=0xFF，误差 ≤ 200+\|x\|/8 |
| IMA-ADPCM | 160 样本→80 字节；16 帧后误差 ≤3000 |
| Opus | 10 帧流，跳 5 帧预热，稳态能量比 ∈(0.5,1.5)（有损+时延，禁止逐样本比） |

意义：任一端改算法，两端测试必有一边变红，防止"网关能播、设备播不了"的隐性不一致。

## 8. 扩展新格式的步骤

1. `internal/codec/` 新增 `xxx.go` 实现 `Codec` 接口（有状态则每会话实例）。
2. `codec.go New()` 注册 id（与设备端 `convai_codec_id_e` 同步编号）。
3. 设备端同步实现同 id 格式（components/convai_ws）。
4. 两端各加同向量测试；联调 hello(audio_codec=新id)。
