# ToolHub 初始构建工作流

## 概述

在 `/root/docker/toolhub` 完成 Go control plane、Go Agent、React/TypeScript UI、PostgreSQL schema、Docker Compose 与跨平台交付骨架。

## 用户需求与提示词

目标是通过 Tailscale 管理 Codex、Claude、Hermes Skills 与 MCP，采用 Agent 优先、SSH fallback、审查后部署、update/sync 分离、RBAC、审计和回滚。

## 工作流记录

先建立 `.builder` 持久状态，再按 foundation、security、Skill intake、reconciliation、Agent、UI、verification 七个纵向切片实现。每个风险边界先定义不变量和数据模型。

## 修改内容

新增控制平面、Agent、runtime adapters、worker/scheduler、REST/OpenAPI、8 个运维页面、migration、容器配置、CI 与运维文档。

## 遇到的错误

主要错误包括 patch transaction 组织、Go 传递依赖抬高 toolchain、旧 Vite/Playwright advisory、Compose hardening 放错服务，以及未登录 session probe 的浏览器 401 console error。

## 根本原因分析

错误分别来自 patch engine 限制、未固定测试传递依赖、旧 dev toolchain、YAML 上下文匹配不精确和 bootstrap endpoint 的 HTTP 语义选择。

## 调试过程

采用 error signature 和 checkpoint 记录；依赖问题通过 audit/最小固定处理；Compose 使用真实容器日志定位；UI 使用 desktop/mobile Playwright 和截图边界检查验证。

## 经验总结

高风险控制台应先锁定 desired/actual、approval/reconcile、artifact/provenance 三组不变量。真实 migration 与浏览器 smoke 比只做编译更早暴露系统集成错误。

## 知识提炼

可复用模式包括 canonical task signing、Agent secret reference authorization、unmanaged target conflict、内容寻址 cache、update candidate 与 desired state 原子分离。

## 测试与验证

Go unit tests、TypeScript typecheck、npm audit、production frontend build、Linux/macOS/Windows Agent cross-build、Docker image、PostgreSQL migration、API auth/CSRF smoke、desktop/mobile Playwright 均执行。

## 参考资料

项目内 `api/openapi.yaml`、`docs/SECURITY.md`、`docs/ROLLOUT.md` 与 `.builder/architecture.md`。

## 指标

实现 10 个 feature groups；npm audit 0 vulnerability；Playwright 2/2 通过；API smoke 4 个关键步骤通过；Agent 3 个目标平台编译通过。
