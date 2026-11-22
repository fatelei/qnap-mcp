# qnap-mcp

用 Go 封装 QNAP QTS 的 File Station Web API，作为 [MCP](https://modelcontextprotocol.io/) 服务通过 stdio 暴露给 WorkBuddy（或其它 MCP 客户端），让 AI 直接浏览、搜索、上传、下载和管理 NAS 上的文件。

基于官方 `QNAP QTS File Station API v0.9/v5`：`authLogin.cgi` 登录换取 sid，`cgi-bin/filemanager/utilRequest.cgi` 执行文件操作。

## 功能（MCP Tools）

| 工具 | 说明 |
|---|---|
| `qnap_test_connection` | 测试连接与认证，返回共享文件夹数量 |
| `qnap_list_shares` | 列出所有共享文件夹 |
| `qnap_list_folders` | 列出指定路径下的子文件夹树 |
| `qnap_list_files` | 列出文件夹内容，支持分页/排序/隐藏文件 |
| `qnap_stat` | 获取文件/文件夹的详细信息（大小、权限、时间等） |
| `qnap_search` | 按关键字搜索文件/文件夹 |
| `qnap_get_file_size` | 统计多个文件/文件夹的总大小 |
| `qnap_create_folder` | 新建文件夹 |
| `qnap_rename` | 重命名文件/文件夹 |
| `qnap_copy` | 复制文件/文件夹 |
| `qnap_move` | 移动文件/文件夹 |
| `qnap_delete` | 删除文件/文件夹（标记为 destructive） |
| `qnap_download` | 下载文件/文件夹到本地路径 |
| `qnap_upload` | 上传本地单个文件到 NAS |

## 构建

需要 Go 1.21+（本项目在 Go 1.27 下验证）。

```bash
cd qnap-mcp
make build          # 生成 ./qnap-mcp 可执行文件
# 或
go build -o qnap-mcp .
```

## 配置

通过环境变量传入（不要在 MCP 配置文件里硬编码明文密码时请留意文件权限）：

| 变量 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `QNAP_HOST` | 是 | - | NAS 地址，可带端口：`192.168.1.10:8080` 或 `https://nas.example.com` |
| `QNAP_USER` | 是（除非设置 `QNAP_SID`） | - | QTS 用户名 |
| `QNAP_PASSWORD` | 是（除非设置 `QNAP_SID`） | - | QTS 密码（会自动 base64 编码） |
| `QNAP_SCHEME` | 否 | `https` | `QNAP_HOST` 不含协议时使用；`http` 且未指定端口时默认 8080 |
| `QNAP_INSECURE_TLS` | 否 | `true` | 是否跳过 TLS 证书校验（NAS 自签证书通常为 true） |
| `QNAP_TIMEOUT` | 否 | `30` | HTTP 请求超时（秒） |
| `QNAP_SID` | 否 | - | 复用已有 sid，设置后跳过 `QNAP_USER`/`QNAP_PASSWORD` 登录 |

如果账户开启了两步验证，请在 QTS 中生成应用专用密码并通过 `QNAP_PASSWORD` 传入。

### 本地连接自检

```bash
export QNAP_HOST="192.168.1.10:8080"
export QNAP_USER="admin"
export QNAP_PASSWORD="your-password"

./qnap-mcp --check
# 输出：连接成功，共 N 个共享文件夹： ...
```

## 在 WorkBuddy 中注册

WorkBuddy 的用户级 MCP 配置文件位于 `~/.workbuddy/mcp.json`。在 `mcpServers` 中加入：

```json
{
  "mcpServers": {
    "qnap": {
      "command": "/绝对路径/qnap-mcp",
      "args": [],
      "env": {
        "QNAP_HOST": "192.168.1.10:8080",
        "QNAP_USER": "admin",
        "QNAP_PASSWORD": "your-password",
        "QNAP_INSECURE_TLS": "true"
      }
    }
  }
}
```

保存后，在 WorkBuddy 设置中信任/启用该服务（新版 WorkBuddy 需要在连接器页面点击“信任”）。之后对话中即可使用 `qnap_*` 工具，例如：

- “列出我 NAS 上的共享文件夹”
- “在 /Public 下创建一个叫 Reports 的文件夹”
- “搜索 /Public 下包含 invoice 的文件”
- “把 /Public/Photos/IMG_001.jpg 下载到本地 ~/Downloads”

### 其它 MCP 客户端

Claude Desktop / Cline 等客户端同样使用 stdio 传输，配置格式类似：

```json
{
  "mcpServers": {
    "qnap": {
      "command": "/绝对路径/qnap-mcp",
      "env": { "QNAP_HOST": "...", "QNAP_USER": "...", "QNAP_PASSWORD": "..." }
    }
  }
}
```

## 实现细节

- **认证**：`GET /cgi-bin/authLogin.cgi?user=...&pwd=<base64>&serviceKey=1&service=1`，解析 XML 中的 `authSid`。
- **会话**：sid 惰性获取并在进程内缓存；遇到 `status=4`（权限/会话失效）自动重新登录并重试一次。
- **路径**：所有 NAS 路径自动补前导 `/`（例如 `Public` → `/Public`）。
- **多文件**：delete/stat/copy/move/get_file_size/download 使用重复的 query key（如 `file_name=a&file_name=b`）。
- **上传**：multipart/form-data，字段名 `file`，附带 `type=standard`、`dest_path`、`overwrite`、`progress`。
- **响应**：QTS 偶尔在 JSON 前输出一行 `Content-type` 头，客户端会自动剥离。
- **日志**：MCP 协议走 stdout，所有诊断信息走 stderr。

## 安全提示

- 服务以 NAS 账户的完整权限运行，`qnap_delete` 标记为 destructive；请为应用使用权限最小化的专用账户。
- `QNAP_INSECURE_TLS=true` 会跳过证书校验，仅建议在可信局域网内使用；公网环境请使用有效证书并设为 `false`。
- MCP 配置文件中的密码为明文，请控制文件权限（如 `chmod 600 ~/.workbuddy/mcp.json`）。

## 相关文档

- QNAP File Station HTTP API v5: https://download.qnap.com/dev/QNAP_QTS_File_Station_API_v5.pdf
- QNAP File Station API v0.9: https://www.qnap.ru/dev/download/QNAP_QTS_File_Station_API_v0.9.pdf
- MCP Go SDK: https://github.com/mark3labs/mcp-go
