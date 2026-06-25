# Android App 发版手册

每次发布新 APK 前**必须先读完本文档**。本流程的核心目标：**APP 迭代不需要重新构建后端 docker 镜像、不需要重新构建前端 bundle，所有版本元数据走运行期 REST/JSON，秒级生效。**

服务端 / 前端代码改动才需要走 `docker-build-limited.sh build`；纯发新 APK 不要走那条路。

---

## 一、不变事实（必须先理解，再动手）

1. **下载链接是固定的**：对外推广的永久链接是
   ```
   https://images.deepfly.bond/api/app/download/app
   ```
   后端会 302 跳到当前 metadata 里的 `downloadUrl`。请在公告、二维码、客服话术里使用这个链接，**不要直接发带版本号的 APK 直链**。

2. **版本元数据唯一来源**是服务器 `/opt/chatgpt2api/data/app-version.json`。代码里**没有**任何硬编码的 versionCode / versionName / downloadUrl 默认值（"No compatibility layers" 约定）。文件不存在或字段非法 → `/api/app/latest-version` 返回 503。

3. **元数据热重载**：后端 mtime 监听 `app-version.json`，文件变化下一次请求立即生效，无需 `docker compose restart`。

4. **APK 必须用固定 keystore 签名**，存在服务器 `/root/build/keys/folio-debug.keystore`（DN `CN=Android Debug, O=Android, C=US`，SHA-1 `A6:81:DB:E6:03:7A:BB:C6:B6:03:7A:8D:C3:34:94:69:5C:BC:52:1A`）。换 keystore 会让所有老用户被迫卸载重装，禁止操作。

5. **静态托管**：nginx 反代 `images.deepfly.bond/download/*` 到 `/var/www/html/download/`，APK 文件直接 scp 到这个目录即可。

---

## 二、发版前置检查清单

- [ ] `android-image-app/app/build.gradle.kts` 中的 `versionCode` 和 `versionName` 已 bump（versionCode 单调递增整数；versionName 用 SemVer 字符串）
- [ ] 已在本机用固定 gradle + JDK 工具链编过一次（参见 `.kiro/steering/build-and-package.md` 第〇/二节），confirm Kotlin 编译通过
- [ ] release notes 已准备好（中文，分行用 `\n`）
- [ ] 知道当前服务器 admin 用户名/密码（在 `/opt/chatgpt2api/.env` 里）

---

## 三、标准发版流程

### Step 1：在服务器上构建 APK

不在本机编。本机没装 Android SDK；服务器有 mingc/android-build-box docker 镜像 + 固定 keystore。

```bash
# 1.1 把本地代码打包同步到服务器（注意 tar 排除规则，省去 ~500MB 的构建产物）
TAR_EXCLUDE="--exclude='./node_modules' --exclude='./web/node_modules' \
  --exclude='./.git' --exclude='./android-image-app/build' \
  --exclude='./android-image-app/app/build' --exclude='./android-image-app/.gradle' \
  --exclude='./android-image-app/.kotlin' --exclude='./internal/web/dist' \
  --exclude='./data' --exclude='./test' --exclude='./assets'"

# （略：本地 tar + pscp 上传到 /root/build/chatgpt2api/）

# 1.2 在服务器上跑 APK build 容器
ssh root@137.184.129.188 'cat > /root/build/build-apk.sh <<SH
#!/bin/bash
set -e
export PATH=/root/build/gradle-cache/gradle-8.10.2/bin:\$PATH
cd /project
for i in 1 2 3 4; do
  gradle -p android-image-app assembleDebug --no-daemon && exit 0
  echo retry \$i
  sleep 30
done
exit 1
SH
chmod +x /root/build/build-apk.sh'

ssh root@137.184.129.188 'docker rm -f apk-build 2>/dev/null; \
  nohup docker run --rm --name apk-build \
    -v /root/build/chatgpt2api:/project \
    -v /root/build/keys:/keys \
    -v /root/build/gradle-cache:/root/build/gradle-cache \
    -v /root/build/build-apk.sh:/build-apk.sh \
    -w /project mingc/android-build-box:latest /build-apk.sh \
    > /root/build/apk-build.log 2>&1 &'

# 1.3 等 10 分钟左右，看 /root/build/apk-build.log 出现 "BUILD SUCCESSFUL"
# APK 落在 /root/build/chatgpt2api/android-image-app/app/build/outputs/apk/debug/app-debug.apk
```

### Step 2：把 APK 发布到下载目录

新 APK 文件命名规范：`zheye-v{versionName}-debug.apk`

```bash
ssh root@137.184.129.188 \
  'cp /root/build/chatgpt2api/android-image-app/app/build/outputs/apk/debug/app-debug.apk \
      /var/www/html/download/zheye-v{NEW_VERSION_NAME}-debug.apk'
```

### Step 3：用 REST API 更新版本元数据

在本机执行（不需要登录服务器）：

```bash
# 编辑 test/seed-app-version.py 里的 METADATA 字典：
#   versionCode  → 与 build.gradle.kts 中一致
#   versionName  → 与 build.gradle.kts 中一致
#   downloadUrl  → https://images.deepfly.bond/download/zheye-v{NEW_VERSION_NAME}-debug.apk
#   releaseNotes → 本次更新说明，分行用 \n
#   minSupportedVersionCode → 默认 1；仅在强制升级时提到 ≥ 某个 versionCode

CHATGPT2API_ADMIN_PASSWORD='<admin 密码>' python3 test/seed-app-version.py
```

脚本会：
1. 登录 admin → 拿 token
2. `PUT /api/admin/app-version`，校验 + 落盘
3. `GET /api/app/latest-version` 回显验证

### Step 4：人肉烟囱检查

```bash
curl -s https://images.deepfly.bond/api/app/latest-version | python -m json.tool
curl -sI https://images.deepfly.bond/api/app/download/app | grep -i location
```

预期：
- `latest-version` 返回新的 versionCode / versionName / downloadUrl
- `download/app` 返回 `302` + `location: https://images.deepfly.bond/download/zheye-v{NEW_VERSION_NAME}-debug.apk`

---

## 四、什么时候**才**需要重新构建后端 docker 镜像

只有当**服务端 Go 代码、前端 React 代码、deploy/Dockerfile** 真有改动时才需要。**纯发新 APK 永远不要 build 镜像**。

如果你看到自己即将运行 `sh deploy/docker-build-limited.sh build` 但本次仅是 APK 发版——立刻停手，回到第三步走 REST API 路径。

---

## 五、特殊场景

### 强制升级（minSupportedVersionCode）

如果本次发版是协议破坏性变更，旧版本必须淘汰，把 `minSupportedVersionCode` 提到本次 `versionCode`（或某个最低可容忍版本）。APP 端的 update overlay 会把这种情况识别为"强制更新"，无法跳过。

### 回滚

`PUT /api/admin/app-version` 一次写入完整 JSON，**没有版本历史**。如果发版后想回滚到 N-1，重新 PUT N-1 的 metadata 即可（APK 文件还在 `/var/www/html/download/` 里，没删）。

### 元数据文件被外部破坏

服务端 mtime 重载发现 JSON 非法时，**保留**上一份合法缓存（不会归零），handler 给客户端 503 + 错误描述。运维 PUT 一次正确的 JSON 即恢复。

---

## 六、相关代码索引

- 路由注册：`internal/httpapi/router.go`
  - `GET /api/app/latest-version`：公开元数据
  - `GET /api/app/download/app`：固定下载链接（302 跳转）
  - `GET|PUT /api/admin/app-version`：管理员读写元数据（admin 鉴权）
- 实现：`internal/httpapi/app_version.go`（`appVersionStore`、handlers、validation）
- 一次性发版脚本：`test/seed-app-version.py`
- App 端检查更新入口：`android-image-app/.../AppUpdateOverlay.kt`、`ApkInstaller.kt`
