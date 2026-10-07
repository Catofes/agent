# 课堂 Agent 平台

一个面向公开课的极简 Agent 平台：Go 单二进制、SQLite 单文件、学生/教师/大屏三个内嵌页面。平台提供学号登录、Soul 设计、多 Skill 目录与按需加载、教师控制的基础/物理/数学预设 Skill、多 Tab 对话与删除、长期 Memory、受控网页访问与网上搜索、Python 执行、行动记录、教师状态墙、课堂能力控制、全班锁定、场次隔离、投屏，以及可由教师开启的多 Agent 狼人杀环节。

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
DEEPSEEK_MODEL='deepseek-flash'
# 可选：配置后教师可以在课堂中切换到 Qwen，无需重启服务
QWEN_API_KEY='替换为百炼 Key'
QWEN_BASE_URL='https://dashscope.aliyuncs.com/compatible-mode/v1'
QWEN_MODEL='qwen3.8-flash'
QWEN_REASONING_EFFORT='low'
# 可选：配置后教师可切换到由阿里云百炼提供推理服务的 DeepSeek
BAILIAN_DEEPSEEK_API_KEY='替换为百炼 Key'
BAILIAN_DEEPSEEK_BASE_URL='https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1'
BAILIAN_DEEPSEEK_MODEL='deepseek-v4-flash-0731'
ADMIN_PASSWORD='替换为教师口令'
ANONYMOUS_HMAC_KEY='替换为独立的长随机字符串'
WEB_SEARCH_PROVIDER='disabled' # disabled、zhipu、deepseek、qwen 或 bailian-deepseek
# DeepSeek 搜索子请求通道；可在教师页按场次覆盖
DEEPSEEK_SEARCH_CHANNEL='anthropic' # anthropic 或 responses
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

## 多 Agent 狼人杀工作坊

教师在“课堂能力”中开启“狼人杀协作”，然后打开教师页的狼人杀控制台，从当前场次名单中选出 6 名学生开局。学生工作台会出现狼人杀入口；每名学生可分别填写通用策略、各角色的发言与投票、狼人夜间商议与击杀、预言家查验、女巫用药和遗言 Prompt。未修改的字段使用页面给出的默认策略。开局时会固定当前学生的 Soul、已启用的 Skill 和狼人杀 Prompt；对局中再修改的内容从下一局起生效。

身份随机分配为 2 狼人、2 平民、1 预言家、1 女巫。主持人按夜间商议与行动、白天发言与投票的顺序调用 6 名学生 Agent，公布结果并判断胜负。最高票平票时无人出局；为适配课堂时长，最多进行 4 轮，仍未分出胜负则记为平局。教师可以随时结束对局，关闭开关会立即中止正在进行的对局。其他学生可围观公开交流；私密身份和夜间信息仅提供给相关玩家与教师。教师可展开每一步，查看发送给 Agent 的完整 Prompt 和原始回复。对局及交流记录保存在当前场次的 SQLite 数据库中；服务重启会将未完成的对局标为中断。

该环节固定使用服务端的 `DEEPSEEK_API_KEY`、`DEEPSEEK_BASE_URL` 和 `DEEPSEEK_MODEL`，不需要学生填写密钥。使用真实模型会消耗各学生的课堂 token 额度；请在开局前配置有效的 DeepSeek Key。参考玩法：[alimamaTech/werewolf_1](https://huggingface.co/spaces/alimamaTech/werewolf_1)。课堂版在本项目内实现主持与数据隔离，不接入参考站点的账号、匹配和积分系统。

教师还可以在同一控制台发起全班比赛：当前名单里的每名学生 Agent 恰好参加 20 局，系统自动随机组成六人局，并行计算比赛。总参赛席位不能被 6 整除时，教师可允许少量系统 Agent 补位；补位 Agent 不列入积分榜。比赛开始时固定每名学生当时的 Soul、已启用 Skill 和狼人杀 Prompt，之后修改的设计不会改变这一届比赛。教师和学生均可查看实时进度、场次、胜负平和总积分；同分时胜局多者靠前。比赛页面不会逐场自动展示交流内容，教师可从“对局记录”点开任何已完成场次，暂停、播放或逐步查看 Soul、Skill、Prompt 和回复。狼人阵营获胜时，每名狼人 +6、其他玩家 −3；好人阵营获胜时，每名狼人 −6、其他玩家 +3；平局各 0 分。教师可暂停、继续比赛；服务重启后可在控制台继续未完成的比赛。已完成的场次和积分会保留。比赛专用请求采用快速推理，默认同时运行最多 32 场，可通过 `WEREWOLF_TOURNAMENT_WORKERS` 调整为 1～64；现场演示与普通单局沿用原来的推理设置。比赛不占用学生工作台的 token 额度，但会产生实际 DeepSeek API 调用；完成时间取决于模型响应和账号可用并发。

启动时若数据库中没有活动场次，会自动创建“课堂 1”。每次启动都会把 `students.csv` 作为当前场次的完整名单重新载入：不在新 CSV 中的学生会被停用、旧登录立即失效，也不会再出现在教师状态墙中；历史课堂数据仍保留在数据库中。

学生登录只需输入学号，不校验姓名；学号中的英文字母不区分大小写，登录后统一使用 `students.csv` 中记录的写法。输入学号 `test` 可直接登录一次性测试账号，服务端会为每次登录生成独立身份，多个听课老师可以同时使用而不会互相顶掉。`students.csv` 中以 `A` 或 `a` 开头的学号是固定账号；新建场次时，这些账号的工作数据和登录会延续到新场次，普通学生只延续名单并重置场次数据，测试账号不会延续。

`A01` 和 `A02` 是狼人杀演示与管理账号，需列在 `students.csv` 中，并在登录工作坊时输入现有教师口令。两者可切换学生端狼人杀入口、使用六人课堂控制台，并用自己的 Soul 与身份 Skill 对战五个系统 Agent。该权限仅覆盖狼人杀，不开放其他教师管理操作。演示画面可暂停、逐步前进、返回上一步或从头播放；暂停画面时后台仍继续生成这局的记录。音乐需要在页面手动开启；演示对局及其私密交流不会出现在学生端课堂对局中。

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
- `zhipu`：使用智谱 Web Search API，本地执行并保留搜索参数、结果和缓存；需要 `ZHIPU_SEARCH_API_KEY`，可通过 `ZHIPU_SEARCH_ENGINE` 选择搜索档位。
- `deepseek`：主对话仍使用 Chat Completions，`web_search` 作为普通工具另行调用 DeepSeek 搜索。`DEEPSEEK_SEARCH_CHANNEL=anthropic` 使用当前可用的 Anthropic Messages `web_search_20250305`；`responses` 保留 Responses `web_search` 通道，若服务端没有实际执行搜索会明确失败，不会把模型凭记忆生成的内容冒充搜索结果。
- `qwen`：千问模型改用百炼 Responses API 的内置 `web_search`，返回模型生成的检索词和来源 URL；需要同时配置 `QWEN_API_KEY`，并且只能与千问模型组合。`web_fetch` 仍是受教师策略控制的独立本地工具。
- `bailian-deepseek`：由阿里云百炼提供推理服务的 DeepSeek 模型改用百炼 Responses API，并启用其内置 `web_search` 与 `web_extractor`；需要同时配置 `BAILIAN_DEEPSEEK_API_KEY` 和带业务空间 ID 的 `BAILIAN_DEEPSEEK_BASE_URL`，只能与“DeepSeek（阿里云百炼）”模型组合。它与 `deepseek`（DeepSeek 官方接口）是两个独立服务。

所有搜索模式都沿用教师的课堂 Tool 开关和学生装备状态。每次对话默认最多执行 30 个工具，其中 `web_search`、`web_fetch` 各最多 10 次，`python_execute`、`recall_memory`、`save_memory` 各最多 5 次；模型迭代数仍由学生设计里的最大步数独立限制。`web_fetch` 是独立的本地网页读取工具；若不希望课堂服务器直接访问搜索结果网址，应由教师关闭它。

每名学生在每个课堂场次中的模型用量默认上限为 5000 万 token，可通过 `STUDENT_TOKEN_BUDGET` 调整。学生工作台显示本场剩余额度，并在每次回答后刷新；教师状态墙显示每名学生的累计用量，学生详情显示输入、输出、上限和占比，并可单独重置当前场次的计量。教师重置后学生端会自动同步，且不会删除对话、设计或 Memory。

教师端“课堂能力”可以在服务启动后切换主模型、搜索服务和 DeepSeek 搜索通道，选择会随课堂场次保存在 SQLite 中，新回合立即生效，不需要重启。保存后页面会核对服务端返回值和设置 revision；也可以不修改选项而重新保存当前设置，以确认请求确实到达后台。页面只显示服务器已配置好的服务，不会显示 API Key、Base URL 等秘密。“DeepSeek 官方搜索”“千问搜索”“百炼内置搜索”分别只能与同名模型服务组合；智谱搜索可与任一模型组合。已有场次会保留上次保存的选择，环境变量不会在普通重启时覆盖它。

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

之后在 `deploy/` 中直接运行 `docker compose up -d` 即可；Compose 每次都会从 GHCR 拉取最新的 `ghcr.io/catofes/agent-python-runner:latest` 和 `ghcr.io/catofes/agent-python-runtime:latest`，部署机不再编译 Go Runner，也不再安装 Python 科学计算包。Python Runtime 固定包含 Noto CJK 中文字体，以及 `numpy`、`pandas`、`matplotlib`、`openpyxl`、`scipy`、`sympy`、`seaborn`、`Pillow`、`scikit-learn`、`networkx` 和 `xlsxwriter`；镜像构建时会实际生成一张含中文标题、坐标轴和图例的 PNG 作为自检。发布工作流会为 Runner 和 Runtime 同时生成 `linux/amd64` 与 `linux/arm64` 镜像。

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

从 v0.7.0（或 v0.6.9）升级到 v0.8.0 时：

1. 停止 Agent，备份整个持久化数据目录（包含 SQLite 及可能存在的 `-wal`、`-shm` 文件）和现有环境配置，保留原有 `students.csv`。启动时仍会以 CSV 为准刷新名单。
2. 将 Agent 镜像固定为 `ghcr.io/catofes/agent:v0.8.0`，或替换为对应架构的 Release 二进制。保留数据挂载、教师口令、HMAC 密钥和现有配置。狼人杀无需新增容器、端口或 Python Runner；本次 Runner/Runtime 没有功能变更。
3. 核对 `DEEPSEEK_API_KEY`、`DEEPSEEK_BASE_URL` 和 `DEEPSEEK_MODEL`，官方接口建议显式设置 `DEEPSEEK_MODEL=deepseek-flash`。已有环境变量或启动参数会覆盖新默认值；`config.example.yaml` 仅供核对，不会自动加载。狼人杀固定使用这组配置，与教师为日常聊天选择的模型无关。
4. 启动后数据库自动从版本 19（或 17）升至 21，新增演示归属、管理账号授权和并行比赛索引，无需手工执行 SQL。已有课堂开关与记录保留，从 v0.6.9 升级时狼人杀默认关闭。检查 `/healthz`、学生登录、原有课堂数据，再由教师开启狼人杀并先试一局。使用 A01/A02 演示时需将账号加入原有 CSV，并用教师口令重新登录；旧会话不会自动获得管理权限。
5. 全班比赛每人 20 局，至少需要 6 名名单学生；例如 30 人共 100 局，默认最多并行 32 场，通过 `WEREWOLF_TOURNAMENT_WORKERS=1` 可恢复逐场运行（最大 64）。同场投票也会并行请求，请按 DeepSeek 账号额度调整并发。比赛会产生实际 API 费用，但不扣学生配额，也不计入现有学生用量墙，应从 DeepSeek 控制台监控费用。单次狼人杀模型调用限时 60 秒，失败最多再试一次，此限时不受 `LLM_TIMEOUT` 调整。

重启后单局和演示会标为中断，不能接续；全班比赛需教师手动继续，已结算场次与积分保留，未完成场次重新开局。仅运行一个 Agent 实例操作该数据库。若需回滚，停止新版，恢复升级前的完整数据备份后再运行旧版；v0.7.0 及更早版本不支持版本 21 的数据库，不能只换回旧镜像。恢复备份会丢失备份之后的新数据。

- 用 HTTPS 反向代理发布服务，并设置 `COOKIE_SECURE=true`。
- 对 `/api/chat`、`/api/events`、`/api/teacher/wall`、`/api/screen/events` 关闭代理缓冲，延长读取超时。
- API Key、教师口令和匿名 HMAC 密钥只通过服务端环境变量或受保护的启动参数注入。
- 定期复制 `data/app.db` 备份；SQLite 使用 WAL，在线备份时应使用 SQLite 备份工具或在停服后复制数据库及其 WAL 文件。
- 每次公开课前通过教师端新建场次。新场次会复制当前 CSV 名单；普通账号不会复制设计、消息、Memory 开关/条目和用量，`A` 或 `a` 开头的固定账号则完整延续这些数据和登录。

Nginx HTTPS 配置和流式检查步骤见 [HTTPS 反向代理部署](doc/HTTPS反向代理部署.md)。

## 当前边界

Memory 对每个学生仍默认关闭，老师可以按场次选择关闭、确认后记忆或自然记忆，并分别控制本场 Skill 和 Tool。关闭 Skill 后，学生端隐藏 Skill 编辑入口，已有 Skill 保留但不可修改、不会进入模型上下文或投屏。Memory 只在同一课堂场次内跨对话生效；除回合开始时的相关筛选外，任务或已加载 Skill 可以通过受限的 `recall_memory` 补充召回，也可以用 `save_memory` 立即提出新增或更新。确认后记忆模式写为候选，自然记忆模式直接生效；两者都重新检查课堂策略和学生开关。默认最多保存 100 条、每条 400 字，每轮实际注入模型的 Memory 仍限制在约 1200 tokens。MVP 边界见 [Memory MVP 设计](doc/MemoryMVP设计.md)，演进状态见 [Memory V2 设计与实施计划](doc/MemoryV2设计与实施计划.md)。联网搜索作为主对话工具执行：智谱直接返回结构化结果，DeepSeek 可由教师选择搜索通道，千问通过百炼 Responses API 执行内置搜索；搜索后仍可调用本地 `web_fetch` 核对原文。zip 导出、随机点名、优秀池、绘图和其他多智能体编排演示仍在 [todo.md](todo.md) 的后续版本清单中。
