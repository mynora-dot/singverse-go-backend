# Go 仓库布局

| 目录 | 内容 |
|---|---|
| cmd/ | 主服务、恢复入口和资料校验器 |
| internal/ | 显式组装、业务模块、平台与嵌入前端 |
| internal/clientrelease/ | 两端发行清单、只读 APK 下载、二维码 |
| contracts/ | 系统权威 API 与跨端契约 |
| database/migrations/ | SQLite/MySQL dbmate 迁移 |
| scripts/ | Go 工具、前端与平台输入准备、显式集成检查 |
| infra/docker/ | Go Dockerfile、组合 Compose、镜像验证与发布 |
| docs/ | 当前说明、第三方许可及注明范围的历史验证 |
| .build/platform/ | 被忽略的已校验前端、两端 APK 和组件锁 |

根 VERSION 定义 Go 版本；components.json 固定前端 Release。手机、TV 和 compute 在各自仓库维护版本、源码、构建工具及发布链。系统分工和发布顺序见[多仓库说明](multi-repository.md)。
