# 分类词表内置封面

默认词表为当前 34 个标签：语种 7、发行年代 8、风格 8、情绪 5、场景 4、自定义 2。`defaults.json` 按稳定标签 ID 记录对应关系，与 SQLite/MySQL 初始化词表一致；每个 ID 对应同名的独立 JPEG。

34 张音乐主题插画于 2026-11-15 使用内置 imagegen 工具逐张生成。生产文件均为 1280×720（16:9）、JPEG 质量 85、无 EXIF，总体积 6,709,865 字节（约 6.4 MiB），最大单张 260,339 字节。原稿仅做统一缩放和 JPEG 编码，没有把标题烘焙进图片。主体居中，供后台横向缩略图和电视方形居中裁切使用；完整提示词集保存在 `prompts.json`。

`classification_defaults.go` 通过 `go:embed` 将图片和词表映射编入 Go 二进制，原生构建、Docker 构建均随包携带，不依赖外部图床或运行时 AI。首次启动在同一事务内为未编辑、仍启用且没有图片的默认标签补图，并保存 `classification_settings.builtin_covers_initialized=1`。升级前已编辑、上传或停用的标签保留，不恢复缺失标签。导入失败整批回滚并可重试。

完成一次导入后，上传替换、移除和重启仍按用户设置工作；图片版本为 JPEG 内容 SHA-256。资源变更不会覆盖已初始化的用户图片。默认词表的历史 SQL 初始化迁移保持原样，新的导入标记由 `202611150001_classification_cover_defaults.sql` 增量添加。

验证入口：`internal/catalog/classification_defaults_test.go` 覆盖完整资源、默认词表匹配、首次启动、重复启动、升级保护、事务回滚与重试；`internal/app/classification_covers_test.go` 验证后台/电视读取同一图片和版本、身份边界、停用和删除行为。MySQL 用 `make go-mysql-check` 运行真实隔离库验证。
