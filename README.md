# 课堂 Agent 平台

一个面向公开课的极简 Agent 平台：Go 单二进制、SQLite 单文件、学生/教师/大屏三个内嵌页面。v0.1 提供学号登录、Agent 设计、计算器工具调用、行动记录、教师状态墙、全班锁定、场次隔离和投屏。

## 本地运行

需要 Go 1.24 或更高版本。先准备名单：

```csv
id,name
2101,张三
2102,李四
```

将名单保存到 `data/students.csv`，然后在项目根目录创建 `.env`：

```dotenv
DEEPSEEK_API_KEY='替换为真实 Key'
ADMIN_PASSWORD='替换为教师口令'
ANONYMOUS_HMAC_KEY='替换为独立的长随机字符串'
```

构建并运行二进制：

```bash
make run
```

`make run` 会自动加载 `.env`、构建 `build/classroom-agent`，然后以前台方式运行。开发时如需直接执行源码，可使用 `make dev`。通过 `ENV_FILE` 可以指定其他环境文件，例如 `make run ENV_FILE=.env.production`。

打开：

- 学生端：`http://localhost:8080/`
- 教师端：`http://localhost:8080/teacher`
- 大屏：`http://localhost:8080/screen`
- 健康检查：`http://localhost:8080/healthz`

启动时若数据库中没有活动场次，会自动创建“课堂 1”，并把 CSV 名单导入该场次。后续启动只新增学生或更新姓名，不删除已有课堂数据。

## 构建与检查

```bash
make test
make check
make build
```

发布产物为 `build/classroom-agent`。前端和三套 SKILL.md 模板已嵌入二进制；运行时仍需提供名单路径和 SQLite 可写目录：

```bash
./build/classroom-agent \
  -listen=:8080 \
  -db=data/app.db \
  -students=data/students.csv \
  -cookie-secure=false
```

所有参数都有对应的大写下划线环境变量。例如 `-llm-concurrency` 对应 `LLM_CONCURRENCY`。完整配置项参见 `config.example.yaml` 或执行：

```bash
./build/classroom-agent -h
```

## 生产部署

- 用 HTTPS 反向代理发布服务，并设置 `COOKIE_SECURE=true`。
- 对 `/api/chat`、`/api/events`、`/api/teacher/wall`、`/api/screen/events` 关闭代理缓冲，延长读取超时。
- API Key、教师口令和匿名 HMAC 密钥只通过服务端环境变量或受保护的启动参数注入。
- 定期复制 `data/app.db` 备份；SQLite 使用 WAL，在线备份时应使用 SQLite 备份工具或在停服后复制数据库及其 WAL 文件。
- 每次公开课前通过教师端新建场次。新场次会复制名单，但不会复制设计、消息和用量。

## 当前边界

当前实现是 v0.1 骨架。长期记忆、zip 导出、随机点名、优秀池、联网搜索、绘图和多智能体演示仍在 [todo.md](todo.md) 的后续版本清单中。
