# 管理平台镜像发布

`platform-release.yml` 仅在 GitHub 页面点击 Publish release、发布正式 Release 时触发。普通 push/PR、保存草稿和 Pre-release 不执行发布，也不提供 Run workflow 手动入口。流程直接准备组件、构建和上传，不运行单元测试、静态分析、lint 或独立 verify 阶段。

Release 标签使用 `vMAJOR.MINOR.PATCH`，如 `v0.0.2`。Actions 自动注入 `SINGVERSE_VERSION=0.0.2`；源码不维护 VERSION 文件，无需在 GitHub Variables 中设置版本。本地源码构建默认 `0.0.0`，指定版本可设置 `SINGVERSE_VERSION`。标签必须指向包含新版工作流的提交。

前端、手机和 TV 在首次打包时解析各自最新正式 Release，也可在 `components.json` 显式指定组件 tag。所有架构共用同一组件锁，重试复用原锁和 GHCR 中保存的 OCI 镜像。Android 包在打包机通过 `apksigner`、`aapt` 核对真实签名和包信息；最终容器无 JDK、Node 或 Go 编译器。

## 配置

在 Go 和 compute 仓库分别配置 Actions Variables 与 Secrets；二者独立发版。发布固定版本后，工作流将同一镜像内容推送至 GHCR 和 Docker Hub。

| Actions Variable | 含义与示例 |
| --- | --- |
| `GHCR_NAMESPACE`（可选） | GitHub 用户或组织；默认当前仓库 owner 的小写名称，如 `mynora-dot` |
| `DOCKERHUB_NAMESPACE` | Docker Hub 用户或组织 |

两个命名空间可不同，均填写小写名称，不填写完整镜像路径。配置缺失时会在构建前提示，不会静默跳过某个平台。

| Actions Secret | 用途 |
| --- | --- |
| `DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN` | Docker Hub 登录账号及具有推送权限的访问令牌 |
| `GHCR_TOKEN`、`GHCR_USERNAME`（可选） | 自定义 GHCR 凭据及账号；默认工作流 `GITHUB_TOKEN` 与 `github.actor` |
| `CROSS_REPO_READ_TOKEN`（仅 Go 私有组件需要） | 读取前端、手机、TV 的私有 Release，只需选定组件仓库的 Contents: read 权限 |

提前创建或授权 Docker Hub 命名空间及仓库；命名空间配置不会自动授予权限。跨 GitHub owner/org 发布按目标授权情况配置自定义 GHCR 凭据。凭据只放 Secrets；跨 GitHub 资产域名跳转时下载脚本移除 Authorization，凭据不进入构建上下文。

## 发布与重试

先将代码和工作流改动推送到 GitHub，再创建指向该提交的新标签和正式 Release。各仓库版本可独立选择。首次应先发布手机、TV 和前端，确认资产齐全后发布 Go；compute 可独立发布。

失败后可在 Actions 对同一次运行选择 Re-run failed jobs，复用原 Release 的组件锁和 GHCR 中的构建镜像。Re-run 始终使用原提交：修复工作流后应创建指向新提交的新版本，不能靠重跑旧记录加载新代码。已有固定镜像标签或附件如内容不同会拒绝覆盖。

Release 只保留重试必需的 `components.lock.json`；Docker/OCI 镜像、分片及其索引不上传为 Release 附件。各架构的构建镜像保存在 `ghcr.io/<ghcr_namespace>/singverse-buildcache`，标签包含版本、完整源码提交及架构；重试从该缓存按摘要恢复，再发布正式多架构镜像。GHCR 凭据须同时具备该缓存仓库与正式镜像的读写权限。清理缓存前确认相关版本不再需要重试。

## 镜像地址与部署

以下 `<VERSION>` 是 Release 标签去掉 `v` 后的版本号，例如 `v0.0.2` 对应 `0.0.2`：

| 平台 | 主服务镜像 |
| --- | --- |
| GitHub GHCR | `ghcr.io/<ghcr_namespace>/singverse:<VERSION>` |
| Docker Hub | `docker.io/<dockerhub_namespace>/singverse:<VERSION>` |

主服务只发布数字版本 `<VERSION>` 和 `latest` 两类标签，两者都通过镜像清单包含 `linux/amd64`、`linux/arm64`。发布时复制完整索引及其引用的镜像层，不再额外发布架构标签；两家固定版本全部成功后更新 `latest`。已有历史标签不会自动删除，数字版本会随发布累积。

compute 仓库独立发布 cuda、rocm、openvino、rknn 四种运行库，每种仅有 `<COMPUTE_VERSION>-<runtime>` 与 `latest-<runtime>`，一个版本共 8 个标签，不额外发布架构标签或 CPU 镜像。CPU 是四种加速镜像内置的降级环境；Mac ARM64 原生安装包和 SHA-256 文件附加到 compute 的同一 Release，不占 Docker 标签。

镜像发布会实时输出目标标签及 Skopeo 的镜像层进度，每 30 秒报告仍在上传的目标与耗时。远端标签查询限时 2 分钟，每次复制限时 30 分钟，期间最多重试 2 次；发布 job 总限时 120 分钟。超时即停止，重跑同一 Release 会复用相同摘要的已有标签及 GHCR 构建镜像。

部署者在 `.env` 设置 `IMAGE_PREFIX=<仓库域名>/<命名空间>`，并固定 `SINGVERSE_VERSION` 和 `COMPUTE_VERSION`。同一 Compose 的两类镜像共用 `IMAGE_PREFIX`，因此启用同机 compute 时应把两个仓库发布到对应平台的同一命名空间；独立计算节点可在自己的 `.env` 选择其他前缀。私有镜像需在部署主机执行 `docker login <仓库域名>`。镜像命名空间不改变 Compose 项目名、服务名或数据卷。具体部署和切换步骤见 [README](README.md) 与 [OPERATIONS](OPERATIONS.md)。

## 流程

1. 检查部署、发布脚本及目标配置。
2. 复用本 Release 已有 components.lock.json，或一次解析前端与两端正式包。
3. 验证资产摘要、真实 APK 签名/包信息；保存锁清单到 Release，不覆盖已有附件。
4. 上传共同构建输入，amd64/arm64 分别构建并保存 OCI 镜像到 GHCR 固定缓存；不执行自动测试、lint 或容器运行验收。
5. 从 GHCR 恢复同一份 OCI 产物，向双仓库发布固定版本。固定 tag 已有不同内容即失败，全部通过后更新 latest。

本地 make docker-build 每次获取最新前端和两端正式包；指定 COMPONENT_LOCK 可复用既定输入。更换任何组件形成新镜像内容时，使用新的 Go 产品版本。首次 Release 顺序见[多仓库说明](../../docs/multi-repository.md)。

检查工作流文件不代表远程 CI 或正式镜像已经发布。

每架构 OCI 镜像保存在现有的 GHCR `singverse-buildcache` 仓库，使用包含版本、源码提交和架构的固定缓存标签；重试按镜像摘要恢复，防止构建时间等变化使固定镜像内容不同。镜像不上传到 Release 或 Actions artifacts；只有共同组件输入仍使用保留 7 天的临时 artifact。发布过程中不得删除或替换 Release 的组件锁及 GHCR 的固定构建缓存。

Docker 构建使用 `ghcr.io/<GHCR_NAMESPACE>/singverse-buildcache:<arch>` 的独立 registry 层缓存，按架构隔离并使用 `mode=max`。现有 GHCR 凭据同时用于缓存包；默认令牌的 build job 获得 `packages: write`，已有包需授权本仓库 Actions，跨 owner 凭据也需覆盖缓存包。正式 `singverse` 仍只有数字版本与 `latest`。缓存缺失时正常构建，缓存导出失败不阻断正式产物；固定构建镜像保存成功后才进入正式推送。Go modules 存在可导出的普通构建层内，版本参数在依赖下载之后注入。

主服务镜像较小，不执行耗时的预装 SDK 清理；准备组件的 job 保留 Android SDK/JDK。发布 job 优先使用 runner 预装 Skopeo，缺失时才安装。每个 job 使用独立的临时 runner，磁盘和本地缓存不跨 job 共享。
