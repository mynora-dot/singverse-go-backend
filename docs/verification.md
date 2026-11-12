# 验证记录

## 2026-11-12 多仓库拆分与客户端更新

本节记录本次本地实施的实际结果。五仓库位于 singverse-all 下，原 SingVerse 保留，未执行远端推送或正式发布。固定版本来自各仓库根 VERSION；契约以 Go 仓库为权威。

| 范围 | 本轮结果 |
|---|---|
| Go | `make go-backend-test` 通过：gofmt、vet、staticcheck、govulncheck、race；govulncheck 未发现漏洞。首次 metadata 测试在并行负载下超时，限制并发后完整重跑约 145 秒通过，首次超时不计成功。 |
| SQLite | 全迁移及基线检查通过，得到 102 张表、78 个索引。 |
| Go/compute 联动 | 显式指定新 compute 仓库和固定 FFmpeg，隔离真实 HTTP/媒体往返约 6.3 秒通过。 |
| 账号恢复 | 原生 PTY 恢复 74 条断言通过。 |
| 前端 | 63 个文件共 540 项测试通过；类型检查与完整 admin、party、downloads 构建通过。静态 Release 包重复打包摘要一致。 |
| compute | 执行 228 项测试：209 项通过，19 项已有模型/GPU/跨仓 opt-in 测试按条件跳过；18 项发布与 OCI 缓存测试通过，Compose 及真实 Docker COPY 上下文检查通过。 |
| 手机 | Flutter 全套 128 项与后续 generation、重连与安装复核边界定向 11 项通过，analyze 无问题；Android 首轮51项单元测试、后续5项Payload定向测试及原生重编译、Debug APK 与仪器测试 APK 编译通过，发布校验 10 项通过。临时测试签名的真实 Release、aapt/apksigner/摘要核验、中文多行说明及 16000 UTF-8 字节限制检查通过；缺签名与 Debug 签名负向检查通过。 |
| TV | 72 项 JVM 单元测试、11 项发布测试通过；Debug 构建/lint、临时测试签名 Release 构建/元数据校验/lint 通过；未签名 Release 及 Debug 证书发布拒绝检查通过。 |

MySQL 隔离全包检查已通过。ARM64 业务镜像真实构建、隔离业务契约（166条共享及122条交付断言）、认证/MFA持久化与终端恢复、OCI内容一致性、断网匿名下载页和两端APK GET/HEAD/Range均通过。35组Compose配置及两种实际COPY上下文审查通过。首次断网检查发现衍生目录默认写入镜像工作目录，修复为/data后重建并通过；Docker Hub匿名认证连接重置，实际构建改用镜像代理获取相同固定SHA-256基础镜像，生产默认配置未改。手机与 TV 临时测试密钥已销毁，不用作生产签名材料。测试APK用于发行/下载协议，后续安装前复核增强以最终源码定向测试与编译验证。

AMD64平台镜像尚未本机实测。远程 GitHub Actions、正式三平台发布、真实手机/电视/NAS、微信及 GPU/NPU 设备未作为本轮通过项。系统安装权限返回、同签名递增版本覆盖安装、数据保留、手机录音/蓝牙及 TV 遥控焦点仍需设备验收。自动化测试、模拟器编译和本地构建不替代这些结果。

## 历史记录索引

- [Go 迁移专项验证](go-migration-verification.md)保留拆分前的 Java/Go、旧 Nginx、离线包与部署结果，仅适用于各记录对应版本。
- [管理界面设计及历次记录](admin-experience.md)保留界面约定与当时事实，不作为本次重新验收。
- [compute 历史记录](https://github.com/mynora-dot/singverse-compute/blob/main/README.md)由计算节点仓库保留；硬件结果不能外推到其他设备。

<a id="2026-11-11-家庭音乐资料室后台整体改版"></a>

### 2026-11-11 家庭音乐资料室后台整体改版

该历史界面改版及后续调整见[管理体验记录](admin-experience.md)。原本机截图和私有运行记录不随仓库发布，本页不补写未重新核实的历史结果。
