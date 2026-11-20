# 硬件与计算节点

音频推理运行库由 `COMPUTE_RUNTIME` 选择，视频设备由覆盖文件映射，两者分开配置。镜像不包含模型权重，不安装宿主驱动，不启用 privileged。模型在管理端下载或导入，实际能力以素材试跑为准。

| 运行库 | 镜像架构 | 硬件覆盖 / 用途 |
|---|---|---|
| cuda | AMD64 | `compose.nvidia.yaml`；兼容 NVIDIA 驱动和 Container Toolkit |
| rocm | AMD64 | `compose.rocm.yaml`；通用 ROCm 与独立 gfx1103 运行库合并在同一镜像，按设备探测选择 |
| openvino | AMD64 | `compose.dri.yaml`；Intel 推理，视频 QSV 单独选择 |
| rknn | ARM64 | `compose.rknn.yaml`；匹配的瑞芯微 BSP、运行库和已转换模型 |

四种镜像均包含 FFmpeg、媒体探测和独立 CPU 降级环境；CPU 不单独发布，也不作为可手动选用的 AI 设备。加速不可用或执行失败时，按共享 `cpuFallback` 开关使用已试跑的 CPU，关闭后不降级。视频 NVIDIA 使用 `compose.nvidia.yaml`，VAAPI/QSV 使用 `compose.dri.yaml`，瑞芯微 MPP/RGA 使用 `compose.rockchip.yaml`。RKNN 音频 NPU 与 MPP 视频映射不能相互替代。

## 同机 GPU

先按部署指南启用 compute，再选择运行库与覆盖文件。示例为 NVIDIA 预构建镜像：

```sh
COMPOSE_PROFILES=compute COMPUTE_NODE_URL=http://singverse-compute:8090 COMPUTE_RUNTIME=cuda \
  docker compose --project-directory . -f infra/docker/compose/compose.yaml \
  -f infra/docker/compose/compose.nvidia.yaml up -d --no-build --pull always --wait
```

本仓库的源码覆盖 `compose.build.yaml` 只构建 Go，不构建 compute。计算源码镜像在独立 compute 仓库按其 [Docker 说明](https://github.com/mynora-dot/singverse-compute/blob/main/infra/docker/README.md)构建；同机组合显式选择该镜像。AMD 设置 `COMPUTE_RENDER_GID` 和 `COMPUTE_VIDEO_GID`；DRI 设置 `COMPUTE_RENDER_GID`，通过 `COMPUTE_HARDWARE_DEVICE` 选择宿主 render 设备。视频后端可用 `COMPUTE_HARDWARE_BACKEND` 显式选择，默认 auto，不代表已经合格。

RKNN 需设置 `RKNPU_CARD_GID`、`RKNPU_GID`，必要时设置设备路径及 `RKNN_RUNTIME_LIBRARY`。宿主提供兼容 `librknnrt.so`。通过管理平台下载或导入时，应为父模型目录提供可写持久化挂载，并省略 `compose.rknn.yaml` 中单独的只读模型挂载；该旧预置方式仍需 `RKNN_MODEL_PATH` 指向已有转换目录。RKNN ZIP 支持单芯片或按 rk3566/rk3568/rk3588 分目录的多芯片模型，在线下载需填写公网 HTTPS 地址和完整包 SHA-256。无 `.env` 的相对目录挂载示例见 [RK3566 测试部署](rk3566/README.md)。转换和真机验证按 compute 模块说明执行，不把模型打入公共运行镜像。

## 独立计算节点

在计算主机运行，不启动业务服务：

```sh
# 镜像路线；先在 .env 选择 Registry、版本及 COMPUTE_RUNTIME
# GPU 场景追加相应硬件覆盖文件
docker compose --project-directory . -f infra/docker/compose/compose.compute.yaml \
  up -d --no-build --pull always --wait
# compute 源码构建在独立仓库追加 compose.compute-build.yaml，并使用 up -d --build
```

默认只监听 `127.0.0.1:18090`。跨主机时设置 `COMPUTE_HTTP_BIND` 为受限 LAN 地址，在管理端性能节点页面配对或配置连接密钥；必要时通过受控管理员终端读取：

```sh
docker compose --project-directory . -f infra/docker/compose/compose.compute.yaml \
  exec -T singverse-compute cat /run/singverse-compute/token
```

密钥是凭据，不复制到仓库或公开日志。Go 的回调地址必须能由计算主机访问，通过管理端或 `SINGVERSE_CALLBACK_ORIGIN` 配置。对跨网络访问落实 TLS 或受限网络与防火墙，客户端仍只连接 Go。

## macOS

使用 singverse-compute 正式 Release 的 `singverse-compute-macos-arm64-<版本>.tar.gz` 和校验文件，按 [Mac 原生安装指南](https://github.com/mynora-dot/singverse-compute/blob/main/MACOS.md)准备原生 ARM64 Python 3.12，运行 `Install.command` 与 `Start.command`。此包提供 MPS / MLX / VideoToolbox，CPU 为内部自动降级环境，不增加 Docker 标签，也不需要新增 GitHub Variables 或 Secrets。Docker 中的业务服务连接 `http://host.docker.internal:18090`，回调使用原生节点可访问的 NAS HTTP 地址；Linux Docker 容器不能代替原生 Apple GPU 节点。

## 验证限制

Actions 只构建与发布，不运行独立测试或硬件验收。宿主驱动、显存、色调映射、硬解和零拷贝必须在目标主机试跑并单独记录。
