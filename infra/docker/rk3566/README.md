# RK3566 测试部署

面向约 4GiB 内存的 ARM64 Linux RK3566，沿用 A3000 的单文件双容器方式，不需要 `.env`。镜像使用 `docker.io/mynora/singverse:latest` 与 `docker.io/mynora/singverse-compute:latest-rknn`；远端标签和架构本轮未验证，可替换为实际发布版本。

## 启动前

1. 核对系统为 `aarch64`，宿主安装匹配的 RKNPU 驱动和 ARM64 `librknnrt.so`。当前应用的 RKNN Lite2/模型工具链约定为 2.3.2，不能用复制运行库代替安装驱动。
2. 核对 `/dev/dri/card0` 和 `/dev/dri/renderD128` 实际属于 RKNPU。配置按已有 RK3566 实测映射，把真实 `renderD128` 同时映射为 Lite2 所需的 `renderD129` 别名；若板端路径不同，修改 `devices` 左侧，不能直接套用其他板的节点。
3. 核对 `/usr/lib/librknnrt.so` 和 `/sys/firmware/devicetree/base/compatible` 存在；运行库在其他位置时修改 bind 的 `source`。文件挂载关闭自动创建，缺失即失败。
4. 将文件放在新建的独立测试目录。本配置使用项目名 `singverse-rk3566-test` 和宿主端口 `18081`，与标准部署的 `singverse-go-app` / `18080` 分开；七个数据目录也只用于本次测试。确认 `18081` 尚未占用。

```sh
uname -m
ls -l /dev/dri/card0 /dev/dri/renderD128 /usr/lib/librknnrt.so
readlink -f /sys/class/drm/card0/device/driver
readlink -f /sys/class/drm/renderD128/device/driver
tr '\0' '\n' < /sys/firmware/devicetree/base/compatible
docker compose -f docker-compose.yaml config --quiet
docker compose -f docker-compose.yaml pull
docker compose -f docker-compose.yaml up -d --no-build --wait --wait-timeout 180
docker compose -f docker-compose.yaml ps
```

打开 `http://<RK3566局域网IP>:18081/admin/`。新数据库初始登录为 `admin/admin`，登录后修改密码。后台计算节点选择“自动连接”；两个容器通过目录共享自动生成的 token，计算端不公开端口。

## 音频与视频测试

使用包含本次 RKNN 模型管理改动的新管理端和计算端镜像后，可直接在管理平台为“瑞芯微 NPU 分离模型”选择“离线导入”，上传根目录包含 `manifest.json` 和 `uvr.rknn` 的模型 ZIP。“下载到节点”接受公网 HTTPS ZIP 地址和完整包 SHA-256。多芯片包可分别包含 `rk3566/`、`rk3568/`、`rk3588/` 目录，每个目录放同名的两个文件，节点会自动选择 RK3566。安装后仍须试跑，包不得超过 512MiB。

容器会将模型安装到现有可写绑定目录，无需命名卷、额外环境变量、宿主 Python 或手动 `chown`。现有 Docker Hub 标签不会自动包含本地代码修改，需要重新构建并发布前端嵌入的管理端镜像和 RKNN 计算端镜像。当前没有预置公共模型包地址；可直接离线导入，或使用自己托管的 HTTPS 模型包地址。

也可将专门转换为 **rk3566** 的 `uvr-mdxnet-9482-rknn` 模型直接放入宿主目录，布局如下：

```text
singverse-audio-models/
└── uvr-mdxnet-9482-rknn/
    ├── manifest.json
    └── uvr.rknn
```

该目录对应容器 `/var/lib/singverse-models/uvr-mdxnet-9482-rknn`，是 `COMPUTE_AUDIO_MODEL_REPO` 指向的 `htdemucs` 的同级目录。模型不随 Compose 提供；缺少模型时管理端仍可启动，RKNN 会显示模型缺失。RK3588/RK3568 模型不能冒充 RK3566 模型。转换约定和既有板端记录见 compute 仓库的 `README.md` 与 `VERIFICATION.rk3566-2026-11-08.md`；旧记录不代表本轮镜像或目标设备已验收。

测试“AI 干净档”（`AI_CLEAN`）时，还需准备固定的 `uvr-mdxnet-9482` ONNX 基准模型，可在管理端下载对应模型，或放入 `./singverse-audio-models/uvr-mdxnet-9482/UVR_MDXNET_9482.onnx`。它与上面的转换模型分别保存，也用于独立 CPU 后端的资格试跑；只有 RKNN 文件时，干净档仍会提示缺少 ONNX 模型。

```sh
docker compose -f docker-compose.yaml logs --tail=100 singverse-compute
docker compose -f docker-compose.yaml exec -T singverse-compute python -c 'from rknnlite.api import RKNNLite; print("Lite2 import OK")'
# 放入模型后，校验模型与板型并实际初始化 NPU；不会回退 CPU。
docker compose -f docker-compose.yaml exec -T singverse-compute python -c 'from singverse_compute.infrastructure.rknn_audio import Forward; r=Forward("/var/lib/singverse-models/uvr-mdxnet-9482-rknn"); r.close(); print("RK3566 NPU init OK")'
```

随后在管理端选择 RKNN，完成模型试跑，再用短音频测试伴奏生成。首次验证关闭 CPU 自动降级，并确认任务实际后端为 `rknn`。Lite2 导入、NPU 初始化或健康检查通过不能代替真实模型推理。

默认视频后端为 `auto`，并发 1；当前只映射 NPU 设备，未通过硬件视频试跑时按默认 CPU 降级策略使用软件编码。需要测试 MPP 时，确认宿主 `/dev/mpp_service`、`/dev/rga`、`/dev/dma_heap` 存在，取消 YAML 中对应三行 `devices` 注释，再执行 `up -d` 更新容器。在后台选择并试跑 `rkmpp`；也可先测试 H.264 编码器：

```sh
docker compose -f docker-compose.yaml exec -T singverse-compute /usr/lib/jellyfin-ffmpeg/ffmpeg -hide_banner -loglevel error -f lavfi -i color=s=1280x720:r=25:d=1 -vf format=nv12 -c:v h264_rkmpp -frames:v 10 -f null -
```

NPU 与 MPP 需分别验证。既有 fnOS 记录中，SDK 读取设备树曾被容器隔离策略阻挡；若出现同样错误，保留错误并检查实际挂载与日志。此配置不自动关闭 AppArmor、系统路径保护或启用 privileged。

## 资源和数据

业务端上限 768MiB / 2 CPU，计算端 2GiB / 2 CPU；媒体子进程预算 512MiB，音频预算 1280MiB、2 线程。为宿主保留内存，先串行测试；2GiB 板需另外降低预算并验证，不能保证运行完整音频模型。缓存预算：播放 4GiB、计算 2GiB，剩余空间保护各 2GiB。

所有数据保存在 YAML 同目录的七个 `singverse-*` 目录，布局与 A3000 示例一致。容器显式使用 root，便于测试挂载，宿主新文件可能归 root。原件仅只读挂载到业务端；密钥、模型、数据库、缓存和本机覆盖配置均由本目录 `.gitignore` 排除。

```sh
# 停止并保留绑定目录数据
docker compose -f docker-compose.yaml down
```

本轮只做 Compose 静态校验，未在目标 RK3566 启动容器或进行 NPU/MPP 实测。
