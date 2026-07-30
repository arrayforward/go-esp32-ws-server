# router 网关研发文档（Spec-Driven Development）

convai.v1 WebSocket 测试网关（Go 实现）的设计文档库。
目标：让不了解上下文的开发者（或 AI）仅凭本目录文档即可完整复现本网关。

## 文档索引

| 文档 | 内容 | SDD 阶段 |
|---|---|---|
| [01-spec-gateway.md](01-spec-gateway.md) | 需求规格：网关要做什么、验收标准 | Spec |
| [02-design-gateway.md](02-design-gateway.md) | 技术设计：架构、协议实现、会话状态机、流水线时序 | Plan |
| [03-codec-business-design.md](03-codec-business-design.md) | **编解码器选择与调用的业务设计**（协商→选择→调用→回编全流程） | Plan |
| [04-implementation-guide.md](04-implementation-guide.md) | 实现指南：环境、proto 生成、文件清单、构建测试、已知坑 | Tasks |

## 工程概况

- **语言**：Go 1.22（cgo 调 libopus）
- **功能**：ESP32-S3（goldie_esp32 固件）的 convai.v1 网关；
  设备音频解码 → ASR → LLM agent → TTS → 同格式编码回发
- **后端**：三个本地 gRPC 服务（D:\vit\asr、D:\agent\feino、D:\vit\tts 的 mediator 契约）
- **验证状态**：`go build ./...` 通过；`go test ./internal/codec/` 7/7 通过

## 快速验证

```bash
cd /mnt/d/dev/router
go test ./internal/codec/ -v     # 编解码自测（与 ESP32 端同向量）
go build -o bin/router ./cmd/router
./bin/router -listen :9000 -asr 127.0.0.1:50051 -llm 127.0.0.1:50052 -tts 127.0.0.1:50061
```
