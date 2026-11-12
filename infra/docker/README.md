# Docker 部署

本仓库交付内嵌前端和两端 APK 的 Go 业务镜像。所有命令从本仓库根执行，详见 [README](../../README.md)。

```sh
cp .env.example .env
# 按部署需要修改占位配置；已有 .env 不覆盖。
make docker-build docker-up
# 或使用已发布镜像：
make docker-pull docker-image-up
make docker-down
```

源码路线追加 compose.build.yaml，仅构建本仓库 Go 镜像，构建前会准备并校验客户端发行包。预构建镜像路线不包含 build。compute 由独立仓库构建；本仓库保留可选计算节点及硬件覆盖的组合部署配置，默认只启动 Go。

项目名 singverse-go-app、服务和命名卷保持原身份。默认业务端口仅绑定回环；手机使用时由用户按受限局域网设置可达地址。媒体原件只读，不为下载开放管理权限。

业务镜像版本由 SINGVERSE_VERSION 选择，计算节点独立使用 COMPUTE_VERSION。可选节点不加入主服务健康依赖。SQLite 数据与认证密钥卷须一起备份，禁止任意删除命名卷。

```sh
make docker-config-check docker-context-check docker-test
make go-container-check auth-docker-check
python3 infra/docker/check-bundled-releases.py --image singverse:local
```

硬件覆盖仍需真实设备验证。发布配置见 [PUBLISHING](PUBLISHING.md)，历史迁移注意事项见 [OPERATIONS](OPERATIONS.md)。
