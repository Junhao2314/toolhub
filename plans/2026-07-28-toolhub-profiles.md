# ToolHub Profiles：来源筛选、Runtime 视图与 Profile 激活

Date: 2026-07-28

Status: Completed（2026-07-28）

## 目标

给 ToolHub 补上三件事：

1. **来源筛选**：Skills / MCP 列表按 provenance（从哪导入的）筛选。
2. **Runtime 视图**：选一个 (节点, runtime)，一屏看清该 target 上究竟生效了什么。
3. **Profile**：用户定义"科研"/"开发"这类选择集（一批 MCP + 一批 Skills），激活到某个 (节点, runtime) 后，只有该集合生效，其余关闭；可激活到远程节点。

术语：本文中 "Agent" 指 `toolhub-agent` 节点守护进程；Claude/Codex/Hermes 等平台称为 "runtime"。
"**ToolHub Profile**"（新概念，表 `toolhub_profiles`）是用户定义的**选择集**；
"**固定投递 profile**"（既有概念，表 `mcp_profiles` 中的 `toolhub-claude` / `toolhub-codex`）是 mcpm 的**投递通道**，其名称与原生锚点保持硬编码不变。这两个概念全文严格区分，不得混用。

---

## 锁定决策

| # | 决策 | 含义 |
|---|---|---|
| D1 | **固定锚点，只换成员** | `~/.claude.json` 的 `mcpServers.toolhub-claude` 与 `~/.codex/config.toml` 的 `[mcp_servers.toolhub-codex]` 一次写定后**永不因切换 profile 而改动**。切换 profile 只重写 `~/.config/mcpm/servers.json` 的 `profile_tags`。 |
| D2 | **Profile 管 MCP + Skills** | 激活"科研" = 只开科研的 MCP + 只开科研的 Skills，其余关闭。 |
| D3 | **单激活** | 一个 (节点, runtime) 同一时刻只有一个生效 profile。数据库 `UNIQUE(node_id, runtime_kind)` 强制。 |
| D4 | **Profile 不绑 runtime** | "科研"只有一份定义，激活时才选 target。同一份可激活到多个 (节点, runtime)。 |
| D5 | **不做适用性判断** | 成员就是成员，全投。codex 专用 skill 投到 claude 上只是多一个用不上的目录，不报错不阻断。**不新增任何 runtime 适用性字段。** |
| D6 | **能力不足显式标注并跳过** | 允许激活到 hermes/grok/openclaw。MCP 写不了的 runtime，MCP 部分跳过并把原因记进激活记录，不静默失败、不阻断 Skills 部分。 |
| D7 | **Runtime 视图走后端聚合端点，provenance 筛选走前端** | 新增 `GET /api/v1/targets/{nodeId}/{runtime}`；列表筛选器在 React 里做，不加 query 参数。 |
| D8 | **MCP 保持自动入库 + 加归档** | 不动 "existing MCP configuration is automatically captured and baselined" 这条 invariant。新增"归档/忽略"把不想要的从默认视图收起。 |
| D9 | **Profile 接管，手工矩阵只读** | 某 target 一旦激活 profile，其 Skills/MCP desired state 完全由 profile 决定，`TargetModal` 对该 target 的勾选框置灰。必须提供"停用 profile"动作交还控制权。 |
| D10 | **新增 `profile_activate` job kind** | 一个 job 统管整次激活，顺序驱动已有的 Skills / MCP 下发逻辑。**Agent 侧零改动**，仍只用 `deploy_skill` / `apply_mcp`。 |
| D11 | **预检 + 失败即停 + 可重试** | 写 desired state 前做完整预检；真失败（中途掉线）停在半路，如实标为 `partial`，提供"重试"和"手动回到上一个 profile"。**不自动回滚**（最常见的失败原因是节点离线，自动回滚同样发不下去）。 |
| D12 | **跨节点密钥下发需确认** | 把带密钥的 MCP 激活到**非本机**节点时，预检返回将要下发的密钥 key 名清单，需请求体带 `confirmSecrets: true` 才继续，并写审计事件。本机（`nodes.is_local`）不拦。 |

---

## 已验证的现状事实

实施前请自行复核这些引用，行号可能因其他改动漂移。

### MCP 投递链

- 每个节点**只有一份** mcpm store：`~/.config/mcpm/servers.json`（`internal/runtime/mcpm.go:26` `mcpmStoreRelativePath`），模式 0600。
- Claude / Codex 的区隔靠 mcpm 的 `profile_tags`，两个 profile 共存于同一份 store。profile 名硬编码：`internal/runtime/mcpm.go:29-30` `managedCodexProfile = "toolhub-codex"` / `managedClaudeProfile = "toolhub-claude"`，由 `MCPMProfileForRuntime()` 分发，**对 codex/claude 以外的 runtime 返回空串**。
- 原生锚点各一条：`internal/runtime/mcp_anchor.go:63` `const anchor = "toolhub-claude"`；Codex 侧为 `config.toml` 的 `[mcp_servers.toolhub-codex]`。
- 固定 profile 的守卫在 `internal/store/mcp.go:513` `managedMCPProfileRuntimeTx()`：要求 `source='toolhub'` 且 `name = "toolhub-"+runtimeKind` 且 `managedRuntime ∈ {codex, claude}`，否则返回 `ErrManagedMCPProfile`（HTTP 409 `managed_mcp_profile_required`，见 `internal/httpapi/api.go:168`）。

### 各 runtime 能力面（不对称）

| runtime | Skills 写入 | MCP 写入 |
|---|---|---|
| claude | ✅ `~/.claude/skills` | ✅ mcpm + `~/.claude.json` 锚点 |
| codex | ✅ `~/.codex/skills` | ✅ mcpm + `config.toml` 锚点 |
| hermes | ✅ `~/.hermes/skills` | ❌ 只读扫 `~/.hermes/config.yaml`（`internal/runtime/inventory.go:197`） |
| grok | ✅ `~/.grok/skills` | ⚠️ **无自己的配置，蹭读 claude 的**（`internal/runtime/inventory.go:198,202`） |
| openclaw | ✅ `~/.openclaw/workspace/skills` | ❌ 只读扫 `mcporter.json`（`internal/runtime/inventory.go:199`） |

Skills 根路径见 `internal/runtime/inventory.go:29-33`。`domain.IsSkillRuntime` == `IsConsumerRuntime`，含全部 5 个。

### 筛选与列表

- **今天没有任何服务端筛选**。所有 list 端点共用 `internal/httpapi/resources.go:64` 的 `serveList`，把 store 返回的 `json.RawMessage` 原样吐出，不接任何 query 参数。`web/src/pages/Skills.tsx` 的搜索框是纯前端 `.filter()`。
- provenance 字段：MCP 为 `mcp_servers.source`（`toolhub` / `runtime-auto` / `mcpm-import` / `shared-import`）+ `origin.importSourceName`；Skills 为 `skill_sources.kind`（`upload`/`git`/`skillsmp`/`openai`/`node`，见 `004_shared_sources.sql:70-72`，另有 008 追加的 xiaping 来源）。
- target 维度**今天无法直接查**：Library 里的 skill 是 runtime 无关的 artifact，runtime 只存在于 `deployments`；MCP 的 runtime 只能经 `mcp_deployments` → `mcp_runtime_bindings` 反查。

### 编排

- Worker job kinds（`internal/worker/worker.go:117-131`）：`inventory_scan` / `skill_import` / `skill_adopt` / `update_check` / `sync`(+`rollback`) / `mcp_sync` / `mcp_health` / `archive_purge`。
- Agent task kinds 是封闭集合：`scan_inventory` / `deploy_skill` / `apply_mcp` / `adopt_skill`。**本计划不新增 Agent task kind。**
- Selector 字段是契约：`syncSkills`（`worker.go:275`）吃 `nodeIds`/`skillIds`/`deploymentIds`/`scopeType`/`scopeId`；`syncMCP`（`worker.go:341`）吃 `nodeIds`/`profileIds`/`deploymentIds`/`scopeType`/`scopeId`。
- Skills desired state 的写入范式见 `internal/store/skills.go:131-140`（`ON CONFLICT ... desired_generation` 条件自增 + `state='pending'`）。**新代码必须复用这段 SQL 的语义**，不要另写一套 generation 逻辑。
- 审计写法：`_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: ..., ResourceType: ..., ResourceID: ..., Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{...}})`（如 `internal/httpapi/skills.go:56`）。

---

## 必须先修的三个既有缺陷

这三个都是**现存代码里的问题**，不修则新功能一定出错。它们构成 Phase 0，应可独立合并、独立验证。

### B1 — `.toolhub-disabled` 不可重入（阻断反复切 profile）

**现状**：`internal/runtime/deploy.go:159-179` 的 `disable()` 把 skill 目录 `os.Rename` 到 `<skillsRoot>/.toolhub-disabled/<slug>-<sha12>`。全仓库只有 `deploy.go:169` 写它、`inventory.go:184` 读它，**没有任何清理**。而 `disable()` 在该路径已存在时是硬报错 `"disabled target already exists"`。

**后果**：科研 → 开发 → 科研 → 开发，第二次切走时每个离开 profile 的 skill 都 disable 失败。

**修法**：
1. `disable()` 中，若目标 disabled 路径已存在，先 `os.RemoveAll(disabled)` 再 rename。安全性论证：路径按 `<slug>-<sha12>` 内容寻址，artifact 不可变，已存在的副本与即将移入的目录**字节等价**。
2. `enable()` 成功激活后（`deploy.go:129` `os.Rename(staging, target)` 之后），清理 `<skillsRoot>/.toolhub-disabled/` 下**该 slug 的所有** `<slug>-*` 条目。这覆盖了"换了版本导致 sha 变化"的遗留。
3. 保留 `"refusing to disable an unmanaged skill"` 这道检查，不要放宽。

**回归测试**（新增到 `internal/runtime/deploy_test.go`）：enable → disable → enable → disable 同一 (slug, sha)，四步全部成功；断言 `.toolhub-disabled` 下该 slug 条目数 ≤ 1。

### B2 — MCP 成员是全局的，无法按节点分化（阻断 D3 + D4）

**现状**：`mcp_profiles.name` 全局 UNIQUE，`toolhub-claude` 只有一行。`internal/store/mcp.go:488` `MCPDeploymentPayload()` 的 SQL 从 `mcp_profile_servers ps WHERE ps.profile_id = p.id` 取成员——**成员集是 per-runtime 全局的，不是 per-node 的**。`internal/store/mcp.go:548` `refreshProfileDeployments()` 同样把**同一个** `desired_hash` 写给该 profile 的**所有**节点部署。

**后果**：在节点 A 的 claude 上激活"科研"、节点 B 的 claude 上激活"开发"，两者会争抢同一行 `toolhub-claude` 的成员，后写覆盖先写，两个节点最终拿到同一套 MCP。这与 D3（单激活是 per-target）和 D4（跨服务器分发）直接冲突。

**修法**（外科式，不动 schema 的 `mcp_profiles`，不动锚点，不动 Agent）：

1. 在 `internal/store/mcp.go` 新增：
   ```go
   // effectiveMCPServerIDs resolves the server set that must be delivered to one
   // (node, runtime). A ToolHub Profile activated on that target wins; without an
   // activation the fixed delivery profile's own membership is used, which keeps
   // pre-Profile behaviour byte-identical.
   func effectiveMCPServerIDsTx(ctx context.Context, tx pgx.Tx, nodeID, runtimeKind, fixedProfileID string) ([]string, error)
   ```
   查 `toolhub_profile_activations` 中该 (node, runtime) 且 `state IN ('pending','active','partial')` 的行：
   - 命中 → 返回 `toolhub_profile_mcp_servers` 中该 profile 的 server，且 `mcp_servers.enabled AND authority='toolhub' AND archived_at IS NULL`。
   - 未命中 → 返回 `mcp_profile_servers WHERE profile_id = fixedProfileID` 的既有集合（同样带上述过滤）。
2. `MCPDeploymentPayload()` 改为：先取该 deployment 的 `node_id` / `runtime_kind` / `profile_id`，再用 `effectiveMCPServerIDsTx` 解析成员，最后拼 `payload.Servers`。**保留原有的 `runtime_kind IN ('codex','claude')`、`p.source='toolhub'`、`p.name='toolhub-'||d.runtime_kind`、`origin->>'managedRuntime'` 四道守卫**，一个都不能少。`payload.MCPMProfile` 仍是 `"toolhub-" + runtime`。
3. `refreshProfileDeployments()` 改为**逐 deployment** 计算 hash：对该 profile 的每一行 `mcp_deployments`，用其 (node, runtime) 的有效成员集算 `desired_hash`，再套用原有的 `desired_generation` 条件自增与 `state='pending'` 逻辑（`mcp.go:554-558` 的语义完全保留，只是 hash 来源变了）。`profileHashTx` 若仅被此处使用则改造之，否则保留并新增 per-deployment 版本。
4. 找出 `refreshProfileDeployments` 与 `profileHashTx` 的**全部**调用点逐一复核（至少包括 `SetMCPProfileServers`、`SetMCPDeployments`、`UpdateMCPServer`、`DeleteMCPServer` 及 discoveries 相关路径）。

**不变量**：当某 (node, runtime) **没有**激活任何 ToolHub Profile 时，`MCPDeploymentPayload` 与 `refreshProfileDeployments` 的输出必须与改动前**逐字节一致**。这是本次重构的验收硬指标。

**回归测试**（PostgreSQL 集成测试）：两个节点、同一 runtime、各激活不同 profile，断言两者 `payload.Servers` 与 `desired_hash` 不同；再断言无激活时输出与旧行为一致。

### B3 — `grok` 的 MCP 会被 claude 的激活静默改变

**现状**：`internal/runtime/inventory.go:202` 显示 grok 无自己的 MCP 配置，直接读 claude 的。

**后果**：把 profile 激活到某节点的 claude，该节点 grok 的 MCP 也跟着变，但 ToolHub 账上不体现。

**修法**：不写代码强行隔离（做不到）。在 Runtime 视图中，当 `runtime == "grok"` 且同节点 claude 有激活时，`capabilities.mcp = false`、`capabilities.mcpNote = "MCP follows claude on this node"`，并展示 claude 当前生效的 MCP 集合作为**只读**信息。激活 profile 到 grok 时，MCP 部分记为 skipped，reason = `mcp_follows_claude`。

---

## 目标架构

```
用户定义                     投递通道（名称硬编码，不变）        节点落盘
┌──────────────────┐
│ toolhub_profiles │
│  "科研"          │
│   ├ MCP  ×N      │
│   └ Skills ×M    │
└────────┬─────────┘
         │ 激活到 (节点, runtime)  ← UNIQUE(node_id, runtime_kind)
         ▼
┌──────────────────────────┐
│ toolhub_profile_         │
│   activations            │
└────────┬─────────────────┘
         │ profile_activate job（预检 → 改 desired state → 驱动下发）
         ├─────────────► deployments (skill)  ──deploy_skill──► 物化目录 / .toolhub-disabled
         └─────────────► mcp_deployments      ──apply_mcp────► ~/.config/mcpm/servers.json
                          (固定 profile         profile_tags:      的 profile_tags
                           toolhub-<runtime>)   toolhub-claude
                                                              ~/.claude.json 锚点【不动】
                                                              config.toml 锚点【不动】
```

关键性质：
- **进程开销与 profile 数量无关**。`mcpm profile run <profile>` 是单 relay 进程，eager 拉起该 profile 的成员。定义 100 个 profile 也只是 `servers.json` 里的 tag 字符串。切换 profile 不增加进程。
- 已知的既有开销：同一 server 同时在 `toolhub-claude` 和 `toolhub-codex` 中，两个客户端都开时会被拉起两份。这是 runtime 数导致的，本计划不改变。

---

## 数据模型

新增 `internal/store/migrations/009_toolhub_profiles.sql`。**不得改写任何已应用的迁移**（`Store.Migrate` 用 advisory lock 18480，记录版本但**无 checksum、无 down migration**）。

```sql
-- User-defined selection sets. Distinct from mcp_profiles, which remains the
-- fixed per-runtime mcpm delivery channel with its hardcoded toolhub-<runtime> name.
CREATE TABLE toolhub_profiles (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE toolhub_profile_mcp_servers (
    profile_id uuid NOT NULL REFERENCES toolhub_profiles(id) ON DELETE CASCADE,
    server_id  uuid NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    PRIMARY KEY (profile_id, server_id)
);

CREATE TABLE toolhub_profile_skills (
    profile_id uuid NOT NULL REFERENCES toolhub_profiles(id) ON DELETE CASCADE,
    skill_id   uuid NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    PRIMARY KEY (profile_id, skill_id)
);

-- Single activation per target is enforced by the UNIQUE constraint, not by code.
CREATE TABLE toolhub_profile_activations (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    runtime_kind text NOT NULL
        CHECK (runtime_kind IN ('codex','claude','hermes','grok','openclaw')),
    profile_id uuid NOT NULL REFERENCES toolhub_profiles(id) ON DELETE RESTRICT,
    previous_profile_id uuid REFERENCES toolhub_profiles(id) ON DELETE SET NULL,
    state text NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending','active','partial','failed')),
    last_error text NOT NULL DEFAULT '',
    -- [{"scope":"mcp","reason":"mcp_unsupported_runtime","detail":"..."}]
    skipped jsonb NOT NULL DEFAULT '[]'::jsonb,
    activated_by uuid REFERENCES users(id),
    activated_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (node_id, runtime_kind)
);

CREATE INDEX toolhub_profile_activations_profile_idx
    ON toolhub_profile_activations(profile_id);

-- D8: archive/ignore for auto-imported MCP servers. Column does not exist yet.
ALTER TABLE mcp_servers ADD COLUMN archived_at timestamptz;
```

注意：
- `deployments` 与 `mcp_deployments` 的 `runtime_kind` CHECK 已在 `004_shared_sources.sql:70-84` 放宽到含 grok/openclaw，无需再动。
- `mcp_servers` 确认**没有** `archived_at`（001 未定义，003/004 的 ALTER 只加了 `runtime_name`/`origin`/`config_fingerprint`/`authority`/`shared_source_id`/`header_refs`/`credential_mode`）。
- 归档语义：`archived_at IS NOT NULL` 的 server 从默认列表隐藏、不可加入 ToolHub Profile、不参与 `effectiveMCPServerIDsTx`。**不删数据**，可取消归档。

---

## 分阶段实施

每个阶段有明确的 gate。**gate 不过不进下一阶段。**

### Phase 0 — 前置缺陷修复

范围：B1、B2。B3 无代码改动（在 Phase 1 的 Runtime 视图里落实）。

改动文件：
- `internal/runtime/deploy.go`（B1）
- `internal/runtime/deploy_test.go`（B1 回归测试）
- `internal/store/mcp.go`（B2）
- `internal/store/migrations/009_toolhub_profiles.sql`（仅建表，B2 的 `effectiveMCPServerIDsTx` 需要 `toolhub_profile_activations` 存在）
- MCP 相关的 PostgreSQL 集成测试文件（B2 回归测试）

**Gate 0**：
- `go test ./...`、`go vet ./...` 通过。
- 新增的 enable/disable 四步循环测试通过。
- 新增的"无激活时输出与旧行为逐字节一致"测试通过。
- Compose 起停一次，migration 009 成功应用。

### Phase 1 — 数据模型 + 只读 API

新增 store 方法（`internal/store/profiles.go`，新文件）：
- `ListToolHubProfiles(ctx) (json.RawMessage, error)` — 含成员计数与激活计数
- `GetToolHubProfile(ctx, id) (json.RawMessage, error)` — 含 `mcpServerIds` / `skillIds`
- `CreateToolHubProfile(ctx, name, description, actor string) (string, error)`
- `UpdateToolHubProfile(ctx, id, name, description string) error`
- `SetToolHubProfileMembers(ctx, id string, mcpServerIDs, skillIDs []string) error`
- `DeleteToolHubProfile(ctx, id string) error` — 存在激活时返回 `ErrStateConflict`
- `TargetView(ctx, nodeID, runtimeKind string) (json.RawMessage, error)` — Runtime 视图聚合

新增 API 路由（`internal/httpapi/api.go`，新 handler 放 `internal/httpapi/profiles.go`）：

在 `read` 组（`admin`/`operator`/`viewer`）：
```go
read.Get("/profiles", a.listProfiles)
read.Get("/profiles/{id}", a.getProfile)
read.Get("/targets/{nodeId}/{runtime}", a.getTargetView)
```
在 `ops` 组（`admin`/`operator`）：
```go
ops.Post("/profiles", a.createProfile)
ops.Patch("/profiles/{id}", a.updateProfile)
ops.Put("/profiles/{id}/members", a.setProfileMembers)
ops.Delete("/profiles/{id}", a.deleteProfile)
```

`GET /api/v1/targets/{nodeId}/{runtime}` 响应（用 `writeJSON` 返回对象，**不是** `writeItems`）：

```jsonc
{
  "node": { "id": "...", "name": "project-host", "status": "online", "isLocal": true },
  "runtime": "codex",
  "capabilities": {
    "skills": true,
    "mcp": true,
    "mcpNote": ""            // grok: "MCP follows claude on this node"
  },
  "activation": {            // null 表示未激活，手工矩阵可编辑
    "profileId": "...", "profileName": "科研",
    "previousProfileId": "...", "previousProfileName": "开发",
    "state": "active",       // pending | active | partial | failed
    "lastError": "",
    "skipped": [ { "scope": "mcp", "reason": "mcp_unsupported_runtime", "detail": "" } ],
    "activatedAt": "...", "activatedBy": "..."
  },
  "mcp": {
    "mcpmProfile": "toolhub-codex",
    "deploymentId": "...", "state": "in_sync",
    "servers": [ { "id","name","runtimeName","transport","endpoint","enabled",
                   "source","originName","missing","drift" } ]
  },
  "skills": [ { "deploymentId","skillId","name","slug","desiredEnabled","actualEnabled",
                "state","desiredVersionId","actualVersionId","sha256","lastError" } ],
  "drift": { "mcp": 0, "skills": 0 }
}
```

`capabilities` 判定规则（**唯一权威来源，不要在别处重复实现**）：
- `skills` = `domain.IsSkillRuntime(runtime)`，恒为 true（5 个 runtime 全支持）。
- `mcp` = `runtime.MCPMProfileForRuntime(runtime) != ""`，即仅 codex/claude 为 true。
- `runtime == "grok"` 时 `mcp = false`，`mcpNote = "MCP follows claude on this node"`，且 `mcp.servers` 填**同节点 claude** 的有效集合作为只读展示。
- `runtime ∈ {hermes, openclaw}` 时 `mcp = false`，`mcpNote` 说明只读扫描路径。

同时更新 `api/openapi.yaml` 与 `docs/API.md`。

**Gate 1**：
- 建一个 profile、勾成员、`GET /profiles/{id}` 返回正确成员。
- `GET /targets/{localNodeId}/codex` 返回当前真实状态，`activation` 为 `null`。
- `GET /targets/{localNodeId}/grok` 的 `capabilities.mcp` 为 false 且 `mcpNote` 非空。
- **零写入副作用**：本阶段不得改动任何 `deployments` / `mcp_deployments` / 节点文件。用 `inventory_scan` 复核节点仍 `in_sync`。
- `go test ./...`、`go vet ./...` 通过。

### Phase 2 — 激活流水线

#### 2.1 预检

新增 `internal/store/profiles.go` 中的：
```go
type ActivationPreflight struct {
    OK               bool
    Errors           []ActivationIssue // 阻断
    Skipped          []ActivationIssue // 非阻断，写入 activation.skipped
    RemoteSecretKeys []string          // D12：非本机节点将下发的密钥 key 名
    NodeIsLocal      bool
}
func (s *Store) PreflightProfileActivation(ctx context.Context, profileID, nodeID, runtimeKind string) (ActivationPreflight, error)
```

阻断项（任一命中即 `OK=false`）：

| code | 条件 |
|---|---|
| `node_not_found` | 节点不存在或已归档 |
| `node_offline` | 节点既不在线也无可用 SSH 回退 |
| `runtime_unavailable` | 该 runtime 不在节点 inventory 的 runtimeKinds 中 |
| `skill_not_approved` | profile 中存在 `review_status != 'approved'` 或已归档或无 `current_version_id` 的 skill（detail 列出 slug） |
| `mcp_server_unavailable` | profile 中存在 `archived_at IS NOT NULL` 或 `authority = 'shared-file'` 的 server |
| `managed_mcp_profile_missing` | runtime ∈ {codex, claude} 但对应固定投递 profile 不存在或未通过 `managedMCPProfileRuntimeTx` |
| `remote_secret_confirmation_required` | D12：节点非本机、profile 含带 `env_refs`/`header_refs` 的 server、且请求未带 `confirmSecrets: true` |

跳过项（不阻断，写进 `activation.skipped`）：

| reason | 条件 |
|---|---|
| `mcp_unsupported_runtime` | runtime ∈ {hermes, openclaw} |
| `mcp_follows_claude` | runtime == grok |
| `empty_mcp_set` | profile 无 MCP 成员且 runtime 支持 MCP |

#### 2.2 激活事务

```go
func (s *Store) ActivateProfile(ctx context.Context, profileID, nodeID, runtimeKind, actor string, confirmSecrets bool) (domain.Job, error)
```

单事务内：
1. 再跑一次预检（TOCTOU 防护，用 `FOR UPDATE` 锁 profile 与 activation 行）。
2. upsert `toolhub_profile_activations`：`ON CONFLICT (node_id, runtime_kind) DO UPDATE SET previous_profile_id = toolhub_profile_activations.profile_id, profile_id = excluded.profile_id, state='pending', last_error='', skipped=excluded.skipped, activated_by=..., updated_at=now()`。
3. **Skills desired state**：
   - profile 成员 skill → upsert `deployments`(nodeID, runtimeKind, skillID) `desired_enabled=true`、`desired_version_id = skills.current_version_id`。
   - 该 (node, runtime) 下**已存在**但不在 profile 成员中的 `deployments` 行 → `desired_enabled=false`。
   - 两者都必须复用 `internal/store/skills.go:131-140` 的 `desired_generation` 条件自增与 `state='pending'` 语义。**不要新写 generation 逻辑。**
4. **MCP desired state**（仅当 `capabilities.mcp == true`）：
   - 确保该 (node, runtime) 存在指向固定投递 profile 的 `mcp_deployments` 行（复用 `SetMCPDeployments` 的 upsert 路径或其内部逻辑）。
   - 调 B2 改造后的 `refreshProfileDeployments`，它会按 `effectiveMCPServerIDsTx` 解析出**该节点**的有效集合并算出 per-deployment 的 `desired_hash`。
   - **不要**改写 `mcp_profile_servers` —— 那是全局的，改它会污染其他节点（正是 B2 描述的坑）。
5. `enqueueJobTx(ctx, tx, "profile_activate", payload, false, actor)`，payload：
   ```json
   { "activationId":"...", "profileId":"...", "nodeIds":["..."], "runtime":"codex",
     "skillIds":["..."], "profileIds":["<fixed mcp profile id>"] }
   ```
   `nodeIds` / `skillIds` / `profileIds` 用**复数形式**，与既有 selector 契约一致。
6. commit。

```go
func (s *Store) DeactivateProfile(ctx context.Context, nodeID, runtimeKind, actor string) error
```
删除该 (node, runtime) 的 activation 行。**不改动任何 desired state**——skills 与 MCP 停在当前状态，手工矩阵恢复可编辑。这是最不意外的语义；不要顺手关掉所有东西。

#### 2.3 Worker

`internal/worker/worker.go`：
- `execute()` 的 switch 加 `case "profile_activate": return w.activateProfile(ctx, job)`。
- `activateProfile()` 顺序执行：
  1. 用 payload 的 `nodeIds`/`skillIds` 复用 `syncSkills` 的既有逻辑下发 `deploy_skill`。
  2. 若 payload 有 `profileIds`，复用 `syncMCP` 的既有逻辑下发 `apply_mcp`。
  3. 汇总两段的 `queued`/`delivered`/`pendingOffline`/`skipped`。
  4. 回写 `toolhub_profile_activations.state`：全部 delivered → `active`；有 `pendingOffline > 0` 或任一段失败 → `partial` 并写 `last_error`；第一步就抛错 → `failed`。
- **重构方式**：把 `syncSkills`/`syncMCP` 的循环体抽成不含 `domain.Job` 依赖的内部函数（如 `dispatchSkillDeployments(ctx, selectors, dryRun, jobID)`），由三个 job kind 共用。**不要复制粘贴。**
- **不新增 Agent task kind。**

#### 2.4 API

在 `ops` 组：
```go
ops.Post("/profiles/{id}/activate", a.activateProfile)      // body: {nodeId, runtime, confirmSecrets?}
ops.Post("/profiles/{id}/preflight", a.preflightProfile)    // body: {nodeId, runtime}；只读，返回预检结果
ops.Post("/targets/{nodeId}/{runtime}/deactivate", a.deactivateTarget)
```

- 预检失败 → HTTP 409，`code` 用上表的 code，`message` 可读，并在响应体带 `issues` 数组。
- D12 命中 → HTTP 409 `remote_secret_confirmation_required`，响应体带 `{ "nodeName": "...", "secretKeys": ["TAVILY_API_KEY", ...] }`。**只返回 key 名，绝不返回值。**
- 激活成功 → 写审计：`Action: "profile_activate"`, `ResourceType: "toolhub_profile"`, `ResourceID: profileID`, `Metadata: {nodeId, runtime, previousProfileId, skipped, remoteSecretKeys}`。`remoteSecretKeys` 只记 key 名。
- 停用 → 审计 `Action: "profile_deactivate"`。

**D9 强制**：`setSkillTargets`（`internal/httpapi/skills.go`）必须新增校验——若目标 (node, runtime) 存在 activation 行，返回 HTTP 409 `target_managed_by_profile`。前端置灰是提示，**后端拒绝才是保证**。同理 `deployMCPProfile` 与 `setMCPProfileServers` 命中已激活 target 时也要拒绝。

**Gate 2**（在本机 project-host 上做，先备份）：
- 建两个 profile（一个含 3 个 MCP + 5 个 skill，一个含 1 个 MCP + 2 个 skill）。
- 激活 A 到 `project-host/codex` → job 成功，`activation.state='active'`，`GET /targets/.../codex` 显示正确集合。
- 检查 `~/.config/mcpm/servers.json`：`toolhub-codex` 的 `profile_tags` 只含 A 的成员；**`~/.codex/config.toml` 的 `[mcp_servers.toolhub-codex]` 与备份逐字节一致**（D1 硬指标）。
- 检查 `~/.codex/skills`：只有 A 的 5 个 skill 是 managed 目录，其余进了 `.toolhub-disabled`；Phase 0 之前存在的 4 个非托管目录**原封不动**。
- 切到 B，再切回 A，再切到 B（**四次切换**）→ 全部成功，验证 B1 修复。
- 激活到 `project-host/hermes` → Skills 生效，`skipped` 含 `mcp_unsupported_runtime`，`~/.hermes/config.yaml` **未被写入**。
- 对已激活的 target 调 `POST /skills/{id}/deployments` → 返回 409 `target_managed_by_profile`。
- 停用后同一调用恢复 200。
- 全部 8 个 MCP server 逐个 `initialize` + `tools/list` 探测通过；Codex 新会话能列出 A 的工具。
- `go test ./...`、`go vet ./...`、`internal/runtime` 与 `internal/store` 的 race 测试、PostgreSQL 集成测试全过。

### Phase 3 — UI

`web/src/pages/Profiles.tsx`（新页）：
- 列表：名称、描述、MCP 数、Skills 数、激活到几个 target。
- 编辑：两个分组的勾选列表（MCP servers / Skills），带搜索 + provenance 筛选。
- 激活：选 (节点, runtime) → 先调 `/preflight` 展示结果（含跳过项和将下发的密钥 key 名）→ 用户确认 → 调 `/activate`。**D12 的确认弹窗只对非本机节点弹**，且必须显示节点名与 key 名清单。

`web/src/pages/Targets.tsx`（新页，Runtime 视图）：
- 顶部选 (节点, runtime)，下面三块：当前 activation（含 state / skipped / lastError / 重试 / 停用）、生效 MCP 列表、生效 Skills 列表，各自标 drift/missing。
- `capabilities.mcp === false` 时 MCP 区块显示 `mcpNote` 的说明条。

`web/src/App.tsx`：
- `navigation` 加两项：`{ path: '/profiles', label: 'Profiles', icon: Layers }`、`{ path: '/targets', label: 'Runtime View', icon: MonitorCog }`（图标从 `lucide-react` 选现有的）。
- 路由分发处加对应分支，把 `canOperate` 传给两页做按钮门禁。

`web/src/pages/Skills.tsx`：
- 工具栏加 provenance 下拉（选项从 `skill.sourceKind` 实际取值动态生成 + "全部"）。
- `TargetModal` 中，对存在 activation 的 (node, runtime) 单元格 `disabled` 并加 `title` 说明"由 profile X 接管"。需要额外拉一次 `/profiles/activations` 或复用 `/targets`——**建议在 `GET /nodes` 响应里带上 `activations: [{runtime, profileName}]`**，避免 N 次请求。

`web/src/pages/MCP.tsx`：
- Servers 页签加 provenance 下拉（`source` + `origin.importSourceName`）。
- 每行加"归档/取消归档"动作（D8），默认列表过滤掉已归档，加一个"显示已归档"开关。

`web/src/api/client.ts`：加对应方法，走既有 `api` 单例，不要另建请求层。

`web/src/i18n.tsx`：**所有新增英文串都要补 zh 词条**。现有词典约 474 行，按分节注释归入合适分组。

**Gate 3**：
- `cd web && npm run typecheck && npm run build` 通过。
- `npm run test:e2e` 通过（后端需已运行；Playwright 无 webServer 设置）。
- 新页面的 Playwright 冒烟：导航可达、角色门禁生效（viewer 看不到激活按钮）、桌面与移动两个 project 都过。

### Phase 4 — 文档与收尾

- `api/openapi.yaml`：补全 `/profiles*`、`/targets*` 全部端点。
- `docs/API.md`：说明新端点的信封与鉴权。
- `AGENTS.md`：
  - job kinds 列表加 `profile_activate`。
  - "Core invariants" 增加："某 (node, runtime) 存在 ToolHub Profile 激活时，其 Skills/MCP desired state 由该 profile 独占；手工 target 设置接口必须拒绝。"
  - "Known limits" 移除或修订与 B1/B2 相关的表述。
- `README.md`：功能列表补一句 Profile 与 Runtime 视图。
- 本文件状态改为 completed 并附执行记录。

**Gate 4**：`make lint`、`make test`、`make build`、`make docker-config` 全过；`git diff` 无生成物、无密钥、无迁移改写。

---

## 明确不做的事

- **不改锚点**。`~/.claude.json` 与 `~/.codex/config.toml` 的锚点条目在本次改动中一次都不该被重写。任何导致锚点变动的实现都是错的。
- **不新增 Agent task kind**，不加任意 shell 执行。
- **不给 Skills 或 MCP 加 runtime 适用性字段**（D5）。
- **不改 MCP 自动 baseline 导入**（D8），不把 MCP 改成 adopt 流程。
- **不做自动回滚**（D11）。
- **不给 hermes / openclaw 实现 MCP 写入器**。
- **不做多 profile 叠加 / base+mode 分层**（D3 已定单激活）。
- 不引入通用的 API 响应脱敏中间件；沿用既有的 `RedactMap`/`RedactJSON` 具体调用点。
- 不扩大 `internal/httpapi/resources.go` 与 `settings.go` 里直接 `Pool().Exec` 的写法，新持久化逻辑一律进 `internal/store`。

---

## 风险登记

| 风险 | 影响 | 缓解 |
|---|---|---|
| B2 重构破坏既有 MCP 投递 | 现网 8 个 server 失效 | "无激活时输出逐字节一致"作为验收硬指标 + 集成测试 |
| `profile_activate` 半途失败 | target 处于混合状态 | D11：state 标 `partial` + lastError + 重试按钮；UI 不得显示为 `active` |
| 多节点并发激活同一 profile | 竞态 | 事务内 `FOR UPDATE` 锁 profile 与 activation 行；`UNIQUE(node_id, runtime_kind)` 兜底 |
| 跨节点密钥外泄 | 研究用 API key 落到别的机器 | D12 二次确认 + 审计；响应只含 key 名 |
| grok 被 claude 激活静默影响 | 用户困惑 | B3：Runtime 视图显式标注，激活时记 skipped |
| `deployments` 与 job 入队非原子 | 与既有 `setSkillTargets` 同类问题 | 沿用既有范式；`profile_activate` 的 state 机器使半成品可见可重试 |

---

## 给实施者的提醒

- 动手前先读：`AGENTS.md`、`internal/store/mcp.go`、`internal/store/skills.go`、`internal/worker/worker.go`、`internal/runtime/deploy.go`、`internal/runtime/mcpm.go`、`internal/runtime/mcp_anchor.go` 及各自测试。
- 高风险边界（改动需先补回归测试）：`internal/security`、`internal/httpapi/middleware.go`、`internal/protocol/task.go`、`internal/runtime/deploy.go`、`internal/skills/package.go`、SSH 回退路径。
- `make lint` 会执行 `gofmt -w`；`make web` 会重写被忽略的 dist 产物。两者都会改文件系统。
- 迁移一律新增编号文件，**绝不改写已应用的迁移**。
- 每阶段结束跑 `git diff` 自查：有没有误提交生成物、密钥、无关改动。

---

## 执行记录（2026-07-28）

已完成：

- Phase 0：Skill enable/disable 可重入；MCP membership、hash、binding 与 Agent secret authorization 改为按 `(node, runtime)` 的有效 Profile 解析，并保留无 activation 时的旧行为。
- Phase 1/2：migration 009、ToolHub Profile CRUD/members/preflight/activation/deactivation、Runtime View、D9 store guards、D12 key-name confirmation/audit、`profile_activate` worker orchestration与 MCP archive/unarchive。
- Phase 3：Profiles 与 Runtime View 页面、Skills/MCP provenance 筛选、Profile-managed target 只读提示、structured API error details、中英文词条及 desktop/mobile RBAC/D12 Playwright coverage。
- Phase 4：OpenAPI、API 文档、README、AGENTS.md 与本计划状态已同步。

验证通过：

```bash
go test ./...
go vet ./...
go test -race ./internal/runtime ./internal/store ./internal/worker ./internal/httpapi
make lint test build docker-config
cd web && npm run build
cd web && npm audit --audit-level=high

TOOLHUB_TEST_DATABASE_URL='postgres://toolhub:***@127.0.0.1:25432/toolhub_discovery_test?sslmode=disable' \
  go test ./internal/store/... -count=1 -v
```

- 隔离 Compose project `toolhub-profiles-smoke-20260728` 成功构建并 healthy；`schema_migrations` 包含 version 9，`GET /healthz` 与 `scripts/smoke-api.sh` 通过。
- Playwright desktop/mobile 共 6 项通过：core navigation、viewer mutation-control gate、operator D12 confirmation；截图复核无 sidebar 遮挡或 incoherent overlap。
- OpenAPI YAML parse、405 个 literal translation key coverage、`git diff --check`、生成物/测试密钥检查全部通过。

未对用户现有 runtime home 执行 Gate 2 中的 live MCP `initialize` / `tools/list` 与真实锚点文件切换；相关数据面、secret scope、per-target payload/hash 与 B1 四步切换由 PostgreSQL integration test 和 runtime regression test 覆盖，避免修改现有 `~/.codex`、`~/.claude.json` 或正在运行的 ToolHub stack。
