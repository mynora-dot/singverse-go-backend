# N100 NAS：Docker Hub 镜像部署

本目录可以单独复制到 Linux N100 NAS，不需要下载源码或在 NAS 编译。使用支持 `docker compose up --wait` 的 Compose 插件。

| 服务 | 用途 | NAS 端口 |
| --- | --- | --- |
| `singverse-go-backend` | Go、内嵌管理端/亲友点歌页/APK 下载页、SQLite | NAS 局域网 IP 的 `18080` |
| `singverse-compute` | OpenVINO 运行库、QSV 视频转码、媒体探测和音频处理 | 不映射；业务容器访问 `8090` |

两服务使用共享认证卷自动连接；计算回调使用 `http://singverse-go-backend:8080`。主服务没有计算节点健康依赖，节点故障时业务页面仍可启动。镜像自带健康检查。模型权重在后台按需下载或导入。

## 1. 检查 NAS 核显

通过 NAS 的 SSH 终端执行：

```sh
docker compose version
ls -l /dev/dri/
stat -c '%g' /dev/dri/renderD128
```

记下最后一条命令输出的**数字 GID**，不固定假设为 `render` 或 `video` 的某个编号。`/dev/dri/renderD128` 必须存在；若不存在，先在 NAS 系统检查核显启用状态和驱动。容器镜像不安装宿主机驱动。多显卡机器先确认 Intel 核显对应的 render 节点，同时修改设备路径和 GID。

配置通过 `devices` 映射 render 节点，通过 `group_add` 授予镜像内 UID/GID `10001:10001` 访问权，无需 `privileged` 或全局修改设备权限。OpenVINO GPU 和 QSV 视频编码仍需分别验证。

## 2. 选择镜像、地址和资源

把本目录复制到 NAS 的持久配置目录，进入该目录，创建配置：

```sh
cp -n .env.example .env
```

编辑 `.env`，填写前五个必填配置：

```dotenv
IMAGE_PREFIX=docker.io/your_dockerhub_namespace
SINGVERSE_VERSION=0.1.0
COMPUTE_VERSION=0.2.0
SINGVERSE_HTTP_BIND=192.168.1.100
COMPUTE_RENDER_GID=实际数字GID
```

这里的空间、两个版本和 IP 都是示例，必须替换。两个版本可以不同，分别对应已发布的 `singverse:<版本>` 和 `singverse-compute:<版本>-openvino`；不要填 `v`、`-amd64` 或把 `-openvino` 再写入版本变量。私有仓库先执行 `docker login docker.io`。没有 `singverse-compute` 的 OpenVINO 镜像时，先发布该变体再启动完整组合。

默认资源是建议起点，按 NAS 其他容器的占用调整：

| 配置 | 8GB NAS，默认 | 16GB NAS，建议 |
| --- | --- | --- |
| `SINGVERSE_CONTAINER_MEMORY_LIMIT` | `1g` | `2g` |
| `COMPUTE_CONTAINER_MEMORY_LIMIT` | `4g` | `6g` |
| `COMPUTE_AUDIO_MEMORY_BYTES` | `2147483648`，2GiB | `4294967296`，4GiB |
| 业务 / compute CPU 配额 | `1.0` / `2.0` | `1.0` / `2.0` |
| 转码并发 / 音频线程 | `1` / `2` | `1` / `2` |

4GB 机器建议只部署业务服务，把计算放到其他机器；可执行 `docker compose up -d --no-build --wait singverse-go-backend`，再在后台将性能节点改为禁用或配置独立节点。音频模型、歌曲时长和处理档位会影响内存需求，模型失败时先查看任务错误和容器内存，再决定调大预算或使用独立节点。

## 3. 持久化和本地曲库

默认采用 Docker 命名卷，项目名为 `singverse-go-app`，与仓库标准部署保持相同身份：

| 卷名后缀 | 内容 |
| --- | --- |
| `singverse-data` | SQLite、封面/元数据、播放缓存、原唱/伴奏衍生文件 |
| `singverse-secrets` | 业务主密钥 |
| `singverse-compute-auth` | 计算节点连接密钥 |
| `singverse-transcode-cache` | 计算临时缓存、硬件/模型选择与试跑记录 |
| `singverse-audio-models` | 模型权重 |
| `singverse-downloads` | 下载文件 |
| `singverse-media` | 默认的只读媒体挂载占位卷 |

实际名称带 `singverse-go-app_` 前缀。建议 NAS 的 Docker 数据存储位于本机 SSD；SQLite 使用本机文件系统。默认播放缓存预算 20GiB、计算临时缓存预算 5GiB，还需给镜像、模型、下载和其他衍生文件预留空间。计算缓存卷包含设置，不能当作纯临时目录整体丢弃。

使用 WebDAV 曲库可保持默认卷。在后台填写 WebDAV 地址。

使用 NAS 本地曲库时，在 `.env` 填写两个**已存在的绝对路径**，再启用覆盖文件：

```dotenv
SINGVERSE_MEDIA_PATH=/实际NAS路径/原始歌曲
SINGVERSE_DOWNLOAD_PATH=/实际NAS路径/SingVerse下载
```

```sh
cp -n compose.storage.yaml compose.override.yaml
```

之后普通 `docker compose` 命令会自动读取覆盖文件。后台本地曲库路径填写 `/media`，下载路径填写 `/downloads`；计算节点通过 Go 的授权回调读取媒体，不额外挂载原件。两个宿主目录应当分开。

通过 NAS 权限管理授予 UID `10001` 对原件和父目录的读取/遍历权限、对下载目录的写入权限。仅对**新建且专用**的下载目录，可由 NAS 管理员设置所有者 `10001:10001`；不要递归修改原始曲库权限。长格式 bind 挂载会在源目录不存在时失败，避免路径写错后扫描空目录。

如只需覆盖其中一个挂载，编辑 `compose.override.yaml`，保留对应条目即可。已有安装采用绑定目录或其他项目名时，应沿用原有配置与数据位置；不要直接启动本示例，把新空卷当成迁移结果。

## 4. 拉取与启动

在 NAS 部署目录执行。所有命令都在同一目录，保持同一配置和项目名：

```sh
docker compose config --quiet
docker compose pull
docker compose up -d --no-build --wait --wait-timeout 180
docker compose ps
docker compose logs --tail 100
```

浏览器打开 `http://<NAS局域网IP>:18080/admin/`，初始账号 `admin/admin`，首次登录后修改密码。亲友点歌为 `/party/`，APK 下载为 `/downloads/`。若修改了 HTTP 端口，访问地址同步修改。手机、TV 和浏览器都连接同一业务入口。

后台性能节点使用自动连接，确认节点在线，然后分别试跑视频 QSV 和音频 OpenVINO。默认视频后端为 `qsv`；也可将 `.env` 的 `COMPUTE_HARDWARE_BACKEND` 改为 `auto`，再执行 `up -d`，由节点按探测结果选择 QSV/VAAPI。后台已保存的硬件选择会覆盖部署默认值，应在后台同步检查。音频处理先按需安装模型并试跑，再开始批量任务；保留单路转码起步。

## 5. 核显验证与排错

检查容器设备权限、OpenVINO 设备枚举和实际 QSV 编码：

```sh
docker compose exec -T singverse-compute id
docker compose exec -T singverse-compute python -c 'import os; p=os.environ["COMPUTE_HARDWARE_DEVICE"]; print(p, "read/write:", os.access(p, os.R_OK | os.W_OK))'
docker compose exec -T singverse-compute python -c 'import openvino as ov; print(ov.Core().available_devices)'
docker compose exec -T singverse-compute sh -c 'exec /usr/lib/jellyfin-ffmpeg/ffmpeg -hide_banner -loglevel error -qsv_device "$COMPUTE_HARDWARE_DEVICE" -f lavfi -i color=s=1280x720:r=25:d=1 -vf format=nv12 -c:v h264_qsv -frames:v 10 -f null -'
```

权限检查应为 `True`；OpenVINO 枚举应包含 `GPU` 或 `GPU.0` 才表示发现 GPU，只有 `CPU` 时检查驱动与设备权限。最后一条命令退出码为 0 表示合成样片 QSV 编码通过，还需在后台用实际歌曲验证解码、转码和模型任务。`healthy` 只表示 HTTP 健康检查通过。

镜像拉取报 `manifest unknown` 时核对命名空间、数字版本和 `-openvino` 标签；报 `permission denied` 时检查 render GID、目录 ACL 和 NAS 容器管理器是否应用了设备映射。日志中出现内存预算错误或容器被 OOM 终止时，先核对内存限额和任务档位。

桥接网络的 HTTP 端口映射不会替宿主发布 mDNS。手机/TV 可手动输入业务地址；需要自动发现时，在 NAS 宿主配置 Avahi 发布 `_singverse._tcp` 与实际端口。不要把 NAS IP 填入桥接容器的原生发现地址。

## 6. 停止、备份和升级

```sh
# 停止并保留卷
docker compose stop
```

服务完全停止后，使用 NAS/Docker 卷备份功能保存上述持久卷及外部绑定目录，数据库、主密钥、计算认证和设置必须一起保存。单独保存 `.env` 与当前镜像版本；备份完成后 `docker compose up -d --no-build --wait` 恢复运行。

升级前完成停机备份，修改 `.env` 中两个独立版本，再执行：

```sh
docker compose pull
docker compose up -d --no-build --wait --wait-timeout 180
docker compose ps
```

检查登录、曲库、播放和计算任务后完成升级。需要移除容器时使用 `docker compose down` 保留卷，**不要使用 `down -v`**。数据库迁移后如需回退，应恢复与旧镜像匹配的数据库及密钥备份。

本目录提供部署配置；配置解析通过不代表 Docker Hub 产物、NAS 驱动或 N100 核显已经完成远程验收。
