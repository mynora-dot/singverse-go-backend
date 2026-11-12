# 管理平台镜像发布

仅 GitHub 正式 Release 触发 platform-release.yml，tag 必须匹配根 VERSION。前端由 components.json 固定，手机和 TV 取各自 Latest 正式 Release；不接受 draft/prerelease。Android 包在打包机通过 apksigner、aapt 校验，最终容器无 JDK、Node 或 Go 编译器。

## 配置

Actions Variables：GHCR_NAMESPACE（默认仓库 owner）、DOCKERHUB_NAMESPACE、ACR_REGISTRY、ACR_NAMESPACE。Actions Secrets：DOCKERHUB_USERNAME、DOCKERHUB_TOKEN、ACR_USERNAME、ACR_PASSWORD；GHCR 使用当前工作流 GITHUB_TOKEN。

私有组件仓库另配 CROSS_REPO_READ_TOKEN，只需目标仓库 contents:read。下载请求跨 GitHub 资产域名跳转时移除 Authorization，凭据不进入构建上下文。

## 流程

1. 检查部署、发布脚本及目标配置。
2. 复用本 Release 已有 components.lock.json，或一次解析前端与两端正式包。
3. 验证资产摘要、真实 APK 签名/包信息；保存锁清单到 Release，不覆盖已有附件。
4. Go 检查通过后上传共同构建输入，amd64/arm64 分别构建及验收；验证离线 APK 下载和 OCI 对应运行镜像。
5. 使用已验证 OCI 产物向三平台发布固定版本。固定 tag 已有不同内容即失败，全部通过后更新 latest。

本地 make docker-build 每次获取最新两端正式包；指定 COMPONENT_LOCK 可复用既定输入。更换任何组件形成新镜像内容时，使用新的 Go 产品版本。首次 Release 顺序见[多仓库说明](../../docs/multi-repository.md)。

检查工作流文件不代表远程 CI 或正式镜像已经发布。

已验收的每架构 OCI 归档同时保存在平台 Release，重试先复用再重新验收，防止构建时间等变化使固定镜像内容不同；Actions 临时 artifact 过期后仍可恢复。发布过程中不得手动删除或替换组件锁及 OCI 归档。
