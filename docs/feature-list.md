# ChatGPT2API 功能清单

> 本文档基于代码实际路由（`internal/httpapi/router.go`）、前端页面（`web/src/app/`）与 Android App 源码复核整理。
> 项目基于 [ZyphrZero/chatgpt2api](https://github.com/ZyphrZero/chatgpt2api) 二次开发。

## 一、对外 OpenAI 兼容 API（`/v1/*`）

| 路由 | 方法 | 功能 |
| --- | --- | --- |
| `/v1/models` | GET | 列出可用模型（连通性 + 鉴权探测） |
| `/v1/images/generations` | POST | 文生图（OpenAI Images 兼容） |
| `/v1/images/edits` | POST | 图生图 / 图片编辑（multipart 上传参考图） |
| `/v1/chat/completions` | POST | 对话补全；支持文本、视觉理解、图片生成（含 SSE 流式） |
| `/v1/responses` | POST | Responses API（含图片生成工具） |
| `/v1/messages` | POST | Anthropic Messages 兼容（含工具调用 XML 解析） |

支持模型：`auto`、`gpt-image-2`、`codex-gpt-image-2`、`gemini-3.1-flash-image` 等；Auto 路由按模型解析对外模型与计费桶。

## 二、鉴权与会话（`/auth/*`）

| 路由 | 功能 |
| --- | --- |
| `/auth/login` | 账号密码登录，返回 token + 计费快照 |
| `/auth/register` | 账号注册（受开关控制） |
| `/auth/logout` | 登出 |
| `/auth/session` | 校验会话、返回身份 |
| `/auth/providers` | 可用登录方式 |
| `/auth/linuxdo/start`、`/auth/linuxdo/oauth/callback`、`/auth/linuxdo/callback` | Linux.do Connect OAuth 登录 |

## 三、管理后台 API（`/api/*`）

### 账号与调度
- `/api/accounts`：ChatGPT 账号池 CRUD、刷新、Plus 资格检查
- `/api/openai-accounts`：OpenAI 协议账号池（双桶配额）管理
- `/api/cpa/pools`：CPA 账号池
- `/api/sub2api/servers`：Sub2API 上游服务器

### 用户、角色与权限（RBAC）
- `/api/admin/users`：用户管理（分页/搜索/排序、用量统计、批量计费调整）
- `/api/admin/roles`：角色管理（菜单权限 + API 权限）
- `/api/admin/permissions`：权限目录
- `/api/auth/users`：用户密钥管理
- `/api/profile`、`/api/profile/password`、`/api/profile/api-key`：个人中心、改密、个人 API Key
- `/api/profile/prompt-favorites`：提示词收藏

### 创作任务（异步）
- `/api/creation-tasks/image-generations`：文生图任务
- `/api/creation-tasks/image-edits`：图生图任务
- `/api/creation-tasks/chat-completions`：文本生成任务
- `/api/creation-tasks?ids=`：任务轮询；`/api/creation-tasks/{id}/cancel`：取消

### 图片库
- `/api/images`：列表（按 scope: mine/public/all）、批量删除
- `/api/images/visibility`：可见性（私密/公开）、分享提示词与参考图
- `/api/images/storage-governance`：存储治理（按保留天数/容量清理）
- `/images/*`、`/image-references/*`、`/image-thumbnails/*`：图片/参考图/缩略图文件服务（含鉴权与云存储回源）

### 注册机
- `/api/register`：批量注册任务（启动/停止/重置/SSE 事件流）
- `/api/register/proxy/test`：注册代理测试
- `/api/hlool-mail`：HloolMail 临时邮箱服务（二开新增）
- 支持多临时邮箱供应商：Cloudflare TempMail、TempMail.lol、DuckMail、GPTMail、MoEmail、Inbucket、YYDS、HLOOL

### 云存储
- `/api/admin/cloud/cookies`、`/api/admin/cloud/cookies/check`：云存储 Cookie 管理与活跃检查
- `/api/admin/cloud/status`、`/api/admin/cloud/test-upload`：状态与测试上传
- 支持 S3 兼容（R2/B2/MinIO/AWS）、A1/A4 线路

### 系统与运维
- `/api/admin/system`：系统信息、检查更新
- `/api/announcements`、`/api/admin/announcements`：公告（公开读 / 管理写）
- `/api/settings`、`/api/settings/login-page-image`：全局设置、登录页背景图
- `/api/logs`、`/api/logs/governance`：业务日志查询与治理
- `/api/proxy`、`/api/proxy/test`：全局代理配置与测试
- `/api/storage/info`：存储信息
- `/api/app-meta`、`/api/app/latest-version`：应用元数据、Android 客户端版本检查
- `/health`、`/version`：健康检查与版本

## 四、Web 管理后台页面（`web/src/app/`）

| 页面 | 功能 |
| --- | --- |
| `login` | 登录（账号密码 + Linux.do） |
| `image` | 在线创作台（文生图/图生图/对话） |
| `image-manager` | 图片库管理 |
| `accounts` | 账号池管理（含 OpenAI 账号、数据导入） |
| `register` | 注册机控制台 |
| `users` | 用户管理 |
| `rbac` | 角色与权限管理 |
| `logs` | 日志查看 |
| `profile` | 个人中心 |
| `settings` | 全局设置（代理、云存储、更新、登录页等） |
| `auth` | OAuth 回调处理 |

## 五、Android 生图 App（`android-image-app/`）

- 登录校验（Base URL + auth-key）、双桶计费展示
- 文生图 / 图生图（多参考图、基于上一轮结果继续编辑）
- 对话模式（SSE 流式 + Markdown 渲染）
- 提示词优化（复用 `/v1/chat/completions`）、提示词市场（本地预设）
- 多轮会话本地持久化、历史管理
- 生成参数面板（模型/尺寸/分辨率/质量/格式/数量/可见性）
- 图片全屏预览（缩放拖拽）、保存到相册
- 主题（浅色/暗色/跟随系统）、应用版本检查（可选/强制更新）

技术栈：Kotlin + Jetpack Compose + Material3 + OkHttp + Coil。

## 六、部署与构建

- Docker Compose 一键部署；多阶段 Dockerfile（bun 构建前端 + Go 内嵌）
- 资源受限服务器源码构建脚本 `deploy/docker-build-limited.sh`
- GoReleaser 发布（linux amd64/arm64，Docker Hub + GHCR）
- 存储后端：SQLite（默认）/ PostgreSQL / MySQL
