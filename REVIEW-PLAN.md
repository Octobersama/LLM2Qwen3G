# REVIEW-PLAN.md — main 全库可维护性审查与整改计划

日期：2026-09-07 · 基线 commit：`ceb8869` · 审查范围：main 全部 Go 源码 + 测试 + 文档

---

## 一、审查结论

### 发现（按严重度）

| # | 级别 | 位置 | 问题 | 整改 |
|---|---|---|---|---|
| B1 | 高 | `server.go chat()` | `fields["status"] = N` 散布在 8 个 early-return 分支——每加一个错误分支都要手工记住记录状态，漏一处审计日志就撒谎（状态缺失/错误） | 收敛到 `respondError(w, fields, status, msg)`：写响应+记状态原子化，删除全部散布赋值 |
| B2 | 高 | `server.go` | `verdict = Safety + "/" + join(...)` 打包成字符串，defer 里 `splitVerdict` 再解包——把结构化数据压扁又拆开，纯属往返浪费 | 直接持有 safety/categories 两个变量；删除 `splitVerdict` |
| B3 | 中 | `server.go failure()` | `fields["request_id"].(string)` 类型断言——内部契约靠 map 断言，未来调用方漏放即 panic | request_id 改显式参数传递 |
| B4 | 中 | `server.go writeCompletion()` | SSE 注释断言 "sub2api's OpenAI clients expect the stop marker" 无出处（此前审查已指出过一次未修），违反本仓库"注释必须指认真实出处"纪律 | 改述为 OpenAI 官方 streaming 合同（不点名 sub2api），或直接删除断言子句 |
| B5 | 中 | `server_test.go` 全部 + `server.New` 注释 | ① 所有测试传 `nil` logger → server.New 静默装 stdout info logger → 每个测试请求向 stdout 打 audit 行（噪音污染测试输出）；② `New` 注释称 "logger may be nil (logging disabled)" 与实际（nil = stdout 默认，非禁用）矛盾 | ~~测试助手统一传 `logsys.New("", "error")`（level 过滤掉 info）~~ 原方案（见 Phase 3 实施修订）：最终实现为 `logsys.NewDiscard()`；修正注释如实描述 |
| B6 | 中 | `config.go:11-12` | 注释称 "Environment names and defaults are defined by DESIGN.md section 4/5"——DESIGN §4/§5 并不包含 env 清单/默认值表（README 配置表与 `.env.example` 才是）——注释指向不存在的权威 | 改指 README 配置表 + `.env.example` |
| B7 | 中 | `internal/config`（AGENTS.md 自我声明缺口） | config 包零测试：必填三项、枚举拒绝、数值边界、appendix fail-fast、LOG_DIR 校验全是行为，仓库 AGENTS.md 自己标注"改动相关区域时补"——本次就是改动区域 | 新增 `config_test.go` 覆盖上述每一分支 |
| B8 | 低 | `logsys.Log()` stdout 行 | map 迭代无序 → stdout key=value 字段顺序随机（JSON 文件 sink 因 encoding/json 排序 key 是稳定的）——journalctl/docker logs 里 grep/对比困难 | stdout 行按字段名排序输出，两 sink 字段顺序一致 |

### 审查后接受、不整改的项（含理由）

| 项 | 理由 |
|---|---|
| `defaultMaxRequestBytes`（server）与 config 默认 `1<<20` 并存 | server 侧 fallback 仅服务手工构造的测试 Config：若删除，手工 Config 零值 → MaxBytesReader(0) → 所有请求静默 400，是更危险的 footgun。3 行防御值得保留；config.FromEnv 仍是生产路径唯一默认来源 |
| `logsys.Log` 持锁写双 sink | 单请求一次审计事件，锁粒度=单次写，无争用场景；拆锁增加复杂度无收益 |
| `chat()` 的 defer 日志模式 | 正确且惯用，保留 |
| `ParseUpstreamJSON` 双 unmarshal | 简单直接，早前审查已定论不动 |
| Dockerfile/compose/.dockerignore | 上轮已加固并对齐，无新发现 |

### 正面结论

分层树干净（config/qwen3guard 叶子化）、协议常量单源 + canonical 预构建映射、ErrorKind 死抽象已清除、`-healthcheck` 可注入测试、全部文件 <250 行、注释出处纪律整体良好（除 B4/B6 两处残留）。

---

## 二、整改计划（分阶段，每阶段后烟雾测试）

### Phase 1 — server.go 收敛（B1/B2/B3/B4）

步骤：
1. 新增 `h.respondError(w, fields, status int, msg string)`：设 `fields["status"]` + 调 `writeAPIError`；替换 chat() 内 8 处散布赋值（401/413/400×3 + failure 内 503 等）
验收标准（全部满足才进下一阶段）：
- [x] `fields["status"]` 赋值仅存于 3 类合法位置：① `respondError` 本体（唯一错误路径写点）；② 成功路径一处（`status=200`）；③ `failure()` 的 safe/unsafe 策略分支复合赋值（`status` 与 `safety` 同写，语义不同不拆）。（修订记录：原稿"≤2 处"表述过粗，把 ②③ 误算为散布；实际目标是消除**错误分支**里的手工赋值——已达成：401/413/400×3/503 全部走 respondError，无遗漏点）
- [x] `splitVerdict` 删除（grep 零命中）
- [x] `.(string)` map 类型断言零命中（request_id 显式参数化）
- [x] "sub2api's OpenAI clients" 无据注释删除，SSE 注释改引 OpenAI 官方 streaming 合同 URL
- [x] 行为不变：现有全部测试**原样通过**（未改任何既有断言）
- [x] `gofmt -l .` 空、`go vet ./...` 零告警、`go test ./...` 全绿

烟雾测试（已执行，通过）：真实千问上游探测 → 200 + golden 输出 `Safety: Unsafe\nCategories: Jailbreak`；按 request_id 关联日志事件 `status=200 safety=Unsafe categories=Jailbreak mode=json_schema` 字段齐全；**顺带验证按日轮转**（跨零点自动切 `gateway-20260908.jsonl`，两发均落盘正确）

### Phase 2 — config 包测试 + 注释修正（B6/B7）

步骤：
1. 新增 `internal/config/config_test.go`（表驱动 + `t.Setenv`）：
   - 必填三项缺失 → 单一错误（分别缺一项/全缺）
   - `STRUCTURED_OUTPUT_MODE` / `FAILURE_POLICY` / `LOG_LEVEL` 非法枚举 → 拒绝；合法值透传
   - 数值边界：timeout≤0 / maxTokens≤0 / temperature<0 或 >2 / MaxInputChars<0 / MAX_REQUEST_BYTES≤0 → "invalid numeric configuration" 或对应错误
   - `MAX_REQUEST_BYTES` env 覆盖默认生效
   - `AUDIT_POLICY_APPEND_FILE`：正常文件加载内容进 `PolicyAppendix`；文件缺失 → 启动错误（fail-fast）
   - `LOG_DIR=off` → 不创建目录；`LOG_DIR` 指向不可写路径 → 错误
   - `UPSTREAM_EXTRA_BODY_JSON` 非法 JSON → 错误；合法 JSON 进 map
   - `UPSTREAM_JSON_SCHEMA_STRICT` 非法值 → 错误
2. `config.go` 结构体注释出处修正（README 配置表 + .env.example）

验收标准：
- [x] 上述每一分支至少一个用例命中（`go test ./internal/config/ -v` 逐条可见：TestRequiredTrio ×3 / TestEnumValidation 全枚举 / TestNumericBounds ×7 / TestDefaultsAndOverrides / TestPolicyAppendixFile 成败两路径 / TestExtraBodyJSON / TestLogDirOffSkipsMkdir / TestStrictBool）
- [x] `grep "DESIGN.md section 4/5" internal/config/config.go` 零命中（改指 README 配置表 + .env.example）
- [x] `gofmt`/`vet`/全量测试绿

烟雾测试（已执行，通过）：全量测试绿；hub 起真实进程干跑——`gateway_start` 事件含 `policy_appendix=true`、脱敏 api_key、`LOG_DIR=off` 不建目录，正常监听后停止

### Phase 3 — 测试噪音消除 + stdout 日志确定性（B5/B8）

步骤：
1. `server_test.go`：测试助手统一注入静默 logger（实施修订：原方案 `logsys.New("", "error")` 不足——ERROR 级 `audit_failed` 事件仍会打到 stdout；最终实现为 `logsys.NewDiscard()` 全静默构造器 + `testLogger(t)` helper）
2. `server.New` 注释修正：nil → "installs a stdout-only default logger"（如实）
3. `logsys.Log`：stdout 行字段按 key 排序（`slices.Sort(keys)`），与 JSON sink 顺序一致
4. `logsys_test.go` 补确定性断言（两次 Log 同字段 → stdout 行字节一致）
5. AGENTS.md 同步：Testing 一节移除 "internal/config 无测试" 缺口条目；补 server 测试注入 `logsys.NewDiscard()` 静默 logger 的惯例（历史注记：原稿写 level=error，因 ERROR 级 `audit_failed` 仍会输出而改为 NewDiscard，同步骤 1）

验收标准：
- [x] `go test ./internal/server/ -v 2>&1 | grep -cE 'msg=audit|audit_failed'` = 0（修订记录：原稿只查 `msg=audit`，会漏 ERROR 级 `audit_failed` 噪音——检查已扩为两者；实现也从 level 过滤改为 `logsys.NewDiscard()` 全静默构造器，附行为测试 TestNewDiscardIsSilent）
- [x] `grep "logging disabled" internal/server/server.go` 零命中（New 注释如实描述 nil → stdout info 默认）
- [x] logsys 确定性测试通过（TestStdoutFieldOrderDeterministic：两 logger 同事件 → stdout 字节一致 + 字段按 key 排序）
- [x] 全量绿 + gofmt；AGENTS.md 已同步（logsys 行如实标注"通用 sink，白名单是 server 层合同"；日志通道描述更新；config 测试缺口条目移除）

烟雾测试（已执行，通过）：真实千问一发 → 200 `Unsafe/Non-violent Illegal Acts`；按 request_id 关联 jsonl 事件字段齐全且与响应一致（11 字段白名单）

### Phase 4 — 终验 + 提交推送

步骤：
1. 终验：`gofmt -l .` 空 → `go vet ./...` → `go test ./...` 全绿 → 真实千问 3 探测（jailbreak/破解/良性）+ 日志落盘检查
2. 更新 REVIEW-PLAN.md：勾选全部验收框
3. 单一提交推送 main（纯内部质量改进，无协议/行为变更，**不发新版本**——二进制行为等价；是否发 v0.2.1 留作开放问题问用户）

验收标准：
- [x] 终验全过：gofmt 空 / vet 零告警 / 全量测试绿 / 真实千问 3 探测（jailbreak→Unsafe/Jailbreak、破解→Unsafe/Non-violent Illegal Acts、睡前故事→Safe/None）/ audit 事件落盘 14 条
- [x] `git status` 干净、整改已推送至远端 main（以 git log / 远端状态核验，不在此固定具体提交）
- [x] REVIEW-PLAN.md 所有框勾选（本项即此动作）
---

## 三、开放问题（执行完后向用户确认）

1. **是否为本次纯内部重构发 v0.2.1？** 行为零变化（现有测试原样通过为证），我倾向不发版；但若你希望"main 与最新 Release 二进制严格一致"，可发。
2. **`defaultMaxRequestBytes` 双默认保留**（server 防御 fallback + config 默认）——我选择保留（防手工 Config 零值 footgun），严格单源派可删，你倾向？
