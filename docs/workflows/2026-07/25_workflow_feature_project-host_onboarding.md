# Project-host onboarding 工作流

> Historical generation-1 report. Agent enrollment and SSH fallback were
> removed by the generation-2 Salt Bridge refactor; do not use this document as
> a deployment runbook. See [`docs/DEPLOYMENT.md`](../../DEPLOYMENT.md).

## 概述

完成项目所在机器的默认节点 onboarding，并将 Agent enrollment、SSH fallback 和单节点 canary 的可配置部分放入 ToolHub UI。

## 用户需求与边界

默认管理当前项目宿主机。Agent 与 SSH 可从页面配置；Tailscale Serve/ACL 仍属于外部基础设施，不由 ToolHub 自动修改。真实 enrollment、SSH 连接和 canary 需要目标机器与密钥，因此保留为 rollout 步骤。

## 实现决策

- Server 启动时 transactionally bootstrap 一个 `project-host`，同名 Agent enrollment 直接 claim 原 UUID。
- Nodes 页面提供可复制的 Agent command，以及 `user@host`、单行 pinned `known_hosts`、encrypted private key 表单。
- SSH secret 与 connection 在同一 transaction 写入；替换时只停用旧 fallback，不读取或回显旧 key。
- Skill target matrix 将 project host 排在首位，只预选 inventory 已发现的 Codex、Claude、Hermes runtimes。
- Enrollment command 只接受规范化 HTTP(S) origin，避免 shell command injection。

## 验证

`go test -race ./...`、`go vet ./...`、Vite production build、`npm audit --audit-level=high`、Compose config、Docker image、health/API smoke、local-node rename invariant、desktop/mobile Playwright 和截图 review 均通过。改名检查保持同一 UUID，local node count 始终为 1。

## Residual risks

真实 Tailnet Agent、SSH key 和单节点 canary 尚未执行；Tailscale Serve/ACL 需要按目标 Tailnet 的策略人工配置。
