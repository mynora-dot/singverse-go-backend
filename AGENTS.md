# SingVerse Go 协作指南

默认使用简体中文。修改前查看 git status 和目标目录 AGENTS.md，保留已有修改、锁文件、本地配置与数据。源码位于仓库根目录，不隐式依赖相邻检出。

Go 是唯一业务主服务与业务数据库写入方；compute 只执行内部媒体计算。使用 database/sql + sqlx、所属模块嵌入 SQL 与显式事务；跨模块事务复用 store.DBTX。Gin 只处理 HTTP 边界。前端、手机、TV 只调用 Go，前端请求同源 /api。

根 VERSION 是唯一产品版本来源。components.json 固定前端正式 Release；新平台打包解析一次两端最新正式包，所有架构与重试复用组件锁。正式包缺失、签名、摘要或版本错误必须失败。运行期间只从镜像内提供 APK，公开下载不扩大管理或曲库权限。

contracts 是权威契约；消费仓库保存带 SOURCE.json 的快照。前端完整 dist 同时包含 admin、party、downloads；Go 构建前准备并嵌入，不引入生产 Node/JVM。工具使用 scripts/go-env.sh 与仓库 .tools，禁止隐式访问旧仓库。

默认 SQLite；MySQL 显式选择，失败不回退。新增迁移，不修改已部署基线。保留 Compose 项目名、服务与命名卷，不删除数据卷、重置账号或修改原始媒体。compute 镜像独立选择 COMPUTE_VERSION。

不提交环境文件、凭据、签名密钥、SDK 路径、数据库、日志、缓存或构建产物。发布标签不可覆盖为不同内容；本地开发任务不授权远端推送或发布。

按 README 与 Makefile 运行对应检查。真实 Go/compute 联动通过 SINGVERSE_COMPUTE_PROJECT 显式指定固定检出。交付报告本次实际结果；本机构建和 CI 配置不能代替远程 CI、NAS、手机、TV 或 GPU 真机验收。

通用技能维护在项目外的用户技能目录（`$CODEX_HOME/skills`，默认 `~/.codex/skills`），按任务读取；不在仓库内保存技能副本。
