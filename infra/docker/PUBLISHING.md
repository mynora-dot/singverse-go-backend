# 管理平台镜像发布

`platform-release.yml` 将同一组已验收镜像推送到 GitHub Container Registry（GHCR）、Docker Hub 和阿里云 ACR。支持正式 Release 自动发布，以及通过 Actions → platform-release → Run workflow 手动选择已有正式 Release 和三个平台的项目空间。GitHub Release 保存组件锁与 OCI 归档，容器镜像保存在 GHCR；二者用途不同。

发布 tag 必须匹配所选源码根 `VERSION`（`v` + 版本号），不接受 draft/prerelease。手动发布使用该 tag 对应的源码及 revision。前端由 `components.json` 固定，手机和 TV 在首次打包时取各自最新正式 Release。Android 包在打包机通过 `apksigner`、`aapt` 校验，最终容器无 JDK、Node 或 Go 编译器。

## 配置

在 Go 和 compute 仓库分别配置 Actions Variables 与 Secrets；二者独立发版。手动输入优先于仓库 Variables，留空则使用同名大写 Variable。自动正式 Release 事件使用 Variables。

| Run workflow 参数 | 回退 Variable | 含义与示例 |
| --- | --- | --- |
| `release_tag` | 无 | 已存在的正式 Release，例如 `v0.1.0`；Go 手动发布必填 |
| `ghcr_namespace` | `GHCR_NAMESPACE` | GitHub 用户或组织，如 `your-org`；均未设置时使用当前仓库 owner 的小写名称 |
| `dockerhub_namespace` | `DOCKERHUB_NAMESPACE` | Docker Hub 用户或组织，如 `your-team` |
| `acr_registry` | `ACR_REGISTRY` | ACR 实例的实际仓库域名，例如 `registry.cn-hangzhou.aliyuncs.com`；不含协议、路径或末尾斜杠 |
| `acr_namespace` | `ACR_NAMESPACE` | ACR 实例内的命名空间，如 `your-project` |

三个命名空间可不同，均填写小写名称，不填写完整镜像路径。域名应使用 ACR 控制台提供、且 GitHub runner 可访问的地址。空间及凭据缺失或参数格式错误会使发布失败，不会静默跳过某个平台。

| Actions Secret | 用途 |
| --- | --- |
| `DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN` | 有权向所选 Docker Hub 用户/组织推送的登录账号及访问令牌 |
| `ACR_USERNAME`、`ACR_PASSWORD` | 有权向所选 ACR 实例与命名空间推送的账号及仓库密码 |
| `GHCR_TOKEN`、`GHCR_USERNAME`（可选） | 自定义 GHCR 凭据及其登录账号；默认使用工作流 `GITHUB_TOKEN` 与 `github.actor` |

选择命名空间不会自动授予权限。默认 `GITHUB_TOKEN` 需要对目标 package 有写权限；跨 GitHub owner/org 发布时，按目标授权情况配置自定义 GHCR 凭据。提前在 Docker Hub/ACR 创建或授权相应命名空间与仓库。凭据只放 Secrets，不作为 Run workflow 参数、Compose 配置或构建参数。

私有组件仓库另配 CROSS_REPO_READ_TOKEN，只需目标仓库 contents:read。下载请求跨 GitHub 资产域名跳转时移除 Authorization，凭据不进入构建上下文。

## 手动发布示例

先将工作流改动合入默认分支，准备符合版本约定的正式 Release。在 Run workflow 中选择包含新版发布工作流的分支，填写 `release_tag` 以及需要覆盖的目标参数。也可以用 GitHub CLI（将示例仓库、版本和空间替换为实际值）：

```sh
gh workflow run platform-release.yml --repo your-org/singverse-go-backend \
  -f release_tag=v0.1.0 \
  -f ghcr_namespace=your-org \
  -f dockerhub_namespace=your-team \
  -f acr_registry=registry.cn-hangzhou.aliyuncs.com \
  -f acr_namespace=your-project
```

这次运行只覆盖本次目的地，不修改 Variables。重新发布同一版本到新空间会复用原 Release 的组件锁和已验收 OCI 归档。更换命名空间不应修改 `VERSION` 或重打同一固定标签的内容。

手动发布也会在固定版本全部成功后更新目标空间的 `latest`。选择历史 Release 会使这些空间的 `latest` 指向该历史版本；仅需使用某个稳定版本的部署应固定数字标签。

## 镜像地址与部署

以下 `VERSION` 代表根版本号，不含 Release tag 的 `v` 前缀：

| 平台 | 主服务镜像 |
| --- | --- |
| GitHub GHCR | `ghcr.io/<ghcr_namespace>/singverse:<VERSION>` |
| Docker Hub | `docker.io/<dockerhub_namespace>/singverse:<VERSION>` |
| 阿里云 ACR | `<acr_registry>/<acr_namespace>/singverse:<VERSION>` |

主服务包含 `amd64`、`arm64`，同时发布 `<VERSION>-amd64`、`<VERSION>-arm64` 和多架构 `<VERSION>` 标签；全部固定版本成功后更新 `latest`。compute 仓库独立发布 `singverse-compute:<COMPUTE_VERSION>-<runtime>`（如 `0.1.0-base`）及对应架构标签、`latest-<runtime>`。

部署者在 `.env` 设置 `IMAGE_PREFIX=<仓库域名>/<命名空间>`，并固定 `SINGVERSE_VERSION` 和 `COMPUTE_VERSION`。同一 Compose 的两类镜像共用 `IMAGE_PREFIX`，因此启用同机 compute 时应把两个仓库发布到对应平台的同一命名空间；独立计算节点可在自己的 `.env` 选择其他前缀。私有镜像需在部署主机执行 `docker login <仓库域名>`。镜像命名空间不改变 Compose 项目名、服务名或数据卷。具体部署和切换步骤见 [README](README.md) 与 [OPERATIONS](OPERATIONS.md)。

## 流程

1. 检查部署、发布脚本及目标配置。
2. 复用本 Release 已有 components.lock.json，或一次解析前端与两端正式包。
3. 验证资产摘要、真实 APK 签名/包信息；保存锁清单到 Release，不覆盖已有附件。
4. Go 检查通过后上传共同构建输入，amd64/arm64 分别构建及验收；验证离线 APK 下载和 OCI 对应运行镜像。
5. 使用已验证 OCI 产物向三平台发布固定版本。固定 tag 已有不同内容即失败，全部通过后更新 latest。

本地 make docker-build 每次获取最新两端正式包；指定 COMPONENT_LOCK 可复用既定输入。更换任何组件形成新镜像内容时，使用新的 Go 产品版本。首次 Release 顺序见[多仓库说明](../../docs/multi-repository.md)。

检查工作流文件不代表远程 CI 或正式镜像已经发布。

已验收的每架构 OCI 归档同时保存在平台 Release，重试先复用再重新验收，防止构建时间等变化使固定镜像内容不同；Actions 临时 artifact 过期后仍可恢复。发布过程中不得手动删除或替换组件锁及 OCI 归档。
