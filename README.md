# SingVerse Go 管理平台

家庭自托管 KTV 的唯一业务主服务。Go / Gin / sqlx，默认 SQLite，可显式选择 MySQL；Python 计算节点可选。源码直接位于本仓库根目录。

本仓库维护业务 API、数据库迁移、系统契约及组合部署。管理页面、亲友 H5 和客户端下载页来自独立前端仓库的完整 dist，由 Go 嵌入托管。产品版本唯一来源为根 `VERSION`。

同一 NAS 的后台、手机和电视共用一份收藏，由 Go 持久保存。后台入口为「曲库 → 收藏」，提供搜索、筛选、排序、添加及当前页批量取消。管理员/手机与 TV 设备凭证分别访问共享服务；不导入旧客户端收藏，不持久化客户端收藏副本，练唱记录及作品保持本地存储。接口、权限和刷新语义见 [NAS 共享收藏](contracts/favorites.md)。

歌曲分类沿用 34 个默认标签，并内置对应的 34 张音乐主题封面，随 Go 二进制和 Docker 镜像分发，初始化时自动导入，无需联网。旧安装仅为未编辑且没有封面的默认标签补图；已上传的封面保留，初始化后移除的封面不会因重启恢复。详见 [默认词表与内置封面](docs/song-classification.md#默认词表与内置封面)。

## 独立开发

需要固定 Go 工具链、Python 3.9+ 和 dbmate，工具安装在本仓库 `.tools`：

```sh
make go-tools-setup dbmate-setup
make go-backend-test
make go-backend-build
make go-backend-run
```

默认按 `components.json` 下载固定前端 Release。首次拆分尚未发布前端时，可在前端仓库构建后显式传入完整 dist；三个入口均为必需：

```sh
make go-backend-test FRONTEND_DIST=/absolute/path/to/frontend/dist
make go-backend-build FRONTEND_DIST=/absolute/path/to/frontend/dist
```

生产原生运行在仓库根目录，默认 `127.0.0.1:8080`，不自动加载 `.env`。管理入口 `/`、`/admin/`，亲友入口 `/party/`，公开下载页 `/downloads/`。首次账号 `admin/admin`，登录后可改密及配置 TOTP MFA。

## Docker 打包与部署

本地联调前端、Go 与计算节点（不依赖已发布的客户端包）：

```sh
cp .env.local.example .env.local
# 编辑 .env.local，填写前端和 compute 仓库的绝对路径。
make docker-local-up
make docker-local-status
# 停止并保留挂载数据：
make docker-local-down
```

Docker Desktop 中的项目为 `singverse-go-app`。前端在构建时嵌入 `singverse-go-backend`，计算节点为 `singverse-compute`；另有一次性目录初始化服务。管理入口为 <http://127.0.0.1:18080/admin/>，初始账号 `admin/admin`，计算节点健康检查为 <http://127.0.0.1:18090/health>。本地配置和 `data/docker/` 下的数据库、密钥、媒体、模型、缓存与下载目录均已加入 Git 忽略，构建上下文也排除运行数据。详细说明见 [本地 Docker 部署](infra/docker/README.md)。

需要客户端下载时，先执行 `make docker-local-releases MOBILE_RELEASE_DIR=/absolute/mobile-release TV_RELEASE_DIR=/absolute/tv-release`，校验后的本地联调包会在下一次 `docker-local-up` 中打入镜像。手机和电视访问前，将 `.env.local` 的 `SINGVERSE_HTTP_BIND` 设置为本机局域网 IP，再用 `http://<该IP>:18080/downloads/` 打开下载页和二维码。

Mac Docker 的手机 / TV 自动发现还需在宿主机另开终端运行 `make lan-discovery` 并保持运行。它验证容器实际发布的 HTTP 入口后发布 `_singverse._tcp`；Ctrl-C 停止，重启 Mac 后重新运行。Docker 端口映射本身不发布发现记录。Linux NAS 与原生 Go 的配置见[局域网发现](infra/docker/README.md#手机和-tv-的局域网发现)。

首次正式发布按手机、TV、前端、Go 的顺序进行。两个 Android 仓库配置各自长期签名，并发布 APK、release.json、SHA256SUMS；前端发布完整静态包。详见[多仓库协作](docs/multi-repository.md)和[发布配置](infra/docker/PUBLISHING.md)。

```sh
make docker-build
make docker-up
# 使用已发布镜像：
make docker-pull docker-image-up
# 停止并保留命名卷：
make docker-down
```

打包机需要 Android SDK 的 `apksigner` 和 `aapt`（可通过 APKSIGNER/AAPT 指定命令）。`make docker-build`、`make docker-up` 均先解析手机和 TV 最新正式 Release，校验 APK 真实签名、包名、版本、ABI、最低系统和摘要，再生成 `.build/platform/components.lock.json`。镜像内只读保存两个 APK，部署后不请求 GitHub。

正式 Release 重试使用其已保存的组件清单；本地也可显式复用：

```sh
make docker-build COMPONENT_LOCK=/absolute/path/to/components.lock.json
```

前端版本由 `components.json` 固定。公开 Release 可匿名访问；私有仓库通过 `CROSS_REPO_TOKEN`（Actions Secret 为 `CROSS_REPO_READ_TOKEN`）配置只读跨仓权限。凭据只用于构建前取包，不进入 Docker 构建参数或镜像。

业务镜像使用 `SINGVERSE_VERSION`，计算镜像使用独立的 `COMPUTE_VERSION`。默认只启动 Go；compute 仍通过 profile 显式启用。项目名 `singverse-go-app` 及现有命名卷保留，源码路线仅构建本仓库的 Go 镜像。

正式镜像同步发布到 GitHub GHCR、Docker Hub、阿里云 ACR。`platform-release` 支持 Run workflow 参数 `release_tag`、`ghcr_namespace`、`dockerhub_namespace`、`acr_registry`、`acr_namespace`，选择正式版本和本次目标空间；目标参数留空使用仓库 Variables。部署 `.env` 的 `IMAGE_PREFIX` 对应所选平台的域名/命名空间，详细凭据与镜像地址见[发布配置](infra/docker/PUBLISHING.md)，切换步骤见[运维说明](infra/docker/OPERATIONS.md)。

## 客户端分发

管理界面“客户端下载”和登录页链接均可进入 `/downloads/`。只读 API：

- `GET /api/v1/client-releases/mobile`、`GET /api/v1/client-releases/tv`
- `GET/HEAD /api/v1/client-releases/{platform}/apk`，支持 Range
- `GET /api/v1/client-releases/{platform}/qr?origin=...`

二维码编码当前 NAS 地址，使用手机可达的 NAS 域名或局域网 IP。localhost/回环入口会提示更换地址；无需为此开放公网。

原生运行可设置 `SINGVERSE_CLIENT_RELEASE_DIRECTORY`，包含 mobile/tv 子目录。容器默认为 `/opt/singverse/releases`。已有 `SINGVERSE_TV_RELEASE_DIRECTORY` 保留为 TV 显式覆盖；新公共接口要求完整新版 release.json，旧 TV 接口继续兼容原有简版清单。

更新提示和跳过记录由各设备本机管理。只读下载不授予管理、曲库或设备权限。完整契约见 [OpenAPI](contracts/openapi/README.md)。

## 验证

```sh
make web-check db-fresh-check db-sqlite-check
make go-mysql-check
make docker-test docker-config-check docker-context-check
make go-container-check auth-docker-check
```

跨服务真实验证显式指定准备好的 compute 检出目录：

```sh
SINGVERSE_COMPUTE_PROJECT=/absolute/path/to/compute make go-compute-check
```

具体本次结果见[验证记录](docs/verification.md)。本地测试不代表远程 Actions、微信、NAS 或真实 Android 设备已经验收。
