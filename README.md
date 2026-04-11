# 企业微信会话存档服务

基于 [WeWorkFinanceSDK](https://github.com/NICEXAI/WeWorkFinanceSDK) 实现的企业微信会话存档服务，支持多企业配置、消息持久化到 MySQL、媒体文件本地存储。

## 功能特性

- **多企业支持** — YAML 配置多个企业微信，每个企业独立 Worker 并行运行
- **自动分表** — 通过 `storage_database` 配置数据库分表时间格式（如 `YYYY-MM-DD`），`storage_file` 配置媒体文件分目录时间格式（如 `YYYY-MM-DD-hh`），程序自动按时间窗口创建独立数据表和文件目录，到达新时间窗口时无需重启即自动切换，游标和未完成任务自动迁移到新表
- **断点续传** — 每个企业独立维护 seq 游标，进程重启或崩溃后从上次位置继续，不丢消息不重复
- **消息去重** — 基于 `(corp_name, msg_id)` 唯一索引，批量 INSERT ON CONFLICT DO NOTHING，重复拉取不会产生重复数据
- **媒体异步下载** — 消息入库与媒体下载解耦，独立协程并行下载，失败自动重试（最多 3 次），不阻塞消息拉取
- **批量入库** — 消息和媒体任务使用 CreateInBatches 批量写入，减少数据库压力
- **积压快速消耗** — 有新消息时立即拉取下一批，不等轮询间隔；无新消息时才进入等待周期
- **SDK v3 支持** — 集成企业微信 x86 服务器 SDK v3（20250205 版本），支持 25+ 种消息类型
- **生产级健壮性** — Worker panic 自动恢复、优雅退出 30 秒超时、启动时校验数据库连接和媒体目录、JSON 解析错误告警
- **独立诊断工具** — 附带 diagnose 命令行工具，快速验证 SDK 连接和消息拉取，不写数据库

## 环境要求

- **操作系统**: Linux x86_64（SDK 仅支持 Linux）
- **Go**: 1.21+（编译用，运行时不需要）
- **MySQL**: 5.7+
- **GCC**: CGO 编译需要

## 项目结构

```
├── main.go                      # 入口：配置加载、DB 初始化、Worker 启动、表前缀自动轮转
├── internal/
│   ├── config/config.go         # 配置加载与校验
│   ├── model/model.go           # 数据库模型（动态表名前缀，读写锁保护）
│   ├── worker/worker.go         # 消息轮询 Worker（解密、批量入库、媒体任务创建）
│   └── media/downloader.go      # 媒体文件下载器（分片下载、MD5 校验、失败重试）
├── cmd/diagnose/main.go         # 诊断工具
├── scripts/schema.sql           # 参考建表 SQL
├── config.example.yaml          # 配置模板
├── WeWorkFinanceSDK/            # Go SDK + C 动态库（v3 20250205）
│   └── lib/libWeWorkFinanceSdk_C.so
└── go.mod
```

## 编译

```bash
# 下载本项目
git clone git@github.com:KimiChen/wxworkChatData.git
cd wxworkChatData

# 编译主程序（已内嵌 rpath，运行时无需设置 LD_LIBRARY_PATH）
CGO_ENABLED=1 go build -o wxworkChatData .

# 编译诊断工具（可选）
CGO_ENABLED=1 go build -o diagnose ./cmd/diagnose/
```

## 配置

复制配置模板并填入实际参数：

```bash
cp config.example.yaml config.yaml
```

关键配置项：

| 配置 | 说明 |
|------|------|
| `storage_database` | 数据库表名前缀的时间格式，如 `YYYY-MM-DD`，程序按当前时间生成表名前缀 |
| `storage_file` | 媒体文件目录的时间格式，如 `YYYY-MM-DD-hh`，程序按当前时间生成文件目录名 |
| `mysql.dsn` | MySQL 连接地址 |
| `media.base_path` | 媒体文件存储目录（建议绝对路径） |
| `proxy.url` | HTTP/SOCKS5 代理（如需要） |
| `corps[].corp_id` | 企业 ID（管理端 → 我的企业 → 企业信息） |
| `corps[].corp_secret` | 会话存档 Secret（管理端 → 管理工具 → 会话内容存档） |
| `corps[].rsa_private_key` | RSA 私钥（与管理端配置的公钥对应） |
| `corps[].poll_interval` | 轮询间隔，建议 60 秒 |

### storage_database / storage_file 说明

这两个配置分别控制数据库表名前缀和媒体文件目录名。使用 `YYYY`/`MM`/`DD`/`hh`/`mm`/`ss` 占位符，程序启动时按当前时间替换，**运行中到达新时间窗口时自动切换，无需重启**。

**storage_database**（数据库表名前缀）:

| 配置值 | 启动时间 2026-04-11 15:30 | 表名示例 | 切换频率 |
|--------|--------------------------|---------|---------|
| `YYYY-MM-DD` | `2026-04-11` | `corp_2026-04-11_messages` | 每天 |
| `YYYY-MM-DD-hh` | `2026-04-11-15` | `corp_2026-04-11-15_messages` | 每小时 |

**storage_file**（媒体文件目录）:

| 配置值 | 启动时间 2026-04-11 15:30 | 目录示例 | 切换频率 |
|--------|--------------------------|---------|---------|
| `YYYY-MM-DD-hh` | `2026-04-11-15` | `media/corp/2026-04-11-15/` | 每小时 |
| `YYYY-MM-DD` | `2026-04-11` | `media/corp/2026-04-11/` | 每天 |

## 部署

### 1. 准备 MySQL 数据库

```sql
CREATE DATABASE wework_archive CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'wework'@'localhost' IDENTIFIED BY 'your_password';
GRANT ALL ON wework_archive.* TO 'wework'@'localhost';
```

程序启动时会自动建表（GORM AutoMigrate），无需手动执行 SQL。

### 2. 上传文件到服务器

```
/opt/wxworkChatData/
├── wxworkChatData               # 编译好的二进制
├── config.yaml                  # 配置文件
└── lib/
    └── libWeWorkFinanceSdk_C.so # C 动态库
```

```bash
scp wxworkChatData config.yaml server:/opt/wxworkChatData/
scp WeWorkFinanceSDK/lib/libWeWorkFinanceSdk_C.so server:/opt/wxworkChatData/lib/
```

### 3. 手动运行测试

二进制已内嵌 rpath（`$ORIGIN/lib`），`-config` 默认为二进制同目录下的 `config.yaml`，直接运行即可：

```bash
/opt/wxworkChatData/wxworkChatData
```

### 4. 诊断工具

用于快速验证 SDK 连接和消息拉取，不写数据库：

```bash
/opt/wxworkChatData/diagnose
```

### 5. 配置 systemd 服务

创建 `/etc/systemd/system/wxworkChatData.service`：

```ini
[Unit]
Description=WeChat Work Chat Archive Service
After=network.target mysqld.service

[Service]
Type=simple
WorkingDirectory=/opt/wxworkChatData
ExecStart=/opt/wxworkChatData/wxworkChatData
Restart=on-failure
RestartSec=10
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable wxworkChatData
systemctl start wxworkChatData
```

### 常用运维命令

```bash
systemctl status wxworkChatData      # 查看状态
systemctl restart wxworkChatData     # 重启
journalctl -u wxworkChatData -f      # 实时日志
journalctl -u wxworkChatData --since "1 hour ago"  # 最近1小时日志
```

## 数据库表说明

表名由 `storage_database` 配置动态生成，格式为 `corp_{时间前缀}_xxx`。例如 `storage_database: "YYYY-MM-DD"` 在 2026-04-11 运行时：

| 表 | 说明 |
|----|------|
| `corp_2026-04-11_messages` | 所有解密后的消息，`content` 字段为完整 JSON |
| `corp_2026-04-11_seq_cursors` | 每个企业的拉取进度（seq 游标） |
| `corp_2026-04-11_media_tasks` | 媒体文件下载队列，status: 0=待下载 1=下载中 2=完成 3=失败 |

到达次日时自动创建 `corp_2026-04-12_*` 新表，游标和未完成的媒体任务自动迁移。

## 媒体文件存储结构

```
{base_path}/{企业名}/{时间前缀}/{消息ID}.{扩展名}
```


## 支持的消息类型

所有 25+ 种消息类型都会存入 messages 表的 `content` JSON 字段。以下类型会额外下载媒体文件：

| 类型 | 说明 | 文件格式 |
|------|------|---------|
| `image` | 图片 | .jpg |
| `voice` | 语音消息 | .amr |
| `video` | 视频 | .mp4 |
| `emotion` | 表情 | .gif / .png |
| `file` | 文件 | 原始扩展名 |
| `meeting_voice_call` | 会议录音 | .amr |
| `voip_doc_share` | 通话中共享文件 | 原始扩展名 |

> 注：普通音视频通话（`voiptext`）仅记录通话时长等元数据，不含录音内容。被拒绝和未接听的通话不会产生存档记录。这些都是企业微信 API 本身的限制。

## 注意事项

- 企业微信会话存档有 **IP 白名单**限制，需在管理端添加服务器出口 IP
- 存档成员需在企业微信客户端 **同意存档** 后，其新消息才会被拉取
- 媒体文件的 `sdkfileid` 有效期为 **3 天**，需及时下载
- 每次拉取最多 1000 条消息，程序会自动分批拉取直到追上最新进度
- 每个企业创建两个独立 SDK Client（轮询和媒体下载各一个），保证线程安全
