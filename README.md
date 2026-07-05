# hpc101-platform

课程容器化平台。学生通过 CLI 工具 `hpc101` 在公网/校园网申请独立开发容器，经 SSH Bastion 跳板连接，CSOJ 作为零改动评测后端。

## 架构

```
                             ┌──────────────────────┐
公网 (443) / 校园网 (22)      │   OpenNG SSH Gateway │
          │                  │   clusters.zju.edu.cn│
          │  username+bastion │                      │
          └──────────────────┤  路由: bastion →      │
                             │  bastion...svc:2222   │
                             └──────────┬───────────┘
                                        │
              ┌─────────────────────────┼─────────────────────────┐
              │   Kubernetes 集群 (hpc101-platform)               │
              │                                                   │
              │  ┌─────────────┐    ┌──────────────────┐         │
              │  │ Controller  │    │  SSH Bastion     │         │
              │  │ :8080       │    │  :2222           │         │
              │  │             │    │  CA 证书认证      │         │
              │  │ /api/v1/*   │    │  AuthorizedPrinc- │         │
              │  │             │    │  ipalsCommand     │         │
              │  └──────┬──────┘    └────────┬─────────┘         │
              │         │                    │                    │
              │         │ Docker API         │ ProxyJump          │
              │         │ (TCP :2375)        │ (permitopen)       │
              └─────────┼────────────────────┼────────────────────┘
                        │                    │
              ┌─────────▼────────────────────▼──────────────────┐
              │              m800 物理机                         │
              │                                                 │
              │  Docker Daemon (:2375)                           │
              │  ┌──────────┐  ┌──────────┐  ┌──────────┐      │
              │  │svc-alice │  │svc-bob   │  │svc-...   │      │
              │  │sshd:2222 │  │sshd:2222 │  │          │      │
              │  │Debian 12 │  │Debian 12 │  │          │      │
              │  │持久化卷  │  │持久化卷  │  │          │      │
              │  └──────────┘  └──────────┘  └──────────┘      │
              └─────────────────────────────────────────────────┘
```

### 网络分层

| 网络层 | 用户位置 | 访问方式 | 端口 |
|--------|---------|---------|------|
| 公网 | 校外 | OpenNG SSH Gateway → Bastion → 容器 | 443 |
| 校园网 | 校内 | OpenNG SSH Gateway → Bastion → 容器 | 22 |
| 集群内网 | — | Pod 间直连（学生不可达） | — |

### 数据流

1. **`hpc101 up`**：学生 → Envoy Gateway(HTTPS) → Controller → Docker API → m800 创建容器 + 签发 SSH 证书
2. **`ssh hpc101-container`**：学生 → OpenNG SSH Gateway → Bastion(CA 验书 + permitopen) → 容器 sshd
3. **`hpc101 submit`**：学生 → Controller → CSOJ Adapter → CSOJ 评测
4. **`hpc101 release`**：学生 → Controller → 停止容器 → 回收卷 → 清理防火墙规则

## 组件

### CLI

| 组件 | 路径 | 说明 |
|------|------|------|
| `hpc101` | `cmd/hpc101/` | 学生命令行工具。编译为单二进制，公网 HTTPS 直连 Controller。 |

### 集群服务

| 组件 | 路径 | 部署 |
|------|------|------|
| Controller | `cmd/controller/` + `pkg/controller/` | k8s Deployment。HTTP API，签发 SSH 证书，管理租约状态机，调用 Docker API 创建/回收容器。 |
| Bastion | `deploy/bastion/` + `cmd/bastion-mgmt/` | k8s Deployment。SSH 跳板，CA 证书认证，AuthorizedPrincipalsCommand 查询 Controller 实现 per-user permitopen 绑定。 |
| CSOJ Adapter | `pkg/adapter/` | 评测适配器。封装 CSOJ 的 submit/poll/log/score HTTP 契约，处理 base64 文件名和 auto-ban 逻辑。 |

### 运行时

| 组件 | 路径 | 说明 |
|------|------|------|
| Docker Runtime | `pkg/runtime/` | Docker SDK 封装。创建/停止容器，管理卷和网络，平台标签发现。 |
| m800 加固 | `deploy/dedicated-judge/` | Docker daemon 的 `containers.conf`：`no_new_privileges=true`，CapDrop ALL，seccomp filter。 |

### 镜像

| 镜像 | 路径 | 说明 |
|------|------|------|
| 学生容器 | `deploy/container/` | Debian 12 + sshd + gcc/python3/git/vim/tmux。`/home/student` 持久化 Docker Volume。 |
| Controller | `deploy/controller/Dockerfile` | Go 二进制，非 root，只读根文件系统。 |
| Bastion | `deploy/bastion/Dockerfile` | Alpine + sshd，CA 证书认证，无 shell。 |

### SSH CA

| 组件 | 路径 | 说明 |
|------|------|------|
| CA 签发 | `pkg/sshca/` | 生成/加载 CA 密钥对，签发短期（8h）SSH 用户证书，force-command=/bin/false，permit-port-forwarding。 |

### 仓库结构

```
cmd/
  hpc101/            学生 CLI
  controller/        平台 Controller
  bastion-mgmt/      Bastion 管理服务
pkg/
  controller/        Handler + Store + 租约/空闲触发器
  lease/             租约状态机 (Active→Closing→Draining→Stopped→Reclaimed)
  runtime/           Docker 客户端
  adapter/           CSOJ 评测适配器
  sshca/             SSH CA 签发
deploy/
  bastion/           Bastion 部署 (Deployment + sshd_config + principals 脚本)
  container/         学生容器镜像 (Debian 12 + sshd + 开发工具)
  controller/        Controller 部署 + RBAC
  csoj/              CSOJ 配置 overlay
  dedicated-judge/   m800 Docker daemon 加固配置
  rbac/              hpcgame-judger 最小权限 ClusterRole
docs/
  TUTORIAL.md        学生使用手册
  CSOJ-DOCKER-CONTRACT.md  CSOJ Docker API 契约
vendor/              CSOJ git subtree (zero-change)
reference/           只读参考 (gitignored)
```

## API

Controller 通过 Envoy Gateway 暴露在 `https://clusters.zju.edu.cn/hpc101`：

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/healthz` | 健康检查 |
| POST | `/api/v1/keys` | 注册 SSH 公钥 |
| POST | `/api/v1/services` | 创建开发容器 |
| GET | `/api/v1/leases?principal=` | 查询活跃租约 |
| GET | `/api/v1/ssh-info?principal=` | 获取 SSH 连接配置（双网络：校园网/公网） |
| DELETE | `/api/v1/release?principal=` | 释放容器 |
| POST | `/api/v1/submissions` | 提交评测（base64 文件） |
| GET | `/api/v1/scores` | 查看分数 |
| GET | `/api/v1/submissions/logs/:id` | 查看评测日志 |
| POST | `/api/v1/problems/sync` | 管理员同步题目到 CSOJ |

## 开发

```bash
# 构建
go build ./cmd/hpc101/ ./cmd/controller/ ./cmd/bastion-mgmt/

# 测试
go test ./pkg/controller/ ./pkg/lease/ ./pkg/adapter/

# 构建学生容器镜像
docker build -f deploy/container/Dockerfile -t hpc101-platform/container:latest .
```

## 当前状态

| 功能 | 状态 | 备注 |
|------|------|------|
| `hpc101 up` 创建容器 | ✅ | 通过公网 HTTPS，已在生产验证 |
| `hpc101 register-key` | ✅ | 注册后自动签发 8h SSH 证书 |
| `hpc101 ssh-info` | ✅ | 同时输出校园网(22)和公网(443)配置 |
| `hpc101 release` | ✅ | 三层回收：停止容器 → 删除卷 → 清理防火墙 |
| `hpc101 submit/score/logs` | ⚠️ | 需 CSOJ 在后端正常运行 |
| SSH 进容器 | ⚠️ | 需 OpenNG 添加 `bastion` 路由 |
| 容器持久化 | ✅ | `/home/student` Docker Volume，释放前数据不丢 |
| 自动释放 | ✅ | 最大 8h + 空闲 30min + 手动 |
| 容器安全加固 | ✅ | m800 `containers.conf`：no_new_privileges, CapDrop ALL, seccomp |
