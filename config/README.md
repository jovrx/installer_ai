# 软件目录

`catalog.example.json` 提供两个固定版本的受维护模板：Git 2.51.0 EXE、Node.js 24.9.0 MSI。它们用于演示可复现安装，不表示当前最新版本。使用前根据需求维护版本、官方下载地址及可信摘要。

2026-10-11 开发时通过以下官方来源取得摘要，未在开发机器执行安装：

- Git：GitHub 官方 git-for-windows/git 的 `v2.51.0.windows.1` release API 中 `Git-2.51.0-64-bit.exe` 的 SHA-256 digest，大小 64701312 字节。
- Node.js：`https://nodejs.org/dist/v24.9.0/SHASUMS256.txt` 中 `node-v24.9.0-x64.msi` 条目。

size_bytes 是下载安装文件的大小上限。目录不允许任意命令或未知 EXE 参数；MSI、Git EXE 和限定 winget 走程序维护的执行模板。软件安装必须在会话范围内，并由 Web 操作员批准。目录修改不会扩大已经建立的 B 会话授权；须重新创建会话。
