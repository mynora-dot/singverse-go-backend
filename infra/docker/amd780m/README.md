# AMD Radeon 780M 单文件部署

适用于 Linux AMD64 主机上的 Radeon 780M 核显。将 `docker-compose.yaml` 单独复制到部署目录即可，Go 镜像内含管理端、亲友点歌和 APK 下载页，计算端使用 `latest-rocm` 镜像；视频选择 VAAPI，音频使用镜像内的 ROCm 或独立 CPU 降级环境。

所有运行参数直接写在 YAML 中，无需 `.env`、shell 变量、源码构建或手填认证 token。镜像使用仓库既有的 `docker.io/mynora/singverse:latest` 和 `docker.io/mynora/singverse-compute:latest-rocm`；如果实际推送了不同标签，修改两处 `image`。业务端与计算端独立选择版本。正式部署可替换为各自已发布的固定版本或 digest，本轮未拉取或确认远端标签摘要。

## 宿主检查与启动

宿主需提供兼容的 `amdgpu`/KFD 驱动、固件以及 `/dev/kfd`、`/dev/dri`。运行库在镜像内，容器不会安装或更新宿主驱动、固件。该配置要求 Linux 原生设备透传，不能用 macOS 或 Windows Docker Desktop 代替真机验收。

```sh
uname -m
ls -l /dev/kfd /dev/dri
cat /sys/class/drm/renderD128/device/vendor
readlink -f /sys/class/drm/renderD128/device/driver
docker compose -f docker-compose.yaml config --quiet
docker compose -f docker-compose.yaml pull
docker compose -f docker-compose.yaml up -d --no-build --wait --wait-timeout 180
docker compose -f docker-compose.yaml ps
```

CPU 架构应为 `x86_64`，默认 render 节点的厂商应为 `0x1002`、驱动应为 `amdgpu`。多显卡主机分别核对 VAAPI 与 ROCm 的选择：将 `COMPUTE_HARDWARE_DEVICE` 改为 780M 对应的 `/dev/dri/renderD*`，并按 ROCm 枚举调整 `HIP_VISIBLE_DEVICES`；两者的编号不是同一套编号。设备不存在时先修复宿主驱动，不创建空目录代替设备。

管理入口：`http://<服务器局域网IP>:18080/admin/`。新数据库账号密码为 `admin/admin`，登录后修改密码。亲友点歌和 APK 下载入口分别为 `/party/`、`/downloads/`。私有镜像需先执行 `docker login docker.io`。

后台性能节点选择“自动连接”，计算端生成并持久化 token，管理端只读同一个目录。计算端不向宿主发布 `8090`，管理端也不依赖其健康状态才能启动。已有数据库若保存了手动连接或禁用状态，需在后台改回自动连接。

## 硬件与模型验证

默认视频后端为 VAAPI，使用 Mesa `radeonsi` 驱动。先检查驱动，再进行短 H.264 硬件编码：

```sh
docker compose -f docker-compose.yaml exec -T singverse-compute /usr/lib/jellyfin-ffmpeg/vainfo --display drm --device /dev/dri/renderD128
docker compose -f docker-compose.yaml exec -T singverse-compute /usr/lib/jellyfin-ffmpeg/ffmpeg -hide_banner -loglevel error -vaapi_device /dev/dri/renderD128 -f lavfi -i color=s=1280x720:r=25:d=1 -vf format=nv12,hwupload -c:v h264_vaapi -frames:v 10 -f null -
```

修改了 YAML 中的 render 路径后，上述命令也使用相同路径。在后台选择“跟随部署”或 VAAPI，进行实际素材试跑；已保存的视频选择会覆盖环境默认值。H.264 合成编码通过只验证该编码路径，不能代替 HEVC/AV1、硬件解码、HDR 色调映射或完整播放验收。

ROCm 检查使用系统 Python；PyTorch 的 HIP 后端仍通过 `torch.cuda` API 暴露设备：

```sh
docker compose -f docker-compose.yaml exec -T singverse-compute python -c 'import torch; from singverse_compute.core.audio_runtime import native_rocm_architectures; print("HIP:", torch.version.hip); assert torch.version.hip and torch.cuda.is_available(); p=torch.cuda.get_device_properties(0); arch=p.gcnArchName.split(":")[0]; print(p.name, arch); assert arch == "gfx1103" and arch in native_rocm_architectures(); x=torch.ones((256,256), device="cuda"); print("GPU result:", (x @ x).mean().item()); torch.cuda.synchronize()'
```

当前 compute Dockerfile 在一套系统 Python 中安装固定 ROCm/Torch 及 `gfx1103` 设备包，本配置因此将 `COMPUTE_AUDIO_ACCELERATOR_PYTHON` 设为 `/usr/local/bin/python`。不使用 CUDA 专用虚拟环境，也不设置 `HSA_OVERRIDE_GFX_VERSION` 伪装 GPU 架构。旧镜像若缺少原生设备包，需选择包含该实现的实际发布版本。

镜像不含模型权重。在后台下载或导入固定模型，完成独立 CPU 试跑及所选加速后端试跑，再用短歌曲验证结果。`htdemucs` 的 ROCm 后端与“干净档” MDX 的 MIGraphX 后端使用不同推理环境；当前 ROCm Dockerfile 安装的是 Torch/HIP，不能据此认定包含 MDX 的 MIGraphX provider。“干净档”未检测到该 provider 时使用通过试跑的独立 CPU 自动降级，不能将结果标记为 AMD GPU 推理成功。

780M 的驱动、固件、共享显存和模型推理仍需目标主机验证。环境检测、简单 GPU 算子与容器 `healthy` 均不能代替真实模型的试跑及长任务验收。

## 数据与资源

两个服务显式使用 `user: "0:0"`，覆盖现有镜像的固定非 root UID。无需设置宿主 UID/GID、`group_add`、初始化容器或执行 `chown`。默认不使用 `privileged` 或关闭容器安全隔离。普通 Linux Docker 中新文件通常归宿主 root，普通用户编辑部分文件仍可能需要管理员权限。

所有持久数据使用 `./singverse-*` 绑定目录，路径相对于 YAML 所在目录，缺失目录由 Docker/Compose 创建。没有顶层命名卷声明：

| 宿主相对目录 | 内容 |
| --- | --- |
| `./singverse-data` | SQLite、封面、元数据和音视频衍生文件 |
| `./singverse-secrets` | 业务主密钥 |
| `./singverse-compute-auth` | 计算端 token，管理端只读 |
| `./singverse-transcode-cache` | 计算缓存、硬件与模型选择、试跑记录 |
| `./singverse-audio-models` | 模型权重 |
| `./singverse-media` | 原始媒体，管理端只读 |
| `./singverse-downloads` | 下载文件 |

WebDAV 曲库在后台配置。本地媒体可放在 `./singverse-media`，或修改该挂载左侧为已有曲库绝对路径，后台填写容器路径 `/media`。计算端通过授权回调读取媒体，无需另挂载原始曲库。

项目名保持 `singverse-go-app`，使用默认 `18080` 端口。在原部署目录替换配置前先停止旧服务并备份。旧命名卷的数据不会自动搬到绑定目录；已有数据库、主密钥、token、设置和模型需成套保留。另开测试部署时使用独立目录、项目名和端口，不能与现有服务同时共享同一个 SQLite 数据库。

初始资源限额为业务端 `2GiB / 2 CPU`、计算端 `6GiB / 4 CPU`，音频进程预算 `4GiB`、媒体进程预算 `1GiB`、视频并发 `1`。宿主需在这些预算外为系统和核显共享内存留余量；音频预算是系统内存预算，不是显存设置。播放缓存 `20GiB`、计算缓存 `10GiB`，另为模型和衍生文件预留磁盘。按实际内存容量同时调整容器限额及进程预算。

SQLite 路线无需 MySQL 环境变量。可选配置中的空字符串具有明确的关闭或默认语义。桥接 Docker 的局域网发现需由宿主发布 mDNS，手机/TV 也可直接输入业务地址。

停止服务并保留绑定数据：

```sh
docker compose -f docker-compose.yaml down
```

备份时停止服务，一起保存 YAML 与七个数据目录，计算缓存也包含需要保留的设置。本轮仅完成配置与环境变量静态校验，未在 780M 主机上拉取、启动容器或验证硬件。
