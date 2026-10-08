# GoogleDrive Share

将 Google Drive 的公开分享文件或文件夹挂载到 AList，匿名浏览和下载。无需 Google
账号、OAuth、Refresh Token、API Key，也无需将分享保存到个人网盘。

## 配置

在 AList 管理后台添加存储，选择 **GoogleDrive Share**：

- **挂载路径**：例如 `/google-share`。
- **share_url**：完整的 Google Drive 分享链接，例如
  `https://drive.google.com/drive/folders/FOLDER_ID` 或
  `https://drive.google.com/file/d/FILE_ID/view`。
- 保留链接中的 `resourcekey` 参数（如果存在）。
- 单个文件分享会显示为挂载目录下的一个文件。

分享必须允许“知道链接的任何人”查看并下载。仅对指定账号、组织内部开放的分享仍需
Google 登录，无法通过这个匿名驱动访问。

这是只读驱动，支持子文件夹、普通上传文件、中文文件名和大文件下载。目录大小及时间
从公开网页获取；目录超过网页首批 50 项时，用嵌入式目录视图列出全部条目，并补齐文件
大小。文件链接会自动处理下载确认表单和临时匿名 Cookie。

下载通过 AList 代理，支持上游 Range 请求，可用于视频拖动、断点续传、WebDAV 和复制
到其他存储。部署 AList 的服务器需要能连接 `drive.google.com` 和
`drive.usercontent.google.com`，以及下载重定向涉及的 `*.googleusercontent.com`。
网络代理沿用 AList 的 HTTP transport / `HTTP_PROXY`、`HTTPS_PROXY` 环境变量设置。

## 边界

- Google 的下载配额、文件所有者禁用下载、需要登录等限制会返回错误。
- 当前支持普通上传文件（视频、PDF、ZIP、Office 文件等）。Google 原生 Docs、Sheets、
  Slides 和快捷方式需要额外的导出或目标解析流程，当前不提供下载。
- 目录和文件元数据依赖 Google 的公开网页结构；网页变更可能需要更新解析器。
- AList 的批量下载/复制可以逐个处理目录内容；本驱动不调用 Google 的整目录 ZIP 打包。

## 验证

离线回归：

```sh
go test ./drivers/google_drive_share
go test -race ./drivers/google_drive_share
```

真实分享验证默认关闭，不会在普通测试中访问 Google。按需指定自己的分享链接和文件
相对路径；单文件分享可省略 `GOOGLE_DRIVE_SHARE_TEST_PATH`：

```sh
GOOGLE_DRIVE_SHARE_TEST_URL='https://drive.google.com/drive/folders/FOLDER_ID' \
GOOGLE_DRIVE_SHARE_TEST_PATH='subfolder/example.mp4' \
go test ./drivers/google_drive_share -run TestLivePublicShare -v -count=1
```

测试只在内存中读取最多 4096 字节并校验 `206`、`Content-Range`、文件总大小。可用
`GOOGLE_DRIVE_SHARE_TEST_RANGE_START` 指定非零起点；设置
`GOOGLE_DRIVE_SHARE_TEST_FULL_DOWNLOAD=1` 可完整验证小于等于 1 MiB 的测试文件，内容
直接丢弃，不写入磁盘。
