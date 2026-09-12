# 移植与分歧溯源（PORTING）

本文件是本仓库所有**跨项目移植行为**的唯一事实源。
每行记录：行为 → 上游出处（仓库/文件/行/commit/日期）→ 本地位置 →
故意差异与理由 → 验证测试。无出处的一律标 `Observed`（生产实测），
不伪造成移植。

维护规则（硬性）：
1. 上游有新提交时，先更新文末"版本钉"，把差异记入"待评估"，评审通过才移植。
2. 每个移植改动必须带代码三行注释（Source / Divergence / Verified），
   并在本文件加一行，否则该项不算完成。
3. 禁止"复刻/借鉴"裸字样：必须能回答"对照的是哪个版本"。

## 上游版本钉（评审基准 2026-09-12）

| 上游 | HEAD | 日期 | 状态 |
|---|---|---|---|
| Sliverkiss/traework2api（本项目基座） | `ea18d5c7` 起始…`207d0807` | 2026-08-02 | 停更；且**无 LICENSE 文件**（LICENSE 中的 derivation note 在上游补 license 后必须同步） |
| autumnsentiment/Trae2api-cn | `c698b19` | 2026-09-01 | 活跃；签到 9074 机制来源 |
| muskke/trae-api-proxy | v0.5.1 | 2026-09-05 | 活跃；Responses/Codex 路线与本服务无关 |
| smart-open/TraeWorkAssistant | v3.3.4 | 2026-09-10 | 活跃但仅 Windows 桌面端；只取协议结论 |
| JeffHu0912/trae2api | `ec20c08` | 2026-09-08 | 刚起步（4 提交），无可移植内容 |

## 行为对照表

| 行为 | 上游出处 | 本地位置 | 故意差异 + 理由 | 验证 |
|---|---|---|---|---|
| 签到设备 ID 派生（sha256→mod 1e16→16 位零填充，`#genN` 后缀） | Trae2api-cn `src/trae_client.py:443-466` @ `c698b19` (2026-09-01) | `internal/upstream/headers.go:CheckinDeviceID` | 身份取 UID（与 JWT `data.id` 同源），不解析 JWT；pin 由 `CheckinDevice` 处理 | `TestCheckinDeviceIDStable`（含 Python 交叉向量 `u1→4302850041909017`） |
| 9074 命中轮换设备代数 | Trae2api-cn `src/trae_client.py:421-440` @ `c698b19` | `internal/pool/pool.go:BumpCheckinGeneration`，`internal/scheduler/scheduler.go:checkinAccount` | 代数存 `state.json`（字段 `checkin_device_gen`，上游存 `accounts.json`），旧文件默认 0 | `TestCheckinGenerationRotateOnly`，`TestCheckinGenerationPersistsAcrossReload` |
| 代数不清零（只清退避） | Trae2api-cn @ `78cd9d5953`（diff 原文：generation survives retry-state reset） | `internal/scheduler/scheduler.go`（无 reset 路径） | 与上游一致；早期版本曾有 `ResetCheckinGeneration`，经取证删除 | `TestRunCheckinKeepsGenerationWhenCheckedIn` |
| 9074 退避（60→120→240→480s 封顶，wall-clock 持久化，intraday 重试） | Trae2api-cn @ `2403954` + @ `165ac6e` | `internal/pool/pool.go:NoteCheckinRateLimited`，`internal/scheduler/scheduler.go:RunCheckinRetries/RetryLoop`（15min tick，主函数接线） | 无日次数上限（跟随上游温和升级；审计曾建议 3–4 次/天，未采纳，理由见下） | `TestCheckinRetryBackoffGrowsAndCaps`，`TestCheckinRetryPersistsAcrossReload`，`TestRunCheckinRetriesDueAccount` |
| 退避不碰签到日期 | Trae2api-cn `src/auth.py:915-929` @ `2403954` | 同上（退避只记 `checkin_retry_after/count`） | 一致 | 同上 |
| status/claim 限流窗口分离 | Trae2api-cn @ `74dc310` | 部分：status-9074 同样进轮换分支（`checkinAccount`），但无独立 status 窗口（单账号场景不需要仪表盘解冻） | 简化；status-9074 换 ID 重查一次即等价 | `TestRunCheckinStatusRateLimitedRotates` |
| 认证失效刷新重试一次 | Trae2api-cn @ `87a510a`（1001/认证文案→刷新→重试一次） | `internal/scheduler/scheduler.go:statusWithRefresh` | 用无条件 `RefreshToken`（吊销 token 不会触发 `NeedsRefresh`）；HTTP 401 经 `ErrSessionDead` 识别，业务 1001 经文案识别 | `TestRunCheckinRefreshesRevokedTokenOnce` |
| 操作员 pin 设备 ID（pin 时禁轮换） | Trae2api-cn @ `b0bf9c2`（`TRAE_CHECKIN_DEVICE_ID[S_JSON]`） | `internal/upstream/headers.go:CheckinDevice/checkinPin` | 键只查 UID；`x-os/app-version` 默认不发（跟随上游默认关闭） | `TestCheckinDevicePin`，`TestCheckinDevicePinPerAccount`，`TestRunCheckinPinnedDeviceNeverRotates` |
| 签到极简请求头 | Trae2api-cn @ `b0bf9c2`（无 UA/region/身份头） | `internal/upstream/headers.go:CheckinHeaders` | brand/type 沿用本项目 SOLO 常量（`83DG`/`windows`），不抄对方 CN 值 | `TestCheckinStatusAndClaim`（断言头集合） |
| claim 解析业务码（0/9074/其他） | 自研（修 9004 误报 OK 的真 bug） | `internal/upstream/client.go:CheckinClaim` | — | `TestCheckinClaimSurfacesRateLimit`，`TestCheckinClaimSurfacesBusinessError` |
| 9095 永不算已签 | Trae2api-cn `src/trae_client.py:539-542`（注释警告） | `cmd/signin/main.go:isAlready`（9095 提前返回 false） | 上游返回 dict 由调用方判断；本服务在字符串判定层拦截 | `TestIsAlready`（9095 用例） |
| 4001 model-config-empty 不冷却账号 | TraeWorkAssistant `models_sync.rs` + muskke 最小请求规则（机制双源佐证） | `internal/upstream/client.go:IsModelConfigMismatch(Code)`，`internal/server/handler.go` 两处调用 | **不切换 `function`**：solo_agent 三模型说法为单源无配对实测断言，见下 | `TestIsModelConfigMismatch`，`TestChatModelConfigMismatchDoesNotCool` |
| 静态模型表刷新 | TraeWorkAssistant CHANGELOG v3.2.5（退役 5 个）＋ Trae2api-cn `c426eea`（新增） | `internal/server/handler.go:staticModels`（32→33） | 纯失败路径回退；动态拉取成功时零影响 | `TestModelsEndpoint` |
| 回调 `data` 字段兼容 | Observed：2026-08-31 生产回调实测（当前 SOLO 授权页把 token 放 `data`） | `internal/server/callback.go` | 非移植，无上游出处 | `TestParseCallbackDataRefreshToken` |
| 一键授权登录 UX | 自研 | `internal/server/admin.html` | 预开空白窗口防拦截＋复制备用链接，链接只存页面内存 | `TestAdminPageHasOneClickLoginControls`＋浏览器点击回归 |

## 显式决策（有争议项的结论）

1. **积分接口仍用登录 DeviceID**：`EntUsage` 未切派生 ID。假定 entitlement 是账号维度；首次 401 即复审（TraeWorkAssistant `b29bca3` 预警已记录）。
2. **无日重试次数上限**：跟随上游温和升级（60→480s）；审计的 3–4 次/天建议未采纳——cap 会错过当天窗口，而升级本身已保证不撞限流。
3. **CLI 读调度器代数**：`signin --state=<state.json>` 只读 `checkin_device_gen`，不写；无 state 时回退基线。
4. **`9004` 未解**：社区无任何讨论；本服务做到失败如实上报（不再误报 OK），等上游放行或次日自动签到。
5. **`solo_agent` 不切换**：待命 live 配对验证（同一模型 `solo_work_lite`→4001 且 `solo_agent`→成功），需单独批准（耗积分）后执行。

## 待评估（上游有、本地无）

- Trae2api-cn `/api/model-test` 按模型连通性探测：nice-to-have，未排期。
- muskke 模型可用性学习（usable/unavailable TTL）：nice-to-have，未排期。
- TraeWorkAssistant `/v1/messages`（Anthropic 兼容）：场景外，不做。
