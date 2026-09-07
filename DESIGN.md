# DESIGN.md — LLM → Qwen3Guard 兼容网关

本文档的每一条协议结论都标注了出处。冲突时遵循的权威顺序：**运行时代码（chat_template / 解析器源码）> 官方 README / 模型卡 > 博客 / 技术报告**。

缩写：
- **[GT]** = Qwen3Guard 仓库 https://github.com/QwenLM/Qwen3Guard （README.md、eval/eval_gen.py）
- **[CT-8B]** = `https://huggingface.co/Qwen/Qwen3Guard-Gen-8B/raw/main/tokenizer_config.json` 的 `chat_template`（运行时权威；4B/0.6B 的 chat_template 已逐字节比对，完全一致）
- **[MC]** = HF 模型卡（Qwen3Guard-Gen-8B/4B/0.6B，Safety Policy 段）
- **[S2A]** = sub2api 仓库 `backend/internal/securityaudit/`（main 分支源码）
- **[PR4485]/[PR4953]** = sub2api PR diff
- **[OR]** = OpenRouter Structured Outputs 文档
- **[SF]** = SiliconFlow JSON 模式 / 结构化输出文档
- **[ZP]** = 智谱 BigModel 文档（docs.bigmodel.cn）

---

## 1. Qwen3Guard-Gen 输出合同（网关必须渲染成的目标格式）

### 1.1 审 prompt（主场景）：两行

```
Safety: Unsafe
Categories: Violent
```

- 第 1 行 `Safety: ` 后取值 **`Safe` / `Unsafe` / `Controversial`**（首字母大写，仅此三档，无其他拼写）。[GT] README 正则 `r"Safety: (Safe|Unsafe|Controversial)"`；[CT-8B] 模板："The first line must be one of: 'Safety: Safe', 'Safety: Unsafe', 'Safety: Controversial'."
- 第 2 行 `Categories: ` 后为类目列表；**安全时写 `Categories: None`**。[CT-8B]："If the content is safe, use 'Categories: None'."
- 输出类目 token 与正则里的完全一致（注意 `PII` 是缩写 token，不是全称）。[GT] README prompt 正则：`(Violent|Non-violent Illegal Acts|Sexual Content or Sexual Acts|PII|Suicide & Self-Harm|Unethical Acts|Politically Sensitive Topics|Copyright Violation|Jailbreak|None)`

### 1.2 审 response：三行（多一行 Refusal）

```
Safety: Safe
Categories: None
Refusal: Yes
```

- `Refusal: Yes|No` 仅出现在 response 模式，且是必填第三行。[CT-8B] response 分支："The third line must be one of: 'Refusal: Yes', 'Refusal: No'."；[GT] README 正则 `r"Refusal: (Yes|No)"`。
- Refusal 语义：Yes = 助手拒绝/未服从用户请求；No = 直接回答/服从。[CT-8B] `<BEGIN REFUSAL POLICY>`。

### 1.3 官方类目（输出 token，精确拼写）

| # | Prompt 模式（9 个） | Response 模式（8 个） |
|---|---|---|
| 1 | `Violent` | `Violent` |
| 2 | `Non-violent Illegal Acts` | `Non-violent Illegal Acts` |
| 3 | `Sexual Content or Sexual Acts` | `Sexual Content or Sexual Acts` |
| 4 | `PII` | `PII` |
| 5 | `Suicide & Self-Harm` | `Suicide & Self-Harm` |
| 6 | `Unethical Acts` | `Unethical Acts` |
| 7 | `Politically Sensitive Topics` | `Politically Sensitive Topics` |
| 8 | `Copyright Violation` | `Copyright Violation` |
| 9 | `Jailbreak` | —（无） |

出处：[CT-8B] `<BEGIN UNSAFE CONTENT CATEGORIES>` 两分支逐字；[GT] README 两条 category 正则（prompt 含 `Jailbreak`，response 不含）；[MC] Safety Policy（bullet 标题为 "Jailbreak (Only for input)"）。

- **Jailbreak 在官方类目/正则里**：prompt 模式含 `Jailbreak`（不是 "Jailbreaking" / "Jailbreak Instructions"——任何官方源都没有这些字符串）。技术报告 §2："the 'jailbreak' label applies exclusively to input classification"。
- 模型卡政策 prose 用全称 "Personally Identifiable Information"，但**输出 token 是 `PII`**（解析正则与 chat_template 均用 PII）——以运行时代码为准，渲染用 `PII`。
- `Suicide & Self-Harm` 用 `&`（不是 "and"）；`Non-violent` 有连字符；`Sexual Content **or** Sexual Acts` 用 "or"。
- 0.6B / 4B / 8B 三尺寸 chat_template 逐字节一致（本地比对验证），类目表不随尺寸变化。

### 1.4 分隔符与顺序

- 分隔符：逗号。[CT-8B]："separated by commas"。官方源没有任何多类目逐字示例；逗号后是否带空格无官方约定 → **采用 `", "`（逗号+空格）**，sub2api 按 `,` 切分后 TrimSpace（[S2A] prompt_qwen3guard.go L131-132），官方正则 re.findall 与顺序/空格无关，两种写法兼容。
- 顺序：无任何官方约定（官方解析器 re.findall 按出现顺序收集）。→ 网关按上游 JSON 返回顺序保序去重输出，不重排。

### 1.5 上游 system prompt 中的政策表述（只用官方政策，不复刻 chat template）

**非目标约束**：不得把 Qwen3Guard 官方 chat_template 的任务指令（"# Task: …"、"Provide your safety assessment for ONLY THE LAST …"、"USER:/ASSISTANT:" 会话渲染等模板脚手架）套到通用 LLM 上。因此网关发给上游的 system prompt 由三部分组成：

1. **任务框架**：网关自己撰写（如"你是内容安全审核引擎，按下列政策对给定文本分类，只输出 JSON"）——非官方文本。
2. **政策定义**：逐字取自 [MC] HF 模型卡 Safety Policy 段——三档 severity 定义（Unsafe/Controversial/Safe）与 9 个类目的完整定义（模型卡用全称 "Personally Identifiable Information"，输出 token 映射为 `PII`；"Jailbreak (Only for input)" 标注仅输入侧）。prompt 审计不涉及 Refusal（官方 Refusal 语义仅存在于 response 审计，见 §1.2，未实现）。
3. **输出格式**：网关自己撰写的 JSON 指令（字段名/枚举值与 §3.3 schema 一致），不使用模板的两/三行文本格式说明。

不发明任何类目；政策文本与 [GT] README、技术报告 §2 一致，可互证。

### 1.6 运营者审查侧重附录（AUDIT_POLICY_APPEND_FILE）

`AUDIT_POLICY_APPEND_FILE` 指向文本文件（Markdown），内容作为「Additional audit focus」插入 §1.5 结构的第 2 部分（官方政策定义）与第 3 部分（JSON 输出指令）**之间**；JSON 指令固定末尾，appendix 无法顶掉。语义边界：appendix 是任意运营者文本，**可改变判定侧重与宽严**（既可能收紧也可能放宽），但**不能改变对外合同**——safety 枚举与官方 9 类目由本地 `ValidateVerdict` 强制（§3.2 第 3 条），越界输出判无效走失败策略。文件缺失/不可读 → 启动失败（fail-fast）。示例见 `policy-appendix.example.md`。

---

## 2. sub2api 解析合同（网关对外的消费方）

出处均为 [S2A] main 分支源码（已下载核对：`prompt_qwen3guard.go`、`prompt_guard.go`、`prompt_config.go`、`prompt_types.go`、`coordinator.go`）。

### 2.1 调用方式

- **POST `{base_url}/v1/chat/completions`**（`ChatCompletionsURL` 会先归一化剥掉结尾 `/v1` 再拼 `/v1/chat/completions`，所以 base_url 填 `http://gw:8080` 或 `http://gw:8080/v1` 都行）。[S2A] prompt_outbound_security.go L42-46
- 请求体（[S2A] prompt_qwen3guard.go L205-211 逐字）：
  ```json
  {"model": "<endpoint.Model>", "messages": [{"role": "user", "content": "<chunk>"}], "temperature": 0, "max_tokens": 64, "seed": 42}
  ```
  **没有 system 消息**，只有一条 user 消息。默认 model 为 `sileader/qwen3guard:0.6b`（`DefaultGuardModel`）——网关忽略请求里的 model 字段，用自己配置的上游模型。
- 若节点配了 token，带 `Authorization: Bearer <token>`。[S2A] L221-223
- 输入分块：`SplitRunes(text, InputLimit)`，`DefaultInputLimit = 4000`（可配 128–100000，[S2A] prompt_config.go L24-26）→ 网关单请求通常收到 ≤4000 字符文本。
- 响应体上限 256KB（`maxGuardResponseBytes`）。

### 2.2 最小合法字段集与解析规则

`ParseQwen3Guard`（[S2A] prompt_qwen3guard.go L88-179，已逐行核对）：

- **必须有非空 `Safety:` 行且取值（大小写不敏感）为 safe/controversial/unsafe 之一，且必须有非空 `Categories:` 行**——两者缺一即 `ErrorCodeInvalidResponse`。不是"只要 Safety"。
- 行按前缀匹配（`strings.HasPrefix(lower, "safety:")`），`\r\n` 归一、行 TrimSpace、取值大小写不敏感后规范化为 `Safe/Controversial/Unsafe`。
- **重复的 Safety 或 Categories 行 → 错误**（L99-106）。
- **未知行（如 `Refusal: No`）被忽略**（L108-109 注释："Auxiliary Guard fields, such as Refusal, do not affect audit decisions."，PR #4953 引入）。→ 网关 response 模式输出三行也不会干扰 sub2api。
- Categories 按 `,` 切分；`none`/`n/a`（大小写不敏感）跳过（L131-134）；空 token 跳过。
- 未知类目：**不回显原文**，哈希为 `unknown:<sha256[:8]>`（L181-184）；Unsafe 时未知类目同样升级为 Block。
- 决策矩阵：Safe→Allow；Controversial→Warn（但 `jailbreak`/`pii`/`suicide_and_self_harm` 升级 Block）；Unsafe→Block（含"Unsafe + Categories: None"，L165 条件 `len(knownList) == 0` 也成立）。
- 响应信封：`choices[0].message.content` 为字符串（非空白），或 content 数组（取各 `{"type":"text","text":...}` 块按 `\n` 连接）；否则错（L282-318）。
- 9 个 scanner ID（[S2A] L25-35）：`violent, non_violent_illegal_acts, sexual_content_or_sexual_acts, pii, suicide_and_self_harm, unethical_acts, politically_sensitive_topics, copyright_violation, jailbreak`——与官方 9 类目一一对应。

### 2.3 失败处理（决定网关默认失败策略）

sub2api **fail-closed**：任何解析失败/网关 5xx/超时 → `DecisionInvalid`/`DecisionUnavailable` → 对客户端返回 **HTTP 503**（`prompt_guard_invalid_response` / `prompt_guard_unavailable`），`AllowNextStage=false`，**绝不放行**。[S2A] coordinator.go `prioritize`、handler/security_audit_errors.go；[PR4485]（模块引入即 fail-closed）。

**网关默认失败策略 = `error`（HTTP 503）**：伪装 Safe 等于静默绕过 sub2api 的阻断策略；而 sub2api 对 5xx 本来就走 fail-closed 重试逻辑，让调用方走失败逻辑与其设计一致。同时提供可配置的 `safe`（fail-open）/ `unsafe`（fail-block：返回 `Safety: Unsafe\nCategories: None`——已验证 sub2api 对此判 Block）供用户选择，但不是默认。

### 2.4 sub2api 不支持审 response

sub2api 只做 prompt-input 审计（feature spec 名就叫 prompt-input-audit；全仓库无 response/output audit 代码路径）。产品需求中 response 模式的前提是"官方和 sub2api 都支持"——sub2api 不支持，条件不成立。因此：

- **网关只实现 prompt 审计**（固定行为：抽取最后一条 user 消息，输出两行），不提供 GUARD_MODE 配置。
- 官方 response 三行合同（§1.2）仅作为调研记录保留；若未来 sub2api 支持输出审计，可按 §1.2/§3.3 扩展。
- sub2api 发来的请求永远只有单条 user 消息；即使其解析器会忽略 Refusal 行（§2.2），网关也不产生该行。

---

## 3. 上游结构化输出合同

### 3.1 三家的请求形态（逐字核对）

| 供应商 | json_object | json_schema | strict |
|---|---|---|---|
| OpenRouter [OR] | `response_format: {"type":"json_object"}` | `response_format: {"type":"json_schema","json_schema":{"name":...,"schema":...,"strict":true}}` | `strict` 可选（OpenAPI required 只有 `name`），官方建议 true |
| SiliconFlow [SF] | `response_format: {"type":"json_object"}` | `response_format: {"type":"json_schema","json_schema":{"name":...,"schema":...}}` | **无** strict 字段（仅 function calling 有） |
| 智谱 [ZP] | `response_format: {"type":"json_object"}`（唯一模式） | **不支持**（OpenAPI enum 仅 text/json_object） | 无 |

- OpenRouter：模型不支持时请求直接报错（"The request will fail with an error indicating lack of support"）。[OR] Error Handling
- SiliconFlow：文档称线上全部 LLM 都支持两种模式；提醒 max_tokens 太小会截断 JSON，应用必须处理不完整 JSON。[SF] json-mode
- 智谱：`glm-4.7-flash` 属文本模型、模型页带"结构化输出"能力卡，支持 `{"type":"json_object"}`；**schema 必须写在 system 消息里**，客户端自行校验。[ZP] struct-output / 对话补全 OpenAPI
- 智谱关思考：`thinking: {"type":"disabled"}`（OpenAPI `ChatThinking`：GLM-4.7 默认 enabled 且强制思考）。[ZP] 对话补全 OpenAPI

### 3.2 网关降级链（仅此一条，无纯文本回退）

1. `STRUCTURED_OUTPUT_MODE=auto`（默认）：先发 `json_schema`；若 HTTP 4xx（401/403 除外，那是配置错误）→ 用 `json_object` 重发一次（同一 system prompt，schema 已内嵌其中）。`json_schema` 默认**不带** `strict` 字段（OpenRouter 文档为可选；SiliconFlow 的 response_format 路径未文档化该字段，见 §3.1），需要时用 `UPSTREAM_JSON_SCHEMA_STRICT=true` 显式开启（发给 OpenRouter 时推荐）。

2. `json_schema` / `json_object`：固定单一模式。
3. **任何时候本地校验都执行**：JSON 解析（容忍 markdown 代码栅栏）→ safety 枚举校验 → 类目逐一规范化到官方 token（别名表源自 sub2api `categoryAliases` + 模型卡全称，不发明新类目）→ 一致性校验（Safe 时 categories 必须为空）。
4. 校验失败 / 上游最终失败 / 超时 → 按失败策略（默认 503）。**绝不退化为"让模型自由输出文本"。**

### 3.3 上游 JSON schema（prompt 模式）

```json
{
  "type": "object",
  "properties": {
    "safety": {"type": "string", "enum": ["Safe", "Unsafe", "Controversial"]},
    "categories": {"type": "array", "items": {"type": "string", "enum": ["Violent","Non-violent Illegal Acts","Sexual Content or Sexual Acts","PII","Suicide & Self-Harm","Unethical Acts","Politically Sensitive Topics","Copyright Violation","Jailbreak"]}}
  },
  "required": ["safety", "categories"],
  "additionalProperties": false
}
```

**categories 数组不带 `uniqueItems`**（2026-09-07 实测修订）：千问 DashScope（`https://dashscope.aliyuncs.com/compatible-mode/v1`，qwen-flash）对数组类型携带 `uniqueItems` 的 schema 返回 400——报错原文：`InternalError.Algo.InvalidParameter: Format error : 'response_format.json_schema.schema'. ... When the schema contains the fields "uniqueItems", "contains", "minContains", or "maxContains", the type should not be "array"`。去重由本地 `NormalizeUpstreamCategories` 强制（§3.2 第 3 条），schema 层约束本就冗余；OpenRouter/SiliconFlow 的标准 JSON Schema 均接受无 `uniqueItems` 的数组，删除无兼容性损失。回归测试：`upstream_test.go` 断言 categories schema 不含 `uniqueItems`。

（未实现）response 模式曾计划的 schema 扩展——加 `"refusal": {"type":"string","enum":["Yes","No"]}` 并从 categories 枚举去掉 `Jailbreak`——随 §2.4 的结论一并搁置，仅留作记录；当前实现只有上述 prompt schema。

---

## 4. 网关对外接口（OpenAI 兼容）

- `POST /v1/chat/completions`：接受标准 chat body（model 忽略；messages 必填；`stream` 支持——SSE 返回 role 帧 → content 帧 → `finish_reason:"stop"` 终止帧 + `[DONE]`，因为审计结果是整体产出的）。
- `GET /healthz`：健康检查（`{"status":"ok"}`）。sub2api 不调 `/v1/models`，不实现。
- 文本抽取：取 **最后一条 user 消息**（固定，见 §2.4）。content 为字符串或 OpenAI content 数组（拼接 text 部件）。多轮历史不作为上游上下文（sub2api 主场景只发单条 user 消息；官方模板仅用于 Qwen3Guard 自己的输入渲染，本网关不复刻模板——非目标）。
- 可选审查侧重附录：`AUDIT_POLICY_APPEND_FILE`（见 §1.6）。
- 审计日志（`internal/logsys`）：每请求双通道记录——stdout 常开（Docker/journald 消费）+ 文件 `LOG_DIR`（默认 `logs`，按日轮转 JSONL；`off` 关闭，Docker read-only 场景用）。字段白名单：request_id/text_chars/stream/model/base_url/api_key(**Redact 脱敏**)/mode/status/latency_ms/safety/categories；**绝不记录**待审文本、messages、上游原始 JSON、appendix 内容。`LOG_LEVEL`=debug|info|warn|error（默认 info）。
- 失败响应对外**固定文案** "guard pipeline failure"——上游响应体（内嵌于 `UpstreamError`）绝不透给调用方（非目标）。
- 可选 `GATEWAY_API_KEY`：设置后校验 `Authorization: Bearer`（sub2api 节点配 token 时会带上）。
- 超长输入：`MAX_INPUT_CHARS` 截断（默认 32000 > sub2api 默认分块 4000；0=不限制），日志告警。

## 5. 失败策略（`FAILURE_POLICY`）

| 值 | 行为 | sub2api 侧结果 |
|---|---|---|
| `error`（默认） | HTTP 503，OpenAI 风格 error body | fail-closed 503，走重试/失败逻辑 |
| `safe` | 200，`Safety: Safe\nCategories: None` | 放行（fail-open） |
| `unsafe` | 200，`Safety: Unsafe\nCategories: None` | Block（已验证 L165：knownList 为空也判 EventCritical/ActionBlock） |

选 `error` 为默认的理由见 2.3。

## 6. 未证实事项（TODO，不编造）

- **逗号后空格**：官方无多类目逐字示例（见 1.4），选用 `", "`，兼容性已论证但不声称是官方规范。
- OpenRouter "不支持 structured outputs" 的确切 HTTP 状态码：官方文档只说 "will fail with an error"，未给码 → auto 模式按"任何 4xx（非 401/403）即降级"处理。
- SiliconFlow 是否接受 json_schema 内的 `strict`：未文档化 → 不发送该字段。
- 智谱 json_object 模式下模型偶发输出不完整 JSON 的处理：文档只要求客户端自行处理 → 本地校验 + 失败策略覆盖。

## 7. 实现选型

Go 1.25（本机已装；Python 3.12 未装）。stdlib `net/http` + `encoding/json`，零第三方依赖；`httptest` 做上游桩，测试性最好。
