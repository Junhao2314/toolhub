# ToolHub 与 mcpm 仓库直运行迁移设计

## 目标

将本机 ToolHub 与 mcpm 收敛为一套可追溯运行时：服务 executable
直接来自 `/root/docker/toolhub` 与 `/root/docker/mcpm`，移除旧 pipx mcpm、
系统路径旧 binary、legacy ToolHub Agent、Vite dev server 和遗留测试容器。

迁移只更换程序与服务入口，不迁移或重建持久化数据。现有 PostgreSQL
volume、Bridge HMAC key、BoltDB journal、Bridge backups、mcpm Profile/config
和 ToolHub singleton account 必须原地保留。

## 当前事实

### mcpm

- 当前 relay `toolhub-mcpm-relay.service` 执行 `/usr/bin/mcpm`。
- `/usr/bin/mcpm` 与 `/root/.local/bin/mcpm` 是相同 pipx launcher，均使用
  `/root/.local/share/pipx/venvs/mcpm` 的公开 `2.15.0`。
- 旧版本不支持 `mcpm toolhub contract --json`。
- `/root/docker/mcpm/.venv` 是 editable install，来源固定为当前仓库，能够
  输出 `2.15.0-toolhub.1` capability contract。

### ToolHub

- HTTP backend 只有一套正式 Compose stack，project/config 已归属
  `/root/docker/toolhub/compose.yaml`，PostgreSQL 使用 named volume
  `toolhub_postgres_data`。
- 当前 ToolHub image 早于仓库最终提交，需要从当前 `main` 重建。
- `toolhub-bridge.service` 执行旧的
  `/usr/local/sbin/toolhub-bridge`，而不是仓库 build artifact。
- 安装的 relay unit 仍是旧模板，缺少 routing file、admin socket 与
  observation limit 参数。
- 存在一个 `/root/docker/toolhub/web` Vite dev process。
- 存在 `toolhub-task9-postgres` 与 `toolhub-task12-test-postgres` 两个无
  Compose labels 的测试容器及匿名 volumes。
- disabled `toolhub-agent.service`、`/usr/local/bin/toolhub-agent`、
  `/etc/toolhub-agent` 和 `/var/lib/toolhub-agent` 是 generation-1 遗留。

## 目标布局

### 活跃服务

| 服务 | 唯一入口 |
| --- | --- |
| ToolHub HTTP | `/root/docker/toolhub/compose.yaml` build 的 `toolhub` container |
| PostgreSQL | 同一 Compose project 的 `toolhub-postgres` 与既有 named volume |
| Bridge | `/root/docker/toolhub/bin/toolhub-bridge` |
| shared mcpm relay | `/root/docker/mcpm/.venv/bin/mcpm` |

systemd unit 文件仍由 `/etc/systemd/system` 加载，但它们由仓库内 packaging
生成，并将 `ExecStart`、`ExecStartPre` 和 `WorkingDirectory` 指向上述仓库。
持久化状态继续使用标准 root-owned 路径：

- `/etc/toolhub-bridge/hmac.key`
- `/var/lib/toolhub-bridge/journal.db`
- `/var/lib/toolhub-bridge/backups`
- `/var/lib/toolhub-bridge/mcpm-relay.env`
- `/run/toolhub-bridge/bridge.sock`
- `/run/toolhub-mcpm/relay.sock`
- `toolhub_postgres_data`
- `/root/.config/mcpm`

不创建 `/usr/bin/mcpm`、`/usr/local/bin/mcpm` 或其他全局 mcpm launcher。

## 仓库改动

修改 ToolHub systemd packaging，使 installer 接受并 canonicalize 两个明确
的 repository root：

- ToolHub repository root
- mcpm repository root

installer 必须验证：

1. 路径是绝对、canonical、非 symlink directory。
2. Bridge binary 是 root-owned regular executable。
3. mcpm executable 是 root-owned regular executable，其 shebang/interpreter
   均位于 mcpm repository `.venv`。
4. 以 managed user 在 5 秒 timeout 内运行 capability contract，并验证
   admin protocol 1、routing schema 1 和全部 required features。
5. unit 只包含由 installer 解析出的 canonical fixed paths；Browser、Bridge
   request 或环境变量不能覆盖 executable/path。

Bridge unit 的 `ExecStart` 指向 repository `bin/toolhub-bridge`。Relay unit
的 `ExecStartPre` 指向 repository packaging 中的 fixed-port check，
`ExecStart` 指向 repository `.venv/bin/mcpm`，`WorkingDirectory` 指向 mcpm
repository，并保留 routing/admin/observation 参数和既有 sandbox。

## 轻量备份

备份目录使用 root-only
`/var/backups/toolhub-repository-migration/YYYYMMDDTHHMMSSZ`，目录名在
preflight开始时按UTC生成一次并固定，mode `0700`。只保存完成rollback所需的
小集合：

1. PostgreSQL compressed logical dump；不复制整个 Docker volume。
2. 当前 ToolHub/relay/legacy Agent unit files 与 unit enablement/status inventory。
3. `/etc/toolhub-bridge`、`mcpm-relay.env` 和 ToolHub `.env`，归档 mode `0600`，
   不向终端输出内容。
4. 当前 `/usr/local/sbin/toolhub-bridge` binary、旧 mcpm launcher metadata、
   pipx package/version inventory 和 Docker image/container digest inventory。
5. 即将删除的 `/etc/toolhub-agent`、`/var/lib/toolhub-agent`、Agent binary 与
   unit，压缩为一个 root-only archive。

以下数据保持原地且不重复备份：PostgreSQL named volume、Bridge journal、
Bridge backups、两个 Git repositories、mcpm Profile/config。迁移不会修改
Bridge key、journal 或 backups；PostgreSQL logical dump覆盖数据库恢复需求。

每个备份文件记录 SHA-256、owner、mode 和 size。dump、archive 或 checksum
任一步失败都必须在停服前终止迁移。

## 迁移流程

### 1. Preflight

- 确认两个 repository `main`、HEAD、working-tree 状态和依赖 lock。
- 在仓库中重新构建 `bin/toolhub-bridge`，验证 build metadata 对应当前 HEAD。
- 执行 mcpm full tests、Ruff、build 和 capability contract。
- 执行 ToolHub focused systemd/Bridge/runtime tests以及完整 precommit gates。
- 检查端口 `6276`、`18480` 与两个 Unix sockets 的当前 owner/process。
- 创建并验证轻量备份。

### 2. 切换 Bridge

- 写入 repository-path Bridge unit并执行 `daemon-reload`。
- restart Bridge；验证 MainPID executable、socket `0660 root:toolhub`、HMAC
  health 和 journal可打开。
- 失败时恢复原 unit 与旧 Bridge binary，restart 并停止后续步骤。

### 3. 切换 relay

- 写入 repository-path relay unit。
- stop旧 relay，restart新 relay；不改变固定端口或 mcpm Profile。
- 验证 MainPID executable 位于 `/root/docker/mcpm`、capability contract、
  `127.0.0.1:6276`、admin socket、routing hash/status 和 session canary。
- 失败时恢复旧 unit并从仍未卸载的 pipx mcpm启动旧 relay。

### 4. 重建 ToolHub Compose

- 使用现有 `.env` 和 `toolhub_postgres_data` 执行
  `docker compose up -d --build --wait`。
- 不执行 `down -v`，不替换或删除 PostgreSQL volume。
- 验证 `/healthz` 为 `status=ok, bridge=ok`，再执行 authenticated API smoke。
- 失败时停止清理阶段并保留旧组件用于 rollback。

### 5. 成功后清理

只有 Bridge、relay、ToolHub 和 smoke 全部通过后才清理：

- `pipx uninstall mcpm`，保留 pipx 管理的其他 package（例如 `uv`）。
- 删除 `/usr/bin/mcpm` 手工 launcher和遗留 `/root/.local/bin/mcpm`。
- 删除 `/usr/local/sbin/toolhub-bridge` 与
  `/usr/local/sbin/toolhub-relay-port-check`。
- disable/remove `toolhub-agent.service`，删除 Agent binary、config 和 state；
  其删除前内容已在轻量备份归档。
- 向 Vite parent发送 SIGTERM，确认 `18481` 不再监听。
- 删除两个明确命名的测试 PostgreSQL containers及其两个匿名 volumes。
- 删除被替代且不再被任何 container引用的 ToolHub旧 image；不执行全局
  `docker system prune`。
- `systemctl daemon-reload` 并确认不存在 failed/stale ToolHub unit。

所有删除目标必须在删除前再次按 exact path/name/inode 或 container ID解析；
不使用 glob、宽范围 package purge、recursive Docker prune 或 Git destructive
command。

## Rollback

清理之前的任一步失败：恢复备份的 units，使用仍存在的旧 binary/pipx
installation重启，并保持原 Compose stack与volume。

清理之后如需 rollback：

- 从 backup恢复旧 units、Bridge binary和legacy config（如确有需要）。
- 按 inventory中的 exact version重装 pipx mcpm。
- 数据库只在确认新 binary已写入不兼容状态时使用 logical dump恢复；普通
  executable rollback不回滚数据库。
- Bridge journal、backups和PostgreSQL volume始终原地保留。

## 验收标准

1. `toolhub-bridge.service` 与 `toolhub-mcpm-relay.service` active，MainPID
   executable均解析到 `/root/docker` 下目标。
2. 只有一套 mcpm Python environment；pipx inventory不含 mcpm，
   `/usr/bin/mcpm` 与 `/root/.local/bin/mcpm` 不存在。
3. `/usr/local/sbin/toolhub-bridge`、legacy Agent unit/binary/config/state不再
   存在。
4. `18481` 不监听；两个遗留测试 containers和匿名 volumes不存在。
5. ToolHub Compose labels继续指向 `/root/docker/toolhub/compose.yaml`；
   `toolhub_postgres_data` identity不变。
6. `6276` 由 repository mcpm监听，Bridge/admin sockets owner和mode正确。
7. mcpm capability、Bridge health、ToolHub health、API smoke与双客户端
   session canary通过。
8. `/etc/toolhub-bridge`、`/var/lib/toolhub-bridge`、mcpm Profile/config和
   PostgreSQL数据无丢失；root-only轻量备份存在且checksums通过。

## 非目标

- 不修改Salt Master配置或放宽Salt `3008.x` gate。
- 不执行fleet Apply或修复当前不可用minion。
- 不改变ToolHub singleton credentials、master key、Bridge HMAC key、relay
  port或managed username。
- 不删除其他 pipx package、其他 Docker project、未明确命名的 image/volume
  或用户的 Git working-tree修改。
