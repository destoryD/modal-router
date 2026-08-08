# Modals Router

AI API 路由转发网关 — 将 AI API 请求分配到多个后端渠道，支持流式/非流式响应，按状态码自动禁用 Key。

## 架构

```
Client ──HTTP──▶ Modals Router ──▶ Backend A (key1 → url1)
                   (Go)       ──▶ Backend B (key2 → url2)   ──▶ 返回内容
                              ──▶ Backend C (key3 → url3)
```

- **负载均衡**: 平滑加权轮询 (Smooth Weighted Round-Robin)
- **流式支持**: 自动检测 SSE (`text/event-stream`)，逐块 flush 转发
- **自动禁用**: 后端返回指定状态码 (默认 402) 时自动禁用该渠道
- **自动恢复**: 可配置冷却时间后自动重新启用
- **故障重试**: 渠道不可用时自动切换到下一个可用渠道
- **持久化**: JSON 文件存储，原子写入，支持 Docker 卷映射

## 快速开始

### Docker 部署 (推荐)

```bash
docker compose up -d
```

数据文件映射到宿主机 `./data/channels.json`，打开 `http://localhost:8080/admin/` 管理渠道。

### 本地运行

```bash
go build -o modals-router .
./modals-router
```

## 配置 (环境变量)

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `ROUTER_LISTEN` | `:8080` | 监听地址 |
| `ROUTER_DATA_DIR` | `./data` | 数据目录 |
| `ROUTER_MAX_RETRIES` | `3` | 最大重试次数 (切换渠道) |
| `ROUTER_ADMIN_TOKEN` | (空) | Admin API 认证 token，为空则不启用认证 |

## 使用方式

### 1. 添加渠道

在 `http://localhost:8080/admin/` 页面添加渠道：

| 字段 | 说明 | 示例 |
|------|------|------|
| Name | 渠道名称 | OpenAI Primary |
| Base URL | 后端基础 URL | `https://api.openai.com` |
| API Key | 后端 API Key | `sk-xxx` |
| Weight | 权重 (轮询比重) | 1 |
| Auth Header | 认证头名称 | `Authorization` |
| Auth Prefix | 认证前缀 | `Bearer ` |
| Disable on Status | 触发禁用的状态码 | `402` |
| Auto Re-enable (sec) | 冷却后自动恢复 | `3600` |

### 2. 发送请求

将原本发往 AI API 的请求改为发往 Modals Router，路径保持不变：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer dummy" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role":"user","content":"Hello"}],
    "stream": true
  }'
```

Router 会自动选择可用渠道，将 `Authorization` 替换为该渠道的 Key，转发请求并返回响应。

### 不同后端认证方式

| 后端类型 | Auth Header | Auth Prefix |
|----------|-------------|-------------|
| OpenAI / 通用 | `Authorization` | `Bearer ` |
| Anthropic | `x-api-key` | (空) |
| Azure OpenAI | `api-key` | (空) |

可通过 **Custom Headers** 添加额外请求头，例如 Anthropic 的 `anthropic-version`：
```json
{"anthropic-version": "2023-06-01"}
```

## 路由说明

| 路径 | 用途 |
|------|------|
| `/admin/` | 管理界面 |
| `/admin/api/*` | Admin REST API |
| `/*` (其他所有) | 代理转发到后端渠道 |

## Admin API

```
GET    /admin/api/channels          列出所有渠道
POST   /admin/api/channels          创建渠道
GET    /admin/api/channels/{id}     获取单个渠道
PUT    /admin/api/channels/{id}     更新渠道
DELETE /admin/api/channels/{id}     删除渠道
POST   /admin/api/channels/{id}/enable   启用渠道
POST   /admin/api/channels/{id}/disable  禁用渠道
POST   /admin/api/channels/{id}/reset    重置统计
GET    /admin/api/stats             全局统计
GET    /admin/api/health            健康检查
```

## 项目结构

```
modals-router/
├── main.go                     # 入口，路由装配
├── internal/
│   ├── models/models.go        # 数据模型
│   ├── store/store.go          # JSON 文件存储
│   ├── balancer/balancer.go    # 加权轮询负载均衡
│   ├── proxy/proxy.go          # HTTP 代理 (流式/重试/禁用)
│   └── api/api.go              # Admin REST API
├── web/                        # 前端 (embed 嵌入二进制)
│   ├── index.html
│   ├── app.js
│   └── style.css
├── Dockerfile
├── docker-compose.yml
└── data/                       # 持久化数据 (Docker 卷)
    └── channels.json
```

## Docker 卷映射

`docker-compose.yml` 将宿主机 `./data` 映射到容器 `/app/data`：

```yaml
volumes:
  - ./data:/app/data
```

渠道配置和统计数据保存在 `./data/channels.json`，容器重建后不丢失。
