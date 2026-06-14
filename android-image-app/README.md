# chatgpt2api 手机生图 App

这是一个独立的 Kotlin Android App，用来直接对接现有 chatgpt2api 服务端，不需要修改服务端。

## 功能

- 填写服务端 `Base URL` 和 `auth-key` 后登录校验
- 自动读取 `/v1/models` 中的图片模型
- 文生图：调用 `/api/image-tasks/generations`
- 图生图：选择手机图片后调用 `/api/image-tasks/edits`
- 任务轮询：调用 `/api/image-tasks?ids=...`
- 提示词优化：调用现有 `/v1/chat/completions`，优化后回填，再手动生图

## 运行

1. 用 Android Studio 打开 `android-image-app/`
2. 等待 Gradle 同步完成
3. 运行 `app`

服务端地址填写示例：

- 真机访问局域网服务端：`http://192.168.1.2:8000`
- Android 模拟器访问电脑本机服务端：`http://10.0.2.2:8000`
- 已部署服务：`https://your-domain.example`

`auth-key` 填 `config.json` 或环境变量里配置的服务端密钥。

## 注意

- App 为了便于本地调试开启了 `usesCleartextTraffic=true`，可访问 HTTP 服务端；正式发布建议使用 HTTPS。
- 目前是轻量版本，没有引入本地数据库；配置存在 SharedPreferences 中，任务历史只保留在当前进程内。
- 提示词优化没有依赖新接口，而是使用现有文本接口 `/v1/chat/completions`。
