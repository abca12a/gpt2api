# AGENTS.md

## 基本规则

- 始终使用中文简体回复。
- 每次回复都称呼用户为 han。
- 用户明确指令优先于默认规则。
- `AGENTS.md` 只记录长期稳定、跨任务复用的规则和连接拓扑；阶段流水、临时结论、一次性排查记录不要写在这里。

## 项目连接信息

> 2026-04-27 由 han 重新确认。本节是项目机器拓扑的权威来源；不要再沿用 `Documentation.md` 里的旧连接流水、历史私钥路径、授权行号或“前端无法登录”等过期结论。

### 号池（当前 Codex / gpt2api）

```text
号池
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMnghMp2z/tJs5u8TAGwWTFjyJDz11fhaE3aV4bZHjeM codex@212.50.232.214-2026-04-23
43.165.170.99
ubuntu

项目目录
ubuntu@VM-0-7-ubuntu:~/gpt2api$
```

- 职责：当前仓库、线上 `gpt2api`、账号池服务所在机器。
- 排查号池：优先在本机 `/home/ubuntu/gpt2api` 操作；不要把 `212.50.232.214` 当号池。

### 下游后端（new-api）

```text
下游后端
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFLhOyQ5fpwI+zLDQI6niEFy8W4yqWykpXe3onPC6T8b new-api-build@beaming-cluster-1
212.50.232.214
root

项目目录
root@beaming-cluster-1.localdomain:/root/new-api$
```

- 职责：下游 `new-api` 后端源码与运行服务。
- 排查后端：进入 `/root/new-api`，重点看任务、日志、渠道、后端转发和 `new-api-postgres-local.tasks.fail_reason`。
- 构建边界：下游后端中的老前端构建需要去构建机完成，不在后端机器直接构建。

### 下游前端（new-api-web）

```text
下游前端
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKXjFn5F87+s5pVyzDlqYWEac6rmddvheGYbAwAVB912 new-api-web preview deploy
43.161.219.135
ubuntu

项目目录
ubuntu@VM-0-13-ubuntu:~/new-api-web$
```

- 职责：下游前端源码与发布。
- 排查前端：进入 `/home/ubuntu/new-api-web`；历史确认静态发布目录为 `/home/ubuntu/preview.dimilinks.com`。
- 构建边界：下游前端的画布部分构建需要去构建机完成，不在前端机器直接构建。

### 构建机

```text
构建机
43.152.240.30
ubuntu

终端
ubuntu@VM-0-11-ubuntu:~$
```

- 职责：下游后端中的老前端构建、下游前端的画布部分构建。

## 连接判断规则

- han 给出上述机器 IP 并问“能不能访问/进去”时，优先理解为 SSH 登录与项目目录可达性，不要只做 `ping`、`curl` 或网页可达性判断。
- 具体 SSH 端口、私钥路径、agent 状态以当前 SSH 配置或当次验证为准；不要把一次性端口验证写成长期事实。
