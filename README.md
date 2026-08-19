# traework2api

TRAE Work (SOLO CN) 的 OpenAI 兼容反向代理。把 TRAE SOLO 免费对话通道
（`llm_utils_chat` + `function=solo_work_lite`）包装成标准的
`/v1/chat/completions` + `/v1/models` 接口，支持多账号轮转、自动签到、token 自动刷新。

纯 Go 标准库，零第三方依赖。

> **本仓库 = 上游 [Sliverkiss/traework2api](https://github.com/Sliverkiss/traework2api) + 以下增强**（相对上游的额外 commit 均在本仓库 history 中，可逐个 diff 查看）。

## 本仓库相对上游的增强

| # | 特性 | 说明 |
|---|---|---|
| 1 | **Web 管理面板** | `GET /admin` 查看全部账号实时额度（积分剩余/总量/已用/权益包数）与签到状态，60s 自动刷新，多账号并发拉取，零外部依赖（`go:embed`） |
| 2 | **Web 凭证管理（CRUD + 登录闭环）** | `/admin/api/accounts` 全套：列表 / 导入（回调链接或 JSON）/ 删除 / 启停软开关 / 改昵称 / 手动刷新 token / JSON 脱敏预览。**Web 登录闭环**：面板发起登录 → TRAE 登录 → 回调自动打到 `127.0.0.1:18080/authorize` → 换 token 原子落盘 → 池热加载（**登录后无需重启服务**）。写操作 Bearer 校验，token 严格脱敏 |
| 3 | **glm-5.3 解锁** | `IdeVersion 0.1.43 → 0.1.52`：实测旧版本请求 glm-5.3 上游报 `4001 param invalid`，新版本正常对话，模型列表自动更新 |
| 4 | **Windows / Git Bash 编码健壮性** | `login.sh`：python3 商店占位别名（exit 49）自动回退；凭证 UTF-8 原子写（防 GBK 写坏凭证）；回调昵称双重编码修复 |
| 5 | **研究笔记** | [`docs/RESEARCH.md`](docs/RESEARCH.md)：TRAE 各通道反代可行性、积分体系（ide_credits vs work_credits）、work 通道不可反代的原因、限流码速查 |

## 功能

- **OpenAI 兼容 API**：`POST /v1/chat/completions`（流式/非流式）、`GET /v1/models`
- **多账号池**：积分降序挑选，1005/429/401/5xx 自动冷却、禁用、轮转
- **自动签到**：每日定时签到 + 手动批量签到（`signin.sh`）
- **积分查询**：全账号/指定账号日报（`credit.sh`）
- **Token 自动刷新**：过期前 24h 预刷新，refreshToken 轮换落盘
- **登录闭环**：`login.sh` 自生成登录链接 → 浏览器登录 → 粘贴回调链接 → 换 token 落盘；或直接用 Web 面板的登录闭环

## 快速开始（Docker）

```bash
# 1. 准备凭证目录（放 trae-*.json）
mkdir -p auths data

# 2. 配置 API Key（Bearer 鉴权，key 只走 env，不落盘 git）
cp .env.example .env
# 编辑 .env，把 changeme 换成你自己的随机密钥

# 3. 启动
docker compose up -d --build

# 4. 验证
curl http://127.0.0.1:7864/healthz          # → ok
curl http://127.0.0.1:7864/v1/models         # → 模型列表
curl http://127.0.0.1:7864/status            # → 账号状态

# 5. 对话
curl -X POST http://127.0.0.1:7864/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${TW2A_API_KEY}" \
  -d '{"model":"glm-5.2","messages":[{"role":"user","content":"你好"}]}'
```

## Web 管理面板

服务启动后访问 `http://127.0.0.1:7864/admin`：

- **账号卡片**：昵称 / uid / 剩余积分（大字）/ 总量 / 已用 / 权益包数 / 签到状态徽章 / 冷却与禁用标记
- **账号管理**：启停开关、删除、改昵称、手动刷新 token、JSON 脱敏预览（token 只显示前缀+长度）
- **Web 登录**：面板一键发起 TRAE 登录（浏览器打开 → 手机号验证码 → 回调自动回传落盘，无需手动粘贴）
- **导入**：粘贴回调链接或 trae-*.json 内容直接导入
- 写操作需 `Authorization: Bearer <TW2A_API_KEY>`（面板顶部输入记忆在 localStorage）；`/admin` 页面本身无鉴权（本地面板），如需暴露到局域网建议加反向代理鉴权

TRAE 登录回调监听 `127.0.0.1:18080`（`config.json` 的 `callback_port`，设 `"0"` 可关，退化为手动粘贴模式）。Docker 部署无需 EXPOSE（回调仅本机）。

## 登录流程（CLI）

TRAE 登录页强制回调 `127.0.0.1`，但浏览器与服务器不需要同机：
`login.sh` 自生成登录链接，登录成功后你把地址栏回调链接粘回去即可。

```bash
# 1. 在服务器（或任意机器）运行 login.sh
./login.sh
#    → 打印登录链接（带 127.0.0.1 回调 + 新的 machine/device id）

# 2. 用浏览器打开链接登录（手机号/验证码）
#    登录成功后浏览器跳到打不开的 127.0.0.1 地址

# 3. 复制浏览器地址栏的完整回调链接，粘贴到 login.sh
#    → 解析 refreshToken/userInfo → ExchangeToken 换 token → GetUserInfo 拿 uid
#    → 落盘 auths/trae-{uid}.json → 自动签到 + 查积分
```

> Windows/Git Bash：`python3` 若指向商店占位别名会自动回退 `python`；凭证写盘为 UTF-8 原子写入，中断不会损坏已有凭证。

## 本机运行（非 Docker）

```bash
# 依赖 Go 1.22+
export TW2A_API_KEY=你的密钥
go build -o tw2api ./cmd/server
./tw2api          # 监听 :7864，auths/ 目录读取凭证
```

配置可选 `config.json`（参考 `config.example.json`），全部项可用 `TW2A_*` env 覆盖：
`TW2A_LISTEN` / `TW2A_AUTH_DIR` / `TW2A_STATE_FILE` / `TW2A_DEFAULT_MODEL` /
`TW2A_PLAN_CREDIT` / `TW2A_SOFT_RATE` / `TW2A_ERR_THRESHOLD` / `TW2A_ERR_COOLDOWN` /
`TW2A_CHECKIN_HOUR` / `TW2A_TIMEOUT_SECONDS` / `TW2A_CALLBACK_PORT`。
`TW2A_API_KEY` **只能**从 env 读。

## 运维

```bash
./signin.sh             # 批量签到（全账号，自动 refresh 过期 token）
./credit.sh             # 积分日报（美化）
./credit.sh -json       # 积分日报（JSON）
./credit.sh <uid>       # 指定账号
```

## 研究笔记

见 [`docs/RESEARCH.md`](docs/RESEARCH.md)：TRAE 反代全景、SOLO 通道机制、积分体系（ide_credits / work_credits）、work 通道不可反代的原因、限流码速查、Windows 编码坑。

## 目录结构

```
cmd/server/       HTTP 服务（config + main，含 18080 回调 server）
cmd/signin/       批量签到工具
cmd/credit/       积分查询工具
internal/auth/    auth 文件解析/原子写回
internal/upstream/ SOLO 上游客户端 + SSE 转换
internal/pool/    账号池（冷却/禁用/积分/软开关）
internal/scheduler/ 定时签到 + token 预刷新
internal/server/  OpenAI 兼容路由 + /admin 面板 + accounts CRUD + 登录闭环
docs/RESEARCH.md  研究笔记
login.sh / signin.sh / credit.sh  运维脚本
auths/            （gitignored）trae-*.json 凭证
data/             （gitignored）state.json 池状态
```

## 脱敏说明

- 任何真实 token/key 一律 `********` 或 env 引用，绝不落盘 git。
- `auths/`、`data/`、`config.json`、`.env`、`*.key`、`*.pem` 全部 gitignored。
- 面板列表与 JSON 预览严格脱敏（token 只返回前缀 + 长度），完整 token 绝不返回前端。
- 日志/状态输出只显示 UID/Nickname/积分，不打印 token。
