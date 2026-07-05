# hpc101-platform

课程容器化平台。学生通过 CLI 工具 `hpc101` 申请开发容器，经 SSH Bastion 跳板连接，CSOJ 作为零改动评测后端。

## 架构概览

```
学生笔记本                    集群内                         m800
    │                           │                             │
    │ ① hpc101 up (HTTPS)      │                             │
    └─────── Controller ───── Docker API ────► Docker daemon │
    │        (k8s Pod)          │              (:2375)       │
    │           │               │                │           │
    │           │ 签发 SSH 证书  │                │ 创建容器   │
    │           ▼               │                ▼           │
    │      SSH Bastion ──ProxyJump──►  学生容器 (sshd :2222) │
    │       (k8s Pod)          │         Debian 12           │
    │           │               │         /home/student 持久化│
    │ ② ssh hpc101-container   │                             │
    └─── OpenNG 网关 ──────────┘                             │
         :22 (校园网)
         :443 (公网)
```

- **接入**：仅 SSH CA 证书认证，经 Bastion 跳板转发。不引入 kubeconfig / OIDC。
- **容器**：Debian 12 完整开发环境，sshd pubkey 认证，`/home/student` 持久化卷。
- **评测**：CSOJ 零改动，`DockerConfig.Host` 指向 m800 Docker Runtime。
- **释放**：最大存活 8 小时 + 空闲 30 分钟 + 手动 `hpc101 release`。

## 仓库结构

```
cmd/
  hpc101/            学生 CLI (替代版本: hpc101.sh 纯 shell 脚本)
  controller/        平台 Controller HTTP API
  bastion-mgmt/      Bastion 管理服务 (drain/reject)
pkg/
  controller/        Handler + Store + Idle/Release 触发器
  lease/             租约状态机 (Active→Closing→Draining→Stopped→Reclaimed)
  runtime/           Docker 客户端 (CreateContainer / StopAndRemove / etc.)
  adapter/           CSOJ 评测适配器 (submit / poll / logs / score)
  sshca/             SSH CA 签发 (短期学生证书)
deploy/
  bastion/           Bastion Deployment + sshd_config + principals 脚本
  container/         学生容器镜像 (Debian 12 + sshd + 开发工具)
  controller/        Controller Deployment + RBAC
  csoj/              CSOJ 配置 overlay
  dedicated-judge/   m800 Docker daemon 安全加固配置
  rbac/              hpcgame-judger 最小权限 ClusterRole
docs/
  README.md          本文件
  TUTORIAL.md        学生使用手册
  CSOJ-DOCKER-CONTRACT.md  CSOJ Docker API 契约文档
vendor/              CSOJ git subtree (zero-change)
reference/           只读参考 (gitignored)
```

## 快速开始

见 `docs/TUTORIAL.md` 学生使用手册，以及 `deploy/` 下各组件 README。

## API 端点

Controller 通过 Envoy Gateway 在 `https://clusters.zju.edu.cn/hpc101` 暴露：

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/healthz` | 健康检查 |
| POST | `/api/v1/keys` | 注册 SSH 公钥 |
| POST | `/api/v1/services` | 创建开发容器 |
| GET | `/api/v1/leases?principal=` | 查询活跃租约 |
| GET | `/api/v1/ssh-info?principal=` | 获取 SSH 连接配置 |
| DELETE | `/api/v1/release?principal=` | 释放容器 |
| POST | `/api/v1/submissions` | 提交评测 |
| GET | `/api/v1/scores` | 查看分数 |
| GET | `/api/v1/submissions/logs/` | 查看评测日志 |
| POST | `/api/v1/problems/sync` | 管理员同步题目到 CSOJ |
