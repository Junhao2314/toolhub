# ToolHub / mcpm MCP 与 Profile 边界需求

状态：accepted（2026-08-17）

## 目标

ToolHub Web/DB 是共享 MCP 配置的声明式编辑面；mcpm 是唯一的共享 relay、
registry 和 MCP upstream process owner。Profile 不再承载 MCP membership、
governance、tool visibility 或 call-policy，只管理可交付的 Skill 版本。

## 行为要求

- MCP 页面支持创建、编辑、启用、停用和删除 MCP 配置；secret 仍为
  write-only，数据库只保存加密引用。
- MCP 创建/编辑可直接粘贴标准客户端 JSON：顶层为 `mcpServers`，且本次
  请求只允许一个服务；每个服务只接受 `type`、`command`、`args`、`url`、
  `env`、`headers`、`description`。解析后仍走同一套固定传输、路径和密钥
  校验，未知字段或多个服务一律拒绝。
- MCP 的 enabled 集合由 Relay Configuration revision 表示。变更通过既有
  typed Bridge/operation/backup/rollback 链路交给 mcpm；ToolHub 不执行任意
  mcpm command，也不代理 MCP tool traffic。
- mcpm `toolhub` Profile、共享 HTTP relay 和 upstream process lifecycle 由
  mcpm 自己维护；ToolHub 只投影期望配置、操作结果和 bounded health。
- relay 健康检查在 compatibility/pass-through 模式不依赖已退役的
  governance admin socket。HTTP/systemd 正常且 mcpm 可用时，成员显示
  `ready`，并从真实 Streamable HTTP `tools/list` 聚合响应按成员前缀统计
  tools/resources/prompts；停用成员显示 `disabled`；真实进程或 relay
  故障才显示 `unavailable`。
- Profile/ProfileRevision/ProfileInput、Profile Bundle、Preflight/Apply
  manifest 与浏览器 API 只暴露 Skills。旧 MCP 字段被 strict JSON 解码拒绝，
  不再影响 Profile Apply、relay health、startup 或 reconcile。
- 旧 `shared-mcp` Profile 不是 MCP/relay owner；迁移后不出现在普通 Profile
  列表，历史和其专属审计/操作/快照按批准范围清理。

## 数据清理范围

一次性 migration `013_skill_only_profile_and_cleanup.sql` 精确清理；随后
`014_retired_object_history_cleanup.sql` 补清理只出现在旧 operation result
中的 relay member/Skill history，`015_remove_profile_mcp_governance_history.sql`
再移除所有残留 Profile MCP governance、旧 Profile Apply/preflight 和相关
transient history；`017_remove_text_processing_profiles.sql` 删除已归档且不再需要的
`claude-text-processing` / `codex-text-processing` Profiles 及其全部历史 revision：

- MCP：`desktop-commander`、`memory`、`sequential-thinking`；其 revisions、
  relay references、contracts/tools、secret rows、snapshots、operation
  targets、专属 audit/history。
- Skills：`baoyu-format-markdown`、`baoyu-translate`、
  `baoyu-url-to-markdown`、`codex-build`、`codex-review`、`grill-me-codex`、
  `grill-with-docs-codex`、`slides`、`using-superpowers`、`workflow-runner`；
  清理历史 Profile references、versions、artifacts 和孤儿 sources。
- 删除旧 `shared-mcp` Profile 及其专属历史；保留 account/session/security
  数据与未列入范围的 MCP、Skill、Profile、backup、audit。
- 删除 `claude-text-processing`、`codex-text-processing` 两个已归档 Profile
  及其 Profile/Skill membership 历史；这些 Profile 使用的 Skills 若仍被
  其他 Profile 引用则保留，不做扩大范围的 Library 卸载。
- 普通 Profile 的 Skill revision history 可以保留，但
  `profile_revision_mcp_governance`、`profile_revision_mcp_servers`、
  `profile_mcp_servers`、Profile MCP tool rules 和 pending MCP secret
  bindings 必须为零。

迁移使用事务、精确 ID/name 集合、临时禁用 immutable triggers 并在结束时
恢复；若发现 active Profile head 重新引用目标 Skill，则该 Skill 不删除并
让迁移失败，避免误删新引用。

## 验收证据

1. 新 Profile API/JSON schema 不接受 MCP membership/governance 字段。
2. MCP list/detail 返回 `enabled` 投影；relay member health 不再因缺少
   governance socket 全部变为 `unavailable`。
3. 迁移 013-017 在 generation-2 clone 上成功执行，FK/triggers 恢复，目标名称、旧
   Profile 和 active Skill references 均为零，并保留六个当前 mcpm members。
4. Skill Profile revision/hash 变化不改变 active Relay Configuration routing
   hash；`/healthz`、`toolhub-mcpm-relay.service`、`/usr/libexec/toolhub-mcpm
   toolhub contract --json` 和完整 Go/web gates 通过。
