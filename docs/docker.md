# Docker 部署

当前仅提供[源码构建和预构建镜像](../infra/docker/README.md)两条路线，业务由 Go 内置完整前端，compute 按需部署。

- [GPU 与独立计算节点](../infra/docker/GPU.md)
- [备份、升级和旧部署切换](../infra/docker/OPERATIONS.md)
- [GHCR、Docker Hub发布配置与 Action 项目空间参数](../infra/docker/PUBLISHING.md)

发布 Action 通过 `ghcr_namespace`、`dockerhub_namespace`、`acr_registry`、`acr_namespace` 选择三个仓库的目标空间；部署 `.env` 的 `IMAGE_PREFIX` 选择实际拉取地址。镜像命名空间与 Compose 项目名无关，切换仓库时保留原项目与数据卷。

旧源码部署包、离线镜像 ZIP、独立 Nginx 及 Java 回退配置已移除。历史验收记录只说明对应旧版本，不能作为新交付验证结果。
