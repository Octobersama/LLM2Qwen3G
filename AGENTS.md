# Repository Guidelines

## Project Overview

LLM2Qwen3Guard 是一个零依赖 Go 网关：对外暴露 OpenAI 兼容的 `POST /v1/chat/completions`，对内调用任意 OpenAI 兼容上游 LLM（json_schema/json_object 结构化输出），经本地校验后把结果渲染成 **Qwen3Guard-Gen 官方两行文本合同**（`Safety: <token>\nCategories: <tokens|None>`），供 sub2api 的提示词审计节点（prompt-input-audit）零改动接入。协议结论的权威出处是 `DESIGN.md`（每条附源码/文档链接），冲突时以运行时代码 > 官方 README/模型卡 > 博客为准。

## Architecture & Data Flow

模块依赖为严格无环树（`go.mod` 零第三方依赖，仅 stdlib）：

```
cmd/gateway ──► internal/server ──► internal/upstream ──► internal/qwen3guard
                    │      │                              ▲
                    │      └──────────────────────────────►│
                    ▼
             internal/config（叶子，被 cmd/server/upstream 引用，不引任何内部包）
```

单请求流水线（`internal/server/server.go` `chat`）：

1. 可选 Bearer 鉴权（`GATEWAY_API_KEY`，401）
2. `http.MaxBytesReader` 请求体上限（`MAX_REQUEST_BYTES` 默认 1MiB，超限 413）
3. 解码 → 取**最后一条 user 消息**文本（string 或 content 数组的 text 部件拼接）→ `MAX_INPUT_CHARS` rune 截断
4. `upstream.Client.Do`：system=`SystemPolicy(PolicyAppendix)`（模型卡政策原文 + 可选运营者侧重附录 + 网关自撰 JSON 指令，JSON 指令固定末尾）+ user=待审文本，`response_format` 按 `STRUCTURED_OUTPUT_MODE` 协商；`auto` 先 `json_schema`，遇 4xx（非 401/403/429）降级 `json_object` 重试一次，**绝无自由文本回退**。注意：schema 不带 `uniqueItems`（DashScope 拒绝数组+uniqueItems，去重由本地校验强制）
5. `ParseUpstreamJSON`（容忍 markdown 栅栏/包裹文本）→ `ValidateVerdict`（safety 枚举、类目别名规范化到官方 9 token、Safe⇒无类目 / 非Safe⇒≥1类目）——**这是 appendix 无法绕过的合同边界**
6. `Render` 两行文本 → 非 stream 输出 chat.completion 信封（透传 usage），stream 输出 3 帧 SSE（role → content → finish_reason:"stop"）+ `[DONE]`
7. 任何上游/解析/校验失败走 `FAILURE_POLICY`（默认 `error`=503 fail-closed，与 sub2api 语义对齐；可选 `safe`/`unsafe`）；503 对外**固定文案**（"guard pipeline failure"），绝不透出上游响应体
8. 每请求经 `internal/logsys` 双通道记审计事件：stdout 常开（Docker/journald）+ 文件 `logs/gateway-YYYYMMDD.jsonl` 按日轮转（`LOG_DIR=off` 关闭）。字段白名单：request_id/text_chars/stream/model/base_url/api_key(脱敏)/mode/status/latency_ms/safety/categories——**绝不记录**待审文本、messages、上游原始 JSON、appendix 内容

入口 `cmd/gateway/main.go`：`config.FromEnv`（失败即 fatal，含 appendix 文件读取与 LOG_DIR 可写性检查）→ `logsys.New` → `http.Server` → SIGINT/SIGTERM 优雅关停（10s）；`-healthcheck` flag 供容器探针。

## Key Directories

| 路径 | 职责 |
|---|---|
| `cmd/gateway/` | main：装配 config + server，优雅关停 |
| `internal/config/` | 环境变量解析与**启动期全量校验**（FromEnv + envInt/envFloat/envOr） |
| `internal/server/` | HTTP 编排：路由/鉴权/请求体大小限制/截断/失败策略/SSE/审计日志事件 |
| `internal/logsys/` | 双通道结构化日志（stdout + 按日轮转 JSONL 文件）、API key 脱敏 `Redact`、日志字段白名单在此强制 |
| （无 dist/） | 二进制不入库；经 [GitHub Releases](https://github.com/Octobersama/LLM2Qwen3G/releases) 分发（v0.1.1+），本地构建走 `-buildvcs=false` |
| `_research/` | gitignore 的调研原始快照（sub2api 源码、智谱 OpenAPI、HF chat_template）——勿删勿提交 |
| `Dockerfile` + `docker-compose.yml` + `.env.docker.example` + `.dockerignore` | 容器部署（多阶段：golang:1.25-alpine 构建 → distroless/static:nonroot 运行；compose 注入 env_file，健康探针用内置 `-healthcheck`——distroless 无 shell，不能改用 curl/wget；`.dockerignore` 防敏感文件入构建上下文）；本机无 Docker，未实测 |
| `DESIGN.md` | 带出处的协议合同（改动协议前必读） |

## Development Commands

```bash
go run ./cmd/gateway          # 本地运行（需环境变量，见 .env.example；网关不读取 .env 文件本身）
go vet ./... && go test ./... # 静态检查 + 全量测试（改代码后必跑）

# 发布二进制（构建到临时目录并上传 GitHub Releases；-buildvcs=false 保证可复现）
GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-s -w" -o /tmp/release/gateway-windows-amd64.exe ./cmd/gateway
GOOS=linux   GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-s -w" -o /tmp/release/gateway-linux-amd64   ./cmd/gateway

# 冒烟探测
curl -X POST http://127.0.0.1:8080/v1/chat/completions -H "Content-Type: application/json" \
  -d '{"model":"any","messages":[{"role":"user","content":"How can I make a bomb?"}]}'
# 期望 content == "Safety: Unsafe\nCategories: Violent"
```

gofmt 是唯一格式器：提交前 `gofmt -l .` 必须为空。

## Code Conventions & Common Patterns

- **协议常量单源**：Qwen3Guard token、类目、别名映射只在 `internal/qwen3guard/contract.go` 定义一次，json_schema 生成（`client.go`）复用同一 slice——新增/修改类目只改这一处，`TestOfficialCategoryLists` 会钉死拼写与顺序。
- **注释必须附出处**：任何协议/兼容性断言旁标注原始 URL（Qwen HF/GitHub、`raw.githubusercontent.com/Wei-Shaw/sub2api/...`、OpenRouter/SiliconFlow/Zhipu 文档），不许只写"见 DESIGN.md"。设计意图性的"不做"也要注明（如 response 审计未实现、chat template 不复刻）。
- **错误处理**：上游错误统一 `*UpstreamError{Status, Mode, Err}`（降级判定直接读 Status）；其余用 `fmt.Errorf`，可包装时 `%w`；边界处 `errors.As`（MaxBytesError→413）/`errors.Is`（ErrServerClosed）。无 sentinel error 变量。
- **配置即契约**：所有 env 校验集中在 `config.FromEnv`，下游拿到 Config 视为已验证（`upstream.NewClient` 只做字段拷贝）；新增配置项沿用 envInt/envFloat/envOr + 集中式数值/枚举校验。
- **依赖注入**：生产路径 `server.New(cfg)` 内部 `upstream.NewClient(cfg)`；测试用 `server.NewWithClient(cfg, client)` 注入桩客户端。
- **分层纪律**：域逻辑进 `qwen3guard`（纯函数），出站 HTTP 进 `upstream`，请求编排进 `server`；`config`/`qwen3guard` 是叶子包，禁止反向依赖。
- **测试内注释引用供应商文档**（如 strict 默认省略的原因），保证期望值可溯源。

## Important Files

| 文件 | 说明 |
|---|---|
| `cmd/gateway/main.go` | 唯一入口 |
| `internal/qwen3guard/contract.go` | 协议核心：token/别名/校验/渲染/系统提示词 |
| `internal/upstream/client.go` | 降级链与上游请求形状 |
| `internal/server/server.go` | 对外 API 与失败策略 |
| `internal/config/config.go` | 全部环境变量及默认值（权威表） |
| `.env.example` | 配置模板（网关不自动加载 .env，需 shell/systemd 注入） |
| `.gitignore` | 保护本地 key 文件与 `_research/`；`/gateway` 根锚定避免误伤 `cmd/gateway/` |

## Runtime/Tooling Preferences

- **Go 1.25**（`go.mod`），**零第三方依赖**——新功能优先 stdlib；引入依赖需极强理由。
- 开发机 Windows（PowerShell：`$env:VAR="..."`），生产 Linux amd64（systemd + EnvironmentFile）；无 Python/Node 参与。
- 日志走 stderr `log.Printf`（每请求一行：upstream_mode/latency/outcome）。

## Testing & QA

- 纯 stdlib `testing` + `httptest`（桩上游返回固定 choices JSON），表驱动 + `t.Run` 子测试；确定性、无外部网络。
- 关键测试语义：
  - `contract_test.go`：渲染黄金串、别名矩阵、9 token 官方表、ValidateVerdict 不变量
  - `sub2api_compat_test.go`：**刻意逐字镜像** sub2api 的 ParseQwen3Guard 做 Render 往返验证——是兼容性证据，勿当重复代码删除；上游 sub2api 变更时需手工同步
  - `upstream_test.go`：降级顺序（位置断言 bodies[0]/bodies[1]）、strict 显式开启才发送、401/429/5xx 不降级、endpointURL 保路径
  - `server_test.go`：端到端、SSE 4 帧计数+stop 帧、失败策略三态、413（注入小上限，勿分配 1MiB+ 大 body）
- 已知未覆盖（改动相关区域时补）：`internal/config` 无测试（枚举/数值校验）、`server.New` 生产构造、`/healthz` 路由、流式下的失败策略、usage 透传断言。

## 提交纪律

- `测试api key信息.txt`（真实 key）与 `_research/` 永不入库；提交前 `git status --short` 自查。
- 改协议相关代码（token/渲染/解析/降级）必须：更新 `DESIGN.md` 对应结论 + 出处 → 跑全量测试 → 新版本构建双平台二进制发布到 GitHub Releases（打 tag `vX.Y.Z`），不提交进仓库。
