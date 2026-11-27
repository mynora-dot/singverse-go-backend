# Docker 部署

本仓库交付内嵌前端和两端 APK 的 Go 业务镜像。所有命令从本仓库根执行，详见 [README](../../README.md)。

## 本地源码部署

需要 Docker Desktop（或支持 BuildKit 命名上下文的 Docker Engine 和 Compose）。在本仓库根执行：

```sh
cp .env.local.example .env.local
# 必填：SINGVERSE_FRONTEND_PROJECT 和 SINGVERSE_COMPUTE_PROJECT 的绝对路径。
make docker-local-up
make docker-local-status
make docker-local-logs
# 停止服务，保留本地数据：
make docker-local-down
```

`docker-local-up` 从所选前端、Go 和计算节点源码构建两个本地镜像，等待服务健康后返回。前端的 admin、party、downloads 三个入口由 Go 同源托管，计算节点通过内部网络连接 Go，认证文件只在挂载目录共享。目录初始化服务以 root 创建并设置运行目录权限，业务服务均以 UID/GID 10001 运行。`COMPUTE_RUNTIME` 默认 cuda，部署前按硬件选择 cuda、rocm、openvino（AMD64）或 rknn（ARM64）；均内置 CPU 降级环境，模型由管理员按需安装。Apple Silicon Mac 使用 compute Release 的原生安装包。

- 管理入口：`http://127.0.0.1:18080/admin/`，初始账号 `admin/admin`。
- 亲友入口：`http://127.0.0.1:18080/party/`。
- 计算节点健康检查：`http://127.0.0.1:18090/health`。
- Docker Desktop 项目：`singverse-go-app`；服务：`singverse-go-backend`、`singverse-compute`，以及退出码为 0 的一次性 `singverse-local-init`。

运行数据位于本仓库的 `data/docker/`：`backend/` 为数据库和业务衍生文件，`secrets/` 为主密钥，`compute-auth/` 为计算节点认证，`compute-cache/` 为计算缓存和硬件设置，`models/` 为模型，`media/` 为只读挂载的媒体原件，`downloads/` 为下载文件。`data/` 与 `.env.local` 均被 Git 忽略，Docker 构建也排除这些文件。备份时一起备份数据库、主密钥和节点认证文件。

本地部署使用独立的 `local.Dockerfile`。尚未准备安装包时，下载页面显示不可用。需要手机和电视下载时，显式选择两个 Android 仓库已生成的安装包目录，每个目录均须包含 APK、`release.json` 和 `SHA256SUMS`：

```sh
make docker-local-releases \
  MOBILE_RELEASE_DIR=/absolute/path/to/mobile/build/mobile-release \
  TV_RELEASE_DIR=/absolute/path/to/tv/app/build/tv-release
# 在 .env.local 中将 SINGVERSE_HTTP_BIND 改为本机当前的局域网 IP。
make docker-local-up
# 在同一局域网的手机或电视中打开 http://<该局域网IP>:18080/downloads/
```

准备命令需要 Android SDK 的 `apksigner`、`aapt`，可通过环境变量 `APKSIGNER`、`AAPT` 指定。它复用正式构建的签名、包名、版本、最低系统、ABI 和摘要校验；任一包无效就失败，不改动现有镜像输入。校验后的副本写入 Git 忽略的 `.build/local-releases/`，清单明确注明“本地联调安装包”，再随本地镜像打包。原始 APK、签名与版本保持不变；临时签名不保证能覆盖设备上已有版本，正式分发仍须使用长期签名和下方的发行包构建流程。

业务端口默认只监听 `127.0.0.1`；手机和电视不能访问该回环地址。局域网使用时绑定实际局域网 IP，并用同一 IP 打开下载页，二维码才会指向设备可达的入口。计算节点仍保持回环监听。机器切换网络后须同步更新 `.env.local` 的绑定 IP 并重建容器。

### 手机和 TV 的局域网发现

两端都查找 `_singverse._tcp` mDNS 服务。同一局域网、HTTP 可访问并不表示已经发布发现记录；Docker 端口映射只提供 HTTP 入口，不会代替宿主机发布 mDNS。不要把宿主机 IP 填入桥接容器的 `SINGVERSE_TV_DISCOVERY_ADDRESS`：该地址不属于容器网卡。

macOS Docker Desktop 部署完成后，在宿主机另开终端运行：

```sh
make lan-discovery
# 自定义容器名：make lan-discovery DISCOVERY_CONTAINER=<Go 容器名>
```

命令读取运行中 Go 容器实际发布的私有 IPv4 地址和 HTTP 端口，检查 `/api/v1/server-info` 后，使用系统 `dns-sd` 发布准确的地址；不会发布容器内部 IP、回环或通配绑定。手机与 TV 重新查找即可。命令需要保持运行，Ctrl-C 撤销广播；Mac 重启后重新运行。服务不可用时撤销广播，恢复后重新发布。不安装开机服务，不修改容器或数据。

Linux NAS 的桥接部署在宿主机用 Avahi 发布同一类型与实际入口端口，例如 `avahi-publish-service "SingVerse NAS" _singverse._tcp 18080 api=1 scheme=http`；保持进程运行，或使用宿主机已有的 Avahi 服务配置管理。原生 Go 部署可显式设置 `SINGVERSE_TV_DISCOVERY_ADDRESS=<本机内网 IP>` 和 `SINGVERSE_TV_DISCOVERY_PORT=<实际 HTTP 入口端口>`。发现记录只提供连接候选，客户端仍会验证服务身份并要求登录。

本地覆盖保持项目和服务名，现有正式命名卷仍被保留；切换部署方式前确认要使用的数据位置。

镜像仓库直连不可用时，可在 `.env.local` 设置 `LOCAL_NODE_IMAGE`、`LOCAL_GO_IMAGE`、`LOCAL_NGINX_IMAGE` 和 `LOCAL_PYTHON_IMAGE` 指向可用镜像地址，保留示例中的固定 SHA-256 摘要；可用 `LOCAL_GOPROXY` 指定 Go 模块代理。所有机器相关配置只保存在未跟踪的 `.env.local`。

## Mac 原生计算端

前后端继续使用 Docker，Mac 上的计算节点由所选 compute 仓库的 `scripts/macos-compute.py` 注册到用户 launchd，使用原生 ARM64 FFmpeg 和 VideoToolbox。先在该 compute 仓库准备 `.venv` 与固定媒体工具（`make compute-setup PYTHON=python3.12 transcode-tools`），再在本仓库的 `.env.local` 中填写 `SINGVERSE_MACOS_CALLBACK_ORIGIN`，例如 `http://192.168.1.102:18080`；地址必须能从 Mac 直接访问当前发布的 Go 端口。

```sh
make docker-macos-up
```

该命令重新构建前后端镜像，停止原 Docker 计算容器，复用已有节点认证文件并重启 Mac 原生节点，再重新部署 Go。Go 通过 `host.docker.internal:18090` 访问节点；媒体任务回源使用所填的 Mac 可达地址。Mac 节点仅监听 `127.0.0.1:18090`，登录后由 launchd 自动启动。原 Docker 计算容器和挂载数据保留。

原生节点的数据、日志、认证和启动配置位于所选 compute 仓库的 `data/macos-compute/`，由该仓库的 `/data` 规则忽略。可在 compute 仓库执行 `.venv/bin/python scripts/macos-compute.py status` 检查状态。后续使用 `make docker-macos-up` 重新部署此组合；`make docker-local-up` 使用 Docker 计算端，两种计算端占用相同端口，切换前先停止当前节点。

## 正式发行包部署

N100 Linux NAS 使用 Docker Hub 预构建镜像时，可直接复制 [N100 独立部署目录](n100/README.md)，其中包含双容器 Compose、OpenVINO/QSV 设备映射、8GB/16GB 资源建议和可选本地曲库挂载。

NVIDIA RTX A3000 Linux 主机可直接使用 [A3000 单文件部署](a3000/README.md)：管理端与 CUDA 计算端同机运行，全部运行参数写在一个 `docker-compose.yaml` 中，无需 `.env`。

```sh
cp .env.example .env
# 按部署需要修改占位配置；已有 .env 不覆盖。
make docker-build docker-up
# 或使用已发布镜像：
make docker-pull docker-image-up
make docker-down
```

源码路线追加 compose.build.yaml，仅构建本仓库 Go 镜像，构建前会准备并校验客户端发行包。预构建镜像路线不包含 build。compute 由独立仓库构建；本仓库保留可选计算节点及硬件覆盖的组合部署配置，默认只启动 Go。

项目名 singverse-go-app、服务和命名卷保持原身份。默认业务端口仅绑定回环；手机使用时由用户按受限局域网设置可达地址。媒体原件只读，不为下载开放管理权限。

业务镜像版本由 SINGVERSE_VERSION 选择，计算节点独立使用 COMPUTE_VERSION。可选节点不加入主服务健康依赖。SQLite 数据与认证密钥卷须一起备份，禁止任意删除命名卷。

### 选择镜像仓库和项目空间

正式镜像由 GitHub Actions 同步推送到 GHCR、Docker Hub。发布者先配置 Actions Variables 与 Secrets，再发布指向新源码的正式 GitHub Release；版本直接来自标签，普通 push/PR 不触发。完整参数和凭据见 [PUBLISHING](PUBLISHING.md)。

部署时把 `.env` 的 `IMAGE_PREFIX` 改为这次实际发布的前缀，只选择其中一个：

| 镜像来源 | `IMAGE_PREFIX` |
| --- | --- |
| GitHub GHCR | `ghcr.io/<ghcr_namespace>` |
| Docker Hub | `docker.io/<dockerhub_namespace>` |

前缀不含镜像名、tag 或末尾斜杠。主服务最终地址为 `${IMAGE_PREFIX}/singverse:${SINGVERSE_VERSION}`，可选计算节点为 `${IMAGE_PREFIX}/singverse-compute:${COMPUTE_VERSION}-${COMPUTE_RUNTIME}`。同机部署两类镜像时，发布者应将它们放入对应平台的同一空间；独立节点可单独配置前缀。

生产部署填写已发布的固定数字版本，例如 `SINGVERSE_VERSION=0.1.0`、`COMPUTE_VERSION=0.1.0`，两者按各自实际版本选择；GitHub Release tag 的 `v` 不写入镜像版本。`COMPUTE_RUNTIME` 只选择 cuda、rocm、openvino 或 rknn，并按[硬件指南](GPU.md)追加设备覆盖。示例版本不表示远端已有可拉取产物。

私有仓库先在部署主机执行 `docker login <仓库域名>`，再运行 `make docker-pull docker-image-up`。切换镜像仓库只修改前缀，保持 `singverse-go-app` 项目名及现有卷；先核对相同固定版本的 digest，详见 [运维指南](OPERATIONS.md)。

硬件覆盖仍需真实设备验证。发布配置见 [PUBLISHING](PUBLISHING.md)，历史迁移注意事项见 [OPERATIONS](OPERATIONS.md)。

RK3566 ARM64 板可使用 [RK3566 测试部署](rk3566/README.md)，使用独立测试项目和 `18081` 端口；提供 RKNN 音频映射及可选 MPP 视频测试，设备路径须按板端驱动核对。

AMD Radeon 780M 核显可使用 [780M 单文件部署](amd780m/README.md)，直接填写完整环境变量并使用相对目录绑定；视频为 VAAPI，音频使用 ROCm 镜像并按实际模型后端试跑。
