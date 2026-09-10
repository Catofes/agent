# 课堂 Agent 平台

一个面向公开课的极简 Agent 平台：Go 单二进制、SQLite 单文件、学生/教师/大屏三个内嵌页面。平台提供学号登录、Soul 设计、多 Skill 目录与按需加载、教师控制的基础/物理/数学预设 Skill、多 Tab 对话与删除、长期 Memory、受控网页访问与网上搜索、Python 执行、行动记录、教师状态墙、课堂能力控制、全班锁定、场次隔离和投屏。

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
# 可选：配置后教师可以在课堂中切换到 Qwen，无需重启服务
QWEN_API_KEY='替换为百炼 Key'
QWEN_BASE_URL='https://dashscope.aliyuncs.com/compatible-mode/v1'
QWEN_MODEL='qwen3.8-flash'
QWEN_REASONING_EFFORT='low'
ADMIN_PASSWORD='替换为教师口令'
ANONYMOUS_HMAC_KEY='替换为独立的长随机字符串'
WEB_SEARCH_PROVIDER='disabled' # disabled、zhipu 或 deepseek
# 使用智谱本地工具链时再配置：
ZHIPU_SEARCH_API_KEY='替换为智谱 API Key'
ZHIPU_SEARCH_ENGINE='search_std'
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
make release
make smoke
```

`make smoke` 会在临时目录中启动内置 fake LLM，并行模拟 50 名学生的登录、保存设计、工具对话、课堂锁定和 SSE 断线重连，最后输出 P50/P95、错误数、模型并发、goroutine 和内存指标。小规模真实 API 验证使用：

```bash
DEEPSEEK_API_KEY='...' make smoke-real
```

`smoke-real` 默认只运行 5 名学生，会产生真实模型费用；可直接运行 `go run ./cmd/smoke -h` 查看并发数、模型和超时等参数。

`make release` 默认生成 `build/classroom-agent-linux-amd64` 及对应 `.sha256` 校验文件。可通过 `TARGET_OS` 和 `TARGET_ARCH` 改变目标，例如 `make release TARGET_OS=linux TARGET_ARCH=arm64`。

发布产物为 `build/classroom-agent`。前端和分类预设 SKILL.md 已嵌入二进制；运行时仍需提供名单路径和 SQLite 可写目录：

```bash
./build/classroom-agent \
  -listen=:8080 \
  -db=data/app.db \
  -students=data/students.csv \
  -cookie-secure=false
```

首次部署可以复制 `data/students.example.csv` 为实际名单，修改内容后再启动。

所有参数都有对应的大写下划线环境变量。例如 `-llm-concurrency` 对应 `LLM_CONCURRENCY`。完整配置项参见 `config.example.yaml` 或执行：

```bash
./build/classroom-agent -h
```

`WEB_SEARCH_PROVIDER` 决定首次启动或旧数据库升级后的联网搜索初始值：

- `disabled`：学生 Agent 不获得 `web_search`。
- `zhipu`：使用智谱 Web Search API，本地执行并保留搜索参数、结果、缓存和每轮最多 2 次的限制；需要 `ZHIPU_SEARCH_API_KEY`，可通过 `ZHIPU_SEARCH_ENGINE` 选择搜索档位。
- `deepseek`：使用现有 `DEEPSEEK_API_KEY` 和 Responses API 的服务端 `web_search`；搜索开始/完成仍显示为课堂行动，但查询改写、网页读取和服务端自动续跑由 DeepSeek 托管，本地无法逐次限制。

两种模式都沿用教师的课堂 Tool 开关和学生装备状态。`web_fetch` 仍是独立的本地网页读取工具；若不希望课堂服务器直接访问搜索结果网址，应由教师关闭它。

教师端“课堂能力”可以在服务启动后切换主模型和搜索服务，选择会随课堂场次保存在 SQLite 中，新回合立即生效，不需要重启。页面只显示服务器已通过环境变量配置好的服务，不会显示 API Key、Base URL 等秘密。DeepSeek 托管搜索只能与 DeepSeek 模型组合；切换到 Qwen 时应同时选择智谱搜索或关闭搜索。已有场次会保留上次保存的选择，`WEB_SEARCH_PROVIDER` 不会在普通重启时覆盖它。

## Python Runner（可选）

Python 能力默认不注册，不影响原有课堂功能。临时公开课推荐把 Runner 部署在一台专用内网 Docker 主机：Runner 自己运行在容器中、持有该主机的 Docker socket，再为每次运行创建无网络的一次性 Python 兄弟容器。课堂 Agent 只访问 Runner HTTP API，不挂载 Docker socket。

第一次部署时，进入 `deploy/`，复制环境变量示例并把其中的占位值替换成与课堂 Agent 完全相同的 `RUNNER_TOKEN`：

```bash
cd deploy
cp .env.example .env
# 编辑 .env；文件中只需配置一行 RUNNER_TOKEN=...
docker compose up -d
curl http://127.0.0.1:8090/healthz
```

之后在 `deploy/` 中直接运行 `docker compose up -d` 即可；Compose 每次都会从 GHCR 拉取最新的 `ghcr.io/catofes/agent-python-runner:latest` 和 `ghcr.io/catofes/agent-python-runtime:latest`，部署机不再编译 Go Runner，也不再安装 Python 科学计算包。Python Runtime 当前固定包含 `numpy`、`pandas`、`matplotlib` 和 `openpyxl`，发布工作流会为 Runner 和 Runtime 同时生成 `linux/amd64` 与 `linux/arm64` 镜像。

正式部署建议在 `deploy/.env` 里额外设置 `PYTHON_RUNNER_IMAGE` 和 `PYTHON_RUNTIME_IMAGE`，将两者锁定到与 Agent 相同的版本标签或审核后的 digest；未设置时默认使用 `latest`。手工更新可执行 `docker compose pull && docker compose up -d`。

`deploy/.env` 已加入 `.gitignore`，不会提交；仓库只保留不含真实密钥的 `deploy/.env.example`。不要为两台机器分别生成 Token：Runner 和课堂 Agent 的 `RUNNER_TOKEN` 必须逐字一致，否则执行请求会返回 `401 Unauthorized`。

将同一个 Token 安全地配置到课堂 Agent 机器，并重启 Agent：

```dotenv
RUNNER_URL='http://10.16.100.20:8090'
RUNNER_TOKEN='与 deploy/.env 完全相同的值'
RUNNER_TIMEOUT='20s'
MAX_PYTHON_CODE_CHARS='12000'
MAX_ARTIFACT_BYTES='10485760'
```

老师还需在课堂能力中开放“Python 执行”，学生才会看到实验台并可为自己的 Agent 装备该工具。学生手动运行不经过 LLM；上传文件与运行产物留在 Runner 的 `/var/lib/classroom-runner`，Agent 的 SQLite 只保存归属及摘要，下载始终经过 Agent 鉴权代理。产物默认保留 24 小时。

Runner 默认允许 16 个 Python 容器同时执行，并允许另外 64 个请求在有界队列中等待；可通过 `RUNNER_CONCURRENCY` 和 `RUNNER_QUEUE_CAPACITY` 调整。排队请求仍受 Agent 的 `RUNNER_TIMEOUT` 总超时约束，默认 20 秒。队列满时 Runner 返回 `429 RUNNER_BUSY`，不会无限堆积请求；`/healthz` 会报告当前 `running`、`queued`、`concurrency` 和 `queue_capacity`。

这套 Docker-socket 方案意味着 Runner 事实上拥有执行机的 root 级控制权，因此执行机必须专用、可重装，不得与 Agent、数据库或其他重要服务混部。防火墙只允许 Agent IP 访问 8090；不要把 Runner 暴露到公网。正式部署应通过 `PYTHON_RUNTIME_IMAGE` 固化审核后的镜像 digest，并监控 `/var/lib/classroom-runner` 磁盘占用。当前 MVP 有单次文件、文件数、输出、CPU、内存、PID、并发和时间限制，但尚未实现每生配额与全盘高水位熔断。

学生单条消息默认最多 12000 个 Unicode 字符，可通过 `MAX_INPUT_CHARS` 调整。输入框支持多行编辑：`Enter` 换行，`Ctrl+Enter`（macOS 为 `⌘+Enter`）发送。当前对话最多向模型组装 500 条已存消息；模型最终可接受的总上下文仍由所选模型决定。

单次模型调用默认允许 180 秒，可通过 `LLM_TIMEOUT` 调整，例如 `LLM_TIMEOUT=180s`。反向代理的读取超时必须更长；仓库提供的 Nginx 示例使用 300 秒。仅调大应用超时无法绕过外层代理的 60 秒限制。

单次模型回答默认最多保留 48000 个 Unicode 字符，可通过 `MAX_OUTPUT_CHARS` 调整。平台保留这一较高的安全上限，以避免异常生成无限占用课堂并发和额度。

## Docker 与 GitHub 发布

本地构建镜像：

```bash
docker build --build-arg VERSION="$(git rev-parse --short HEAD)" -t classroom-agent:local .
```

运行镜像基于 Alpine，并以非 root 用户运行；配置仍通过环境变量注入，SQLite、学生名单和备份统一放在 `/data`。请先准备一个包含 `students.csv` 的数据目录：

```bash
docker run --rm \
  --env-file .env \
  -p 8080:8080 \
  -v "$(pwd)/data:/data" \
  ghcr.io/catofes/agent:latest
```

镜像不包含 `.env`、真实学生名单、SQLite 数据库、日志或 Git 历史。直接运行镜像而不挂载 `/data` 时会使用内置的示例名单，容器删除后数据不会保留。

推送 `v*` 标签会触发 GitHub Release：先对完整 Git 历史执行 Gitleaks 密钥扫描，再运行测试，生成 amd64/arm64 二进制与校验文件，并发布 Agent、Python Runner 与 Python Runtime 三个 GHCR 多架构镜像。三个镜像使用同一版本标签、提交 SHA 标签和 `latest`；任一密钥扫描或测试步骤失败都不会创建 Release 或推送镜像。

## 生产部署

- 用 HTTPS 反向代理发布服务，并设置 `COOKIE_SECURE=true`。
- 对 `/api/chat`、`/api/events`、`/api/teacher/wall`、`/api/screen/events` 关闭代理缓冲，延长读取超时。
- API Key、教师口令和匿名 HMAC 密钥只通过服务端环境变量或受保护的启动参数注入。
- 定期复制 `data/app.db` 备份；SQLite 使用 WAL，在线备份时应使用 SQLite 备份工具或在停服后复制数据库及其 WAL 文件。
- 每次公开课前通过教师端新建场次。新场次会复制名单，但不会复制设计、消息、Memory 开关/条目和用量。

Nginx HTTPS 配置和流式检查步骤见 [HTTPS 反向代理部署](doc/HTTPS反向代理部署.md)。

## 当前边界

Memory 对每个学生仍默认关闭，老师可以按场次选择关闭、确认后记忆或自然记忆，并分别控制本场 Skill 和 Tool。关闭 Skill 后，学生端隐藏 Skill 编辑入口，已有 Skill 保留但不可修改、不会进入模型上下文或投屏。Memory 只在同一课堂场次内跨对话生效；除回合开始时的相关筛选外，任务或已加载 Skill 还可以通过受限的 `recall_memory` 补充召回当前学生已确认的事实。MVP 边界见 [Memory MVP 设计](doc/MemoryMVP设计.md)，演进状态见 [Memory V2 设计与实施计划](doc/MemoryV2设计与实施计划.md)。v0.3 已提供受控联网链路：智谱模式执行本地 `web_search → web_fetch`，DeepSeek 模式由 Responses API 托管搜索；两者都需要教师按场次显式开放、学生自行装备。智谱搜索与本地网页读取每轮分别最多调用 2 次；DeepSeek 服务端内部的搜索和网页读取轮次无法由本地逐次限制。zip 导出、随机点名、优秀池、绘图和多智能体演示仍在 [todo.md](todo.md) 的后续版本清单中。
