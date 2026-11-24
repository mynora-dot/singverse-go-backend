# 共享容器 v5

每次导出生成一个 ZIP。导入时上传完整 ZIP，接收方用自己的本地或 WebDAV 来源绑定包内相对路径。

```text
index.json
records/000001.json
assets/<sha256>.jpg
```

## 清单和内容

`index.json` 声明 `format: "singverse-share-set"`、`version: 5` 和 `metadataSchema: "1.0"`。`id` 为快照 UUID，`collectionId` 为共享系列 UUID，`collectionRevision` 为十进制字符串。`baseId` 为空字符串时表示完整快照，否则指向接收方已应用的基线。`total` 为本次包内实际包含的媒体条目数。

`records` 和 `deletions` 分别按顺序列出 `records/000001.json` 和 `deletions/000001.json` 起的文件名。每个资料文件是一个 JSON 数组，单条记录包含 `rootId: "media"`、相对路径 `path`、可选媒体大小 `size`、`mediaId` 和完整的 `documents` 关联对象。重复出现的对象必须具有相同内容。删除建议使用 `{"advisoryOnly":true,"paths":[...]}`，接收方只显示提示。没有记录或删除建议时，对应列表为空。

封面、肖像及歌词原文保存在 `assets/`，文件名用原始字节的 SHA-256 和扩展名。同一摘要仅保留一份附件。扩展名 `.jpg`、`.png`、`.webp` 分别对应 JPEG、PNG、WebP，`.json`、`.txt`、`.srt`、`.vtt` 分别对应 `application/json`、`text/plain`、`application/x-subrip`、`text/vtt`。每条附件引用须指向包内完整原文。资料包的共享内容限于元数据、原始附件和相对路径。

`files` 列出除 `index.json` 以外的全部文件，并声明各文件展开后的 `size` 和 `sha256`。`contentDigest` 为移除该字段后对清单进行 JCS 规范化所得的 SHA-256。相同系列和修订必须使用相同摘要。ZIP 的文件顺序不影响语义。

## 读取与限制

ZIP 压缩及展开总量均限 8 GiB，最多 30,001 个文件、100,000 条媒体记录和 100,000 条删除建议。单个附件限 32 MiB，每份 JSON 文件限 16 MiB，单个资料数组限 5,000 条。校验器按文件分批读取并检查声明、路径、类型、引用及摘要。机器格式见 [JSON Schema](package-v5.schema.json)。

后台导入支持预览路径和直接入库。整包提交后推进基线，保留接收方的本机订正。原唱和伴奏继续使用 [Metadata Schema 1.0 的字段定义](fields.md)。已有 v2、v3 和 v4 资料包使用各自的格式解析。
