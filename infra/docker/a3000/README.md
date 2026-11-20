# A3000 单文件部署

将本目录的 `docker-compose.yaml` 单独复制到 Linux AMD64 部署主机即可。Go 镜像内含管理端、亲友点歌和 APK 下载页；CUDA 计算端负责视频转码及音频处理。所有运行参数直接写在 YAML 中，不需要 `.env`、环境变量导出或源码构建。

镜像地址沿用仓库现有部署的 `mynora` 空间：

- `docker.io/mynora/singverse:latest`
- `docker.io/mynora/singverse-compute:latest-cuda`

如果实际推送了其他标签，直接修改 YAML 的两处 `image`。业务端和计算端独立选择版本，计算端须使用 `-cuda` 变体。生产升级建议记录并固定实际发布的版本或 digest。本次检查 Docker Hub 时网络连接被重置，未确认这两个远端标签及其摘要。

## 启动

宿主机先安装兼容镜像 CUDA/FFmpeg 运行库的 NVIDIA 驱动和 NVIDIA Container Toolkit，并将 Toolkit 配置到 Docker。`nvidia-smi` 应能列出 A3000；仅安装驱动还不足以向容器提供 GPU。Compose 使用 NVIDIA GPU reservation，无需 Swarm。详细硬件约定见 [硬件说明](../GPU.md)。

在保存 YAML 的目录执行：

```sh
nvidia-smi
docker compose -f docker-compose.yaml config --quiet
docker compose -f docker-compose.yaml pull
docker compose -f docker-compose.yaml up -d --no-build --wait --wait-timeout 180
docker compose -f docker-compose.yaml ps
```

私有镜像先执行 `docker login docker.io`。

管理入口为 `http://<服务器局域网IP>:18080/admin/`，新数据库的初始账号密码为 `admin/admin`，登录后修改密码。亲友点歌入口为 `/party/`，APK 下载入口为 `/downloads/`。HTTP 端口监听所有宿主网卡；计算端 `8090` 只在 Compose 内部网络可达。

管理端与计算端通过同一个 `./singverse-compute-auth` 目录自动使用同一个 token，密钥由程序生成并持久化。管理端没有计算端启动依赖，计算端故障时仍可打开管理页面。后台的性能节点选择“自动连接”；已有数据库若保存了手动连接或禁用设置，需要在后台改回自动连接。

## A3000 检查

默认选择宿主 GPU `0`，多卡主机先用 `nvidia-smi -L` 确认，再修改 `deploy.resources.reservations.devices[0].device_ids` 为 A3000 的编号或 UUID。容器只接收选中的一张卡，`CUDA_VISIBLE_DEVICES` 保持 `0`。

```sh
docker compose -f docker-compose.yaml exec -T singverse-compute nvidia-smi
docker compose -f docker-compose.yaml exec -T singverse-compute python -c 'import torch; print("CUDA:", torch.cuda.is_available()); assert torch.cuda.is_available(); print(torch.cuda.get_device_name(0))'
docker compose -f docker-compose.yaml exec -T singverse-compute /opt/singverse-audio-accelerator/bin/python -c 'import onnxruntime as ort; p=ort.get_available_providers(); print(p); assert "CUDAExecutionProvider" in p'
docker compose -f docker-compose.yaml exec -T singverse-compute /usr/lib/jellyfin-ffmpeg/ffmpeg -hide_banner -loglevel error -f lavfi -i color=s=1280x720:r=25:d=1 -vf format=nv12 -c:v h264_nvenc -frames:v 10 -f null -
```

默认视频后端为 `nvenc`，转码并发为 `1`。后台保存的硬件选择会覆盖部署默认值；从 Intel/其他主机迁移后，应在后台选择“跟随部署”或 NVENC，并重新试跑。

模型权重通过管理端下载或导入，在后台选择 CUDA 并完成音频模型试跑。`healthy`、CUDA 枚举和 ONNX provider 列表不能代替实际素材的推理与转码验收。A3000 显存按设备实际容量识别，无需把显存大小填进环境变量。

## 数据与资源

项目名保持 `singverse-go-app`，只有管理端和计算端两个服务。所有数据使用 `./singverse-data:/data` 这样的相对目录挂载，路径相对于 `docker-compose.yaml` 所在目录；新安装时 Docker/Compose 自动创建缺失目录。无需顶层 `volumes:` 声明、初始化容器、`chown` 命令或 `.env` 文件。

镜像默认的 `USER 10001:10001` 是非 root 运行策略，程序本身没有固定 UID 要求。此配置在两个服务中显式设置 `user: "0:0"` 覆盖镜像默认值，因此不需要给自动创建的目录改所有者，也无需重新构建镜像。仅删除 `user` 配置仍会沿用镜像的 `10001` 用户，无法解决写入权限。

这版容器以 root 运行；在普通 Linux Docker 部署中，新生成文件通常归宿主 root，宿主普通用户编辑或备份部分文件仍可能需要管理员权限。若要让文件归宿主普通用户，需要改为该用户的 UID/GID 并预先准备可写目录，不能使用一组固定数字适配所有机器。

| 宿主相对目录 | 容器路径 | 内容 |
| --- | --- | --- |
| `./singverse-data` | `/data` | SQLite、封面、元数据和音视频衍生文件 |
| `./singverse-secrets` | `/secrets` | 业务主密钥 |
| `./singverse-compute-auth` | `/run/singverse-compute` | 计算端 token，管理端只读 |
| `./singverse-transcode-cache` | `/var/cache/singverse-transcode` | 计算临时文件及硬件/模型设置 |
| `./singverse-audio-models` | `/var/lib/singverse-models` | 模型权重 |
| `./singverse-media` | `/media` | 原始媒体，管理端只读 |
| `./singverse-downloads` | `/downloads` | 下载文件 |

WebDAV 曲库在后台配置即可。使用本地曲库时，可将歌曲放到 `./singverse-media`，或将挂载左侧改为已有曲库的绝对路径，后台路径填写 `/media`。计算端通过授权回调读取媒体，不另挂载原始文件。

如果原部署使用同名的 `./singverse-data` 等目录，在同一个部署目录使用本文件即可继续读取它们。如果已经用旧版命名卷启动过，先停止服务并备份，将旧卷中的数据、主密钥、节点认证、缓存设置和模型完整迁移到上述对应目录，再切换配置；改写挂载不会自动搬迁旧卷数据。此文件不会删除旧命名卷，也不要同时运行两个项目共享同一 SQLite 数据库。

初始资源限额：业务容器 `2GiB / 2 CPU`，计算容器 `6GiB / 4 CPU`。音频子进程预算 `4GiB` 是系统内存限制，与显存不同；按宿主可用内存一起调整 YAML 的预算和容器限额。播放缓存预算 `20GiB`，计算临时缓存 `10GiB`，还需为模型、原件和衍生文件预留空间。

SQLite 部署不需要 MySQL 变量。YAML 中空字符串表示关闭可选的 HTTPS 反代、独立 TV 安装包目录、原生 mDNS 或诊断监听；它们不是待填的必需配置。桥接 Docker 的局域网发现需由宿主发布 mDNS，手机/TV 也可直接输入业务地址。

停止并保留数据：

```sh
docker compose -f docker-compose.yaml down
```

备份时停止服务，一起保存 `docker-compose.yaml` 和上述七个目录。`down` 不会删除这些绑定目录；计算缓存目录也保存硬件和模型设置，需要一起备份。本次只完成配置静态校验，没有在目标 A3000 主机运行容器或进行 GPU 实测。
