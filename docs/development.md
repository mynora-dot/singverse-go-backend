# 独立 Go 仓库开发

源码在本仓库根目录，根 Makefile 是命令入口。固定工具链、数据库和业务规范见 [README](../README.md)、[数据库说明](../database/README.md)和[认证说明](authentication.md)。

完整前端从 components.json 固定的正式 Release 获取，或通过 FRONTEND_DIST 显式传入本地 dist。缺少 admin、party、downloads 任一入口必须失败，生产不运行 Node 或 JVM。前端与 compute 的单模块检查分别在各自仓库执行。

使用 make go-backend-test 执行格式、vet、staticcheck、govulncheck、race。首次 make go-tools-setup dbmate-setup 安装仓库本地工具，不修改全局环境。数据库/HTTP 测试使用隔离数据与随机端口。

跨仓验证使用 SINGVERSE_COMPUTE_PROJECT，不自动查找相邻目录。Docker 构建先校验三个发布组件，生产构建缺包或校验失败即停止。具体流程见[多仓库说明](multi-repository.md)。

业务 SQL 仍归 internal/<domain>/sql，使用 sqlx 和显式绑定；跨模块事务复用 store.DBTX，不引入 ORM，不让媒体/网络计算占用业务事务。后续数据库变化新增迁移，不修改已部署基线。

按变更范围运行实际检查，记录未执行的硬件与远程 CI 验收，不复用历史记录作为本次证据。
