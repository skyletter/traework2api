# 上游来源与更新拉取手册（VENDOR）

目的：本仓库每个非自研行为都能回答"从哪来、对照哪个版本"；
上游更新时，能用固定流程把新代码拉下来评估、移植。

配套文档：`docs/PORTING.md`（行为级对照表 + 版本钉 + 决策记录）。
本文件是操作手册：来源清单、看什么文件、拉取命令、移植纪律。

## 一、来源清单

| 代号 | 仓库 | 许可证 | 在本仓库的作用 | 关键文件（上游） | 落点（本地） |
|---|---|---|---|---|---|
| `base` | Sliverkiss/traework2api | 无 LICENSE 文件（2026-09-12 核实） | 基座：整个 Go 服务最初从此 fork 链下来 | 全仓库 | 全仓库（经 connectedGraph 二次修改） |
| `cn` | autumnsentiment/Trae2api-cn | MIT | 签到 9074 机制的唯一来源：设备 ID 派生、轮换、退避、刷新重试、pin | `src/trae_client.py`（checkin 区约 340–560 行）、`src/auth.py`（约 870–960 行）、`src/main.py`（retry 循环、`_claim_checkin_throttled`） | `internal/upstream/headers.go`、`client.go`、`internal/pool/pool.go`、`internal/scheduler/scheduler.go` |
| `work` | smart-open/TraeWorkAssistant | MIT | 协议结论来源（只取结论，不取代码）：solo_agent-only 模型名单、退役模型名单、4001 机制、新 JWT 指纹头预警 | `CHANGELOG.md`（v3.2.5 起）、`src-tauri/src/api_server/models_sync.rs`、`mod.rs` | `internal/server/handler.go`（静态表）、`IsModelConfigMismatch` |
| `muskke` | muskke/trae-api-proxy | MIT（以仓库实际为准，取用前复核） | 4001 最小请求规则的第二佐证；Responses/Codex 路线与本服务无关 | `CHANGELOG.md`（v0.4.3/v0.4.4）、`README.md` | 仅佐证 4001 规则，无代码移植 |
| `jeff` | JeffHu0912/trae2api | MIT | 观察位：同为 SOLO Go 服务，暂无可移植内容 | — | 无 |

注意：
- `work` 是 Windows 桌面端（Tauri/Rust），**永远不要**直接移植它的
  平台代码，只取它实测出的协议结论，且涉及行为变更的结论
  （如 solo_agent）必须经 live 配对验证才落地。
- `cn` 是 Python，实现**必须用 Go 重写**，逐值对齐点只有一处：
  设备 ID 派生（sha256→mod 1e16→16 位零填充），验证测试见下。

## 二、日常检查：上游有没有更新（一行命令）

```bash
for r in autumnsentiment/Trae2api-cn muskke/trae-api-proxy \
         smart-open/TraeWorkAssistant JeffHu0912/trae2api \
         Sliverkiss/traework2api connectedGraph/trae2api-web; do
  printf '%s ' "$r"
  curl -fsSL --max-time 25 \
    "https://api.github.com/repos/$r/commits?per_page=1" |
    jq -r '.[0] | "\(.sha[0:10]) \(.commit.author.date) \(.commit.message | split("\n")[0] | .[0:60])"'
done
```

拿输出与 `PORTING.md` 文末"版本钉"对比：SHA 变了 → 进第三节。

## 三、拉取更新的标准流程

以下 `cn` 为例，其他来源把 remote 名和路径换掉即可。
（Python 参考实现**只读**，绝不进入发布集。）

```bash
# 0. 建工作区（与发布分支隔离；/tmp 重启会丢，大包放别处）
git remote add vendor-cn https://github.com/autumnsentiment/Trae2api-cn.git
git fetch vendor-cn

# 1. 看版本钉之后新增了什么（先看标题，再读 diff）
git log --oneline PORTING_PIN_CN..vendor-cn/main
# PORTING_PIN_CN 是记录在 PORTING.md 的旧 HEAD，如 c698b19

# 2. 只取相关 diff（不要全量 merge：语言不同，merge 无意义）
git diff PORTING_PIN_CN..vendor-cn/main -- src/trae_client.py src/auth.py src/main.py

# 3. 逐条过"三问"（任一不过即记入待评估，不移植）：
#    a) 属于我们的场景吗（headless Linux relay / SOLO CN）？
#    b) 有实测证据吗（测试/配对抓包，还是单方断言）？
#    c) 与 PORTING.md 现有分歧冲突吗？
```

移植纪律（与 PORTING.md 维护规则联动）：

1. 先写**失败测试**（含上游交叉向量，如设备 ID 派生值），再写实现。
2. 代码注释三行格式：`Source`（仓库/文件/行/commit/日期）、
   `Divergence`（故意差异+理由）、`Verified`（测试名）。
3. 同步更新 `PORTING.md`：对照表加行、版本钉前移、决策区记录取舍。
4. `git diff -w` 必须干净（功能提交不夹带空白改动）。
5. 全量 `go vet` + `go test ./...` 通过才合入。

## 四、各来源的"看什么"（watch 清单）

- `cn`：`src/trae_client.py` 的 checkin 区（device/claim/status）、
  `src/main.py` 的 `_checkin_auto_retry_cycle` 与 `_claim_checkin_throttled`、
  `.env.example` 新增的 `TRAE_CHECKIN_*` 开关；
  不看它的 remote/raw/CLI/narration（另一条协议路线）。
- `work`：只看 `CHANGELOG.md` 含 `checkin`/`credit`/`model`/`function`/`401`
  的条目；Rust 代码仅在结论不明时当第二证据。
- `muskke`：只看 `CHANGELOG.md` 的 relay 相关条目；Global 专属改动跳过。
- `base`/`connectedGraph`：已停更，每季度抽查一次即可。
- `jeff`：有实质提交（非 docs）时再评估。

## 五、验证锚点（移植后必须通过的对应关系）

| 断言 | 上游侧 | 本地测试 |
|---|---|---|
| 设备 ID 算法一致 | `checkin_device_id_for("u1", gen=0)` | `TestCheckinDeviceIDStable`（向量 `u1→4302850041909017`，Python 交叉计算） |
| 退避序列一致 | 60→120→240→480s 封顶 | `TestCheckinRetryBackoffGrowsAndCaps` |
| 代数不清零 | `78cd9d5953` diff 原文 | `TestRunCheckinKeepsGenerationWhenCheckedIn` |
| 限流不立即重发 | `_claim_checkin_throttled` 注释 | `TestRunCheckinRotatesDeviceOnRateLimit`（claim 恰一次） |
| 9095 不算已签 | `trae_client.py:539-542` 注释警告 | `TestIsAlready`（9095 用例） |

## 六、许可证红线

- `base` 无 license 文件：只做事实派生声明（见 LICENSE），若上游补 license
  立即同步保留。
- `cn`/`work`/`muskke` 为 MIT：算法用 Go 重写 + 注释出处 +
  PORTING.md 登记，即满足合规；若整段翻译代码，必须保留原版权行。
- 第三方 Python 参考文件**永不进仓库**（`.gitignore` 未覆盖 `*.py`
  是故意的——靠流程保证，见发布审计）。
