# HPC101 Platform 使用手册

## 概述

HPC101 是课程容器化平台。你通过一个 `codojo` 命令行工具完成：

- 申请一个交互式开发容器（带 sshd）
- 通过 SSH（bastion 跳板）连接进入容器
- 提交代码到 CSOJ 评测系统
- 查看评测结果与日志
- 释放容器资源

整个流程**不需要 kubeconfig，也不需要 OIDC 登录**，只用 SSH 密钥 + 平台签发的短期证书。

```
你的笔记本
   │
   │ 1) codojo up        （HTTPS 申请容器，平台签发 8h SSH 证书）
   │ 2) ssh hpc101-container  （经 bastion 跳板进入容器）
   ▼
SSH Bastion  ──ProxyJump──►  你的开发容器（sshd, svc- 前缀）
   ▲                              │
   │                              │ 3) 在容器里写代码
平台 Controller                  │
   │                             │ 4) codojo submit（退到笔记本或留在容器里都行）
   ▼                             ▼
CSOJ Adapter ──► CSOJ（评测）
```

## 1. 前提条件

| 项目 | 要求 |
|------|------|
| 网络 | 能访问 `clusters.zju.edu.cn`（校内网或 VPN） |
| SSH | 已有一对 SSH 密钥（`id_ed25519` 或 `id_rsa`） |
| 系统 | macOS（Intel / Apple Silicon）、Linux x86_64、Windows x86_64 |
| 工具 | `codojo` CLI（见下节安装） |

> 如果你还没有 SSH 密钥，先生成：
> ```bash
> ssh-keygen -t ed25519 -C "your_email@example.com"
> # 一路回车，默认保存到 ~/.ssh/id_ed25519
> ```

## 2. 安装 codojo CLI

从平台获取对应你系统的二进制文件，重命名为 `codojo` 并放入 `PATH`。

**macOS / Linux：**

```bash
mv codojo-darwin-arm64 /usr/local/bin/codojo    # 改成你对应平台的文件名
chmod +x /usr/local/bin/codojo
codojo    # 应输出命令列表
```

**Windows (PowerShell)：**

```powershell
Move-Item .\codojo-windows-amd64.exe C:\Users\<你的用户名>\codojo.exe
.\codojo.exe
```

## 3. 配置环境变量（一次性）

`codojo` 需要知道平台 Controller 的公网地址。**必须设置**，否则会用集群内部地址（仅 Pod 内可达）。

```bash
# macOS / Linux：追加到 ~/.bashrc 或 ~/.zshrc
export CODOJO_CONTROLLER_URL="https://clusters.zju.edu.cn/hpc101"
```

```powershell
# Windows PowerShell
$env:CODOJO_CONTROLLER_URL = "https://clusters.zju.edu.cn/hpc101"
# 或写入用户环境变量（永久）：
[Environment]::SetEnvironmentVariable("CODOJO_CONTROLLER_URL", "https://clusters.zju.edu.cn/hpc101", "User")
```

验证连通性：

```bash
curl -sk https://clusters.zju.edu.cn/hpc101/healthz
# 应返回：ok
```

## 4. 注册 SSH 公钥（首次使用）

平台需要你的 SSH 公钥来配置容器的免密登录，并用它签发短期证书。

```bash
codojo register-key ~/.ssh/id_ed25519
```

输出：

```
key registered with controller (identity: /home/ckyasb/.ssh/id_ed25519)
```

这个命令会：
1. 读取你的公钥（`.pub` 文件）
2. 注册到平台 Controller
3. 在 `~/.hpc101/config.json` 保存本地配置

> **注意**：注册的是公钥（`.pub`），私钥永远不要离开你的笔记本。

## 5. 启动开发环境

```bash
codojo up <镜像名> [课程名] [题目名]

# 示例：启动带 sshd 的标准开发环境
codojo up hpc101-platform/container:latest cs101 lab1
```

成功后会返回：

```
ready: dedicated-judge-runtime.hpc101-runtime.svc.cluster.local:32772
cert saved: /home/ckyasb/.hpc101/ckyasb-key-cert.pub
```

**说明：**

- `ready: host:port` 是你的容器 SSH 端点
- `cert saved: ...` 是平台用 CA 签发的**短期 SSH 证书**（默认 8 小时有效），自动保存到 `~/.hpc101/<你的用户名>-key-cert.pub`
- 容器有**最大存活时间**（默认 8 小时）和**空闲超时**（默认 30 分钟无 SSH 连接），超时自动释放

## 6. 配置 SSH 并连接

### 6.1 查看连接信息

```bash
codojo ssh-info
```

输出（已自动填入你的私钥和证书路径）：

```
Host hpc101-bastion
  HostName bastion.hpc101-platform.svc.cluster.local
  Port 2222
  User bastion
  IdentityFile /home/ckyasb/.ssh/id_ed25519
  CertificateFile ~/.hpc101/ckyasb-key-cert.pub
  IdentitiesOnly yes
  ForwardAgent no

Host hpc101-container
  HostName dedicated-judge-runtime.hpc101-runtime.svc.cluster.local
  Port 32772
  User student
  ProxyJump hpc101-bastion
```

### 6.2 写入 ~/.ssh/config

把上面的输出追加到你的 SSH 配置文件：

```bash
codojo ssh-info >> ~/.ssh/config
```

> **Windows**：SSH 配置文件在 `C:\Users\<你的用户名>\.ssh\config`，手动把输出粘贴进去。

### 6.3 连接容器

```bash
ssh hpc101-container
```

`ProxyJump hpc101-bastion` 会自动经过 bastion 转发到你的容器，**无需手动两跳**。

### 6.4 VSCode Remote-SSH（推荐开发方式）

1. 安装 VSCode 扩展 "Remote - SSH"
2. 确认 `~/.ssh/config` 已包含上面的配置
3. 在 VSCode 中：`Cmd/Ctrl+Shift+P` → "Remote-SSH: Connect to Host..." → 选择 `hpc101-container`
4. VSCode 会通过 bastion 连接到容器，可以在容器里直接编辑、编译、调试

### 6.5 文件传输

由于配置了 SFTP 子系统，可以直接用 `scp` / `sftp`：

```bash
# 上传文件到容器
scp solve.py hpc101-container:~/

# 下载文件
scp hpc101-container:~/output.log ./
```

## 7. 提交代码评测

在容器里写好代码后，退出容器（或另开终端），提交评测：

```bash
# 语法：codojo submit <课程> <竞赛> <题目ID> <文件...>
codojo submit cs101 contest1 hello solve.py
```

返回：

```
submitted: {"submission_id":"abc-123-def","status":"submitted"}
```

文件会被 base64 编码后提交到 CSOJ 评测系统。记下 `submission_id`。

> **前提**：课程和题目必须已由管理员同步到 CSOJ。如果提交报 "problem not mapped"，联系管理员。

## 8. 查看评测结果

```bash
# 列出所有已完成的评测
codojo score

# 查看特定提交的结果（会轮询直到完成）
codojo score <submission-id>
```

输出示例：

```
status: Success
score: 100
performance: 95
info: {"test_cases": [...]}
```

状态可能是：`Queued`（排队中）、`Running`（评测中）、`Success`（成功）、`Failed`（失败）。

## 9. 查看评测日志

```bash
codojo logs <submission-id>
```

会流式输出评测容器的日志（stdout + stderr），用于调试。

## 10. 列出可用题目

```bash
codojo problem
```

输出已同步到平台的题目列表。

## 11. 释放资源

用完后**务必释放**，否则会占用资源直到超时自动释放：

```bash
codojo release
```

输出：

```
released: Reclaimed
```

释放流程会：
1. 关闭 SSH 连接
2. 停止容器
3. 清理存储卷和网络

## 完整工作流示例

```bash
# ===== 一次性设置 =====

# 1. 配置环境变量（追加到 ~/.bashrc 或 ~/.zshrc）
export CODOJO_CONTROLLER_URL="https://clusters.zju.edu.cn/hpc101"

# 2. 生成 SSH 密钥（如果没有）
ssh-keygen -t ed25519 -C "you@example.com"

# 3. 注册公钥
codojo register-key ~/.ssh/id_ed25519

# ===== 日常使用 =====

# 4. 启动开发环境
codojo up hpc101-platform/container:latest cs101 lab1
# → ready: dedicated-judge-runtime.hpc101-runtime.svc.cluster.local:32772

# 5. 配置 SSH
codojo ssh-info >> ~/.ssh/config

# 6. 连接容器
ssh hpc101-container

# 7. 在容器里写代码
vim solve.py
python3 solve.py        # 本地测试

# 8. 退出容器，提交评测
exit
codojo submit cs101 contest1 hello solve.py
# → submission_id: abc-123-def

# 9. 查看结果
codojo score abc-123-def
# → Success, score=100

# 10. 查看日志
codojo logs abc-123-def

# 11. 结束工作，释放环境
codojo release
```

## 命令速查表

| 命令 | 说明 | 示例 |
|------|------|------|
| `register-key <path>` | 注册 SSH 公钥（首次） | `codojo register-key ~/.ssh/id_ed25519` |
| `up <image> [course] [problem]` | 启动开发容器 | `codojo up hpc101-platform/container:latest cs101 lab1` |
| `ssh-info` | 查看容器 SSH 连接信息 | `codojo ssh-info` |
| `release` | 释放容器 | `codojo release` |
| `submit <course> <contest> <pid> <files...>` | 提交评测 | `codojo submit cs101 contest1 hello solve.py` |
| `score [submission-id]` | 查看分数 | `codojo score abc-123` |
| `logs <submission-id>` | 查看评测日志 | `codojo logs abc-123` |
| `problem` | 列出可用题目 | `codojo problem` |

## 环境变量参考

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `CODOJO_CONTROLLER_URL` | `http://controller.hpc101-platform.svc.cluster.local:8080`（集群内部） | Controller API 地址，**从笔记本使用必须设置为公网地址** |
| `USER` | 当前系统用户名 | 学生身份标识（principal） |

## 资源超时说明

| 超时项 | 默认值 | 触发动作 |
|--------|--------|----------|
| 最大存活 | 8 小时 | 容器自动释放 |
| 空闲超时 | 30 分钟无 SSH 连接 | 容器自动释放 |

> 容器释放后，数据会被清理。**重要数据请及时提交到 Git 或下载到本地。**

## 常见问题

### Q1: `codojo up` 提示 "lease conflict"？

你已有一个活跃的开发环境。每个学生同时只能拥有一个容器。

```bash
codojo release    # 先释放旧的
codojo up ...     # 再申请新的
```

### Q2: SSH 连接被拒绝？

1. 确认已执行 `codojo register-key` 注册公钥
2. 确认容器状态为 Active（`codojo ssh-info` 能正常返回）
3. 确认 `~/.ssh/config` 已包含 `codojo ssh-info` 的输出
4. 确认使用的是注册时对应的私钥（`IdentityFile` 路径正确）
5. 证书可能已过期（8 小时），重新 `codojo up` 即可获取新证书

### Q3: SSH 提示 "Permission denied (publickey)"？

- 检查 `~/.ssh/config` 里的 `IdentityFile` 指向的是你的**私钥**（不是公钥）
- 检查 `CertificateFile` 指向 `~/.hpc101/<你的用户名>-key-cert.pub` 且文件存在
- 私钥权限应为 `600`：`chmod 600 ~/.ssh/id_ed25519`

### Q4: 评测提交失败？

- 确认课程和题目已同步到 CSOJ（`codojo problem` 能看到题目）
- 确认文件格式正确（文本文件，非二进制）
- 查看 `codojo logs <submission-id>` 了解具体错误
- 提交被自动禁止（auto-ban）通常是因为文件名包含非法字符，请使用纯英文文件名

### Q5: 证书过期了怎么办？

SSH 证书默认 8 小时有效。过期后重新执行 `codojo up` 即可获取新证书（如果容器还在，会返回已有容器信息并重新签发证书）。

### Q6: 如何查看我当前的容器？

```bash
codojo ssh-info
# 如果有活跃容器，会输出连接信息
# 如果没有，会提示 "no active environment"
```

### Q7: `clusters.zju.edu.cn` 解析不了？

- 确认在校内网或已连接 VPN
- 检查 DNS：`nslookup clusters.zju.edu.cn`
- 如果在校外且无法 VPN，可使用 SSH 隧道：
  ```bash
  ssh -L 8080:controller.hpc101-platform.svc.cluster.local:8080 root@172.25.4.11 -N
  export CODOJO_CONTROLLER_URL="http://localhost:8080"
  ```

### Q8: Windows 下 `codojo ssh-info >> ~/.ssh/config` 报错？

Windows 没有 `~` 简写。手动操作：

```powershell
codojo ssh-info > $env:USERPROFILE\.ssh\config.tmp
Get-Content $env:USERPROFILE\.ssh\config.tmp | Add-Content $env:USERPROFILE\.ssh\config
Remove-Item $env:USERPROFILE\.ssh\config.tmp
```

或手动把 `codojo ssh-info` 的输出粘贴到 `C:\Users\<你的用户名>\.ssh\config` 末尾。

## 故障排查

### 查看详细错误

`codojo` 的错误信息通常已经足够明确。如果遇到不明确的错误，可以用 `curl` 直接测试 Controller：

```bash
# 测试连通性
curl -sk https://clusters.zju.edu.cn/hpc101/healthz
# 应返回：ok

# 查看当前是否有活跃容器
curl -s "https://clusters.zju.edu.cn/hpc101/api/v1/leases?principal=$(whoami)"
# 返回容器信息或 "no active lease"
```

### 常见错误码

| HTTP 码 | 含义 | 解决方法 |
|---------|------|----------|
| 400 | 请求参数错误 | 检查命令参数 |
| 404 | 无活跃容器 | 先执行 `codojo up` |
| 409 | 容器冲突 | 先 `codojo release` |
| 500 | 服务器错误 | 联系管理员，附上时间点 |
| 503 | 服务不可用 | Controller 或 Runtime 未就绪，稍后重试 |

## 联系支持

如遇本手册未覆盖的问题，请联系课程助教或平台管理员，并提供：

1. 执行的完整命令
2. 错误输出（完整）
3. `codojo ssh-info` 的输出（如能执行）
4. 时间点
