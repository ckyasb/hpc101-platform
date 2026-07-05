# hpc101-platform

课程容器化平台。学生通过 SSH 接入独立开发环境，CSOJ 作为零改动评测后端。

## 架构概览

```
学生 ──ssh -J bastion──► SSH Bastion  ──►  容器内 sshd (svc- 前缀)
                            │
                    平台 controller ──► CSOJ Adapter ──► CSOJ (仅 judge)
                            │                               │
                     ┌──────┴──────┐              DockerConfig.Host
                     │ Podman Runtime (rootless, docker-compat TCP :2375) │
                     └───────────────────────────────────────────────────┘
```

- **接入**: 仅 SSH（堡垒机 + 转发），不引入 kubeconfig / OIDC。
- **评测**: CSOJ 零改动，`DockerConfig.Host` → podman runtime。
- **服务**: 交互式开发环境 + 题目服务组件经 podman runtime 以 docker 兼容 API 起停。
- **自动关闭**: 最大存活 + 空闲超时 + 手动释放。

## 仓库结构

```
vendor/csoj/          # CSOJ (git subtree, zero-change, github.com/ZJUSCT/CSOJ)
deploy/
  podman-runtime/     # Rootless podman k8s manifests + hardening verification
  csoj/               # CSOJ config overlay (DockerConfig.Host → runtime)
reference/            # Read-only reference material (gitignored)
plan.md               # Implementation plan (gitignored)
.humanize/            # Humanize tooling (gitignored)
```

## 快速开始

见 `deploy/` 下的各组件 README 与清单。
