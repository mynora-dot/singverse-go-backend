# 多仓库协作与首次发布

五个仓库独立检出、测试和发版，源码均位于各自根目录。原 SingVerse 保留历史；迁移以当前代码初始提交开始。

| 仓库 | 交付内容 |
|---|---|
| mynora-dot/singverse-frontend | 管理、亲友、公开下载三个页面的完整 dist |
| mynora-dot/singverse-go-backend | Go、数据库、系统契约、管理平台镜像、组合 Compose |
| mynora-dot/singverse-compute | Python、固定运行库、模型工具、计算节点镜像 |
| mynora-dot/singverse-mobile | Android 手机通用 APK |
| mynora-dot/singverse-tv | Android TV 通用 APK |

前端保存品牌源素材；手机与电视保留工程内派生资源。所需第三方声明随代码迁移，包括 Go 的 [ktv-home MIT 许可](third-party/ktv-home-LICENSE.txt)。各仓库 .gitignore 排除环境配置、签名材料、SDK 本地路径、数据库与工具缓存。

## 契约

Go 仓库为 contracts 权威来源；其他仓库保留必要快照，并用 SOURCE.json 记录来源仓库、提交和文件摘要。修改跨端协议时，先更新 Go 契约及行为测试，再同步消费方快照。普通检查不隐式访问相邻源码。快照内对 Go 文档、数据库说明及内部资源的引用使用固定提交链接，避免消费仓库依赖 Go 的目录布局。

真实 Go/Python 集成通过 SINGVERSE_COMPUTE_PROJECT 选择一个准备好 .venv 的固定检出；CI 可检出指定 revision，再显式传参。Go 不负责安装 Python 依赖。

## 版本与依赖

各仓通过正式 GitHub Release 标签独立递增版本，例如 v0.0.2；Actions 自动注入 SINGVERSE_VERSION，不维护 VERSION 文件。本地开发默认 0.0.0。Android 版本码为 major × 1000000 + minor × 1000 + patch；minor/patch 为 0–999，结果必须为正数且不超过 2100000000，并高于上一正式包。两端发布通用 APK，版本不由 CI run_number 决定。

Go 的 components.json 指定三个组件仓库；前端、手机和 TV 在每次新平台打包时解析各自最新正式 Release（也可为组件显式设置 tag）。构建先把两端 release ID、asset ID、摘要和元数据固定为组件锁清单。不同架构共用这份输入；正式重试复用 Release 附件 components.lock.json，同版本不替换为另一组内容。

## 首次发布顺序

1. 在手机、TV 仓库按各自 README 配置长期签名 Secrets，安全备份原始密钥。
2. 发布两端正式 Release，检查签名 APK、release.json 和 SHA256SUMS。
3. 发布前端完整静态包，确认包含 admin、party、downloads。
4. 配置 Go 的跨仓只读权限与三平台镜像发布 Variables/Secrets，再发布指向最新源码的正式 Release。Actions 自动使用 Variables，直接构建和上传，不运行自动测试、lint 或独立 verify，见[发布参数](../infra/docker/PUBLISHING.md)。
5. compute 配置其自身镜像发布环境后独立发布正式 Release；部署显式选择 COMPUTE_VERSION。同机 Compose 共用 IMAGE_PREFIX，应把主服务和 compute 发布到对应平台的同一空间。

当前没有既有正式 APK；旧 debug 安装的证书可能不同。覆盖升级只支持相同包名与受信签名，更新功能不会自动卸载或清除用户数据。

长期签名不能由普通构建临时生成。临时测试签名只能用于隔离验证；生产流程未配置签名时必须失败。

## 运行与回退

管理平台运行期间只提供镜像内 APK，不联系 GitHub。换用新平台镜像后客户端检查新的内置版本；客户端不自动降级。手机仅从保存的家庭 NAS 检查，不跟随临时扫码聚会来源。

管理平台回退仍需匹配的数据及密钥备份；安装包与镜像版本清单不能代替数据库备份。停止服务保留命名卷，媒体原件只读。
