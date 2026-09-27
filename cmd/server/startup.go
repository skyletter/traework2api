// startup.go 启动运行报告：刷新账号签到/积分后，汇总输出运行信息。
package main

import (
	"fmt"
	"log"
	"strings"

	"trae2api-web/internal/pool"
	"trae2api-web/internal/scheduler"
)

// printStartupReport 输出运行报告（在启动补签到完成后调用）。
func printStartupReport(cfg *Config, p *pool.Pool, outcomes []scheduler.CheckinOutcome) {
	byUID := make(map[string]scheduler.CheckinOutcome, len(outcomes))
	for _, o := range outcomes {
		byUID[o.UID] = o
	}

	sts := p.List()
	var total int64
	for _, st := range sts {
		total += st.Credits
	}
	cb := "回调 127.0.0.1:" + cfg.CallbackPort
	if cfg.CallbackPort == "" || cfg.CallbackPort == "0" {
		cb = "回调已关闭"
	}

	var b strings.Builder
	b.WriteString("======================= 运行报告 =======================\n")
	fmt.Fprintf(&b, "版本     : %s\n", shortVer(version))
	fmt.Fprintf(&b, "监听     : %s    %s\n", cfg.Listen, cb)
	fmt.Fprintf(&b, "凭证目录 : %s\n", cfg.AuthDir)
	fmt.Fprintf(&b, "状态文件 : %s\n", cfg.StateFile)
	fmt.Fprintf(&b, "定时任务 : 签到每日 %02d:00 | token 刷新 %s\n", cfg.Schedule.CheckinHour, fmtHours(cfg.Schedule.RefreshHours))
	fmt.Fprintf(&b, "账号池   : %d 个账号 | 合计积分 %d\n", len(sts), total)
	if len(sts) == 0 {
		b.WriteString("  （空）在 NAS 终端执行：docker exec -it <容器名> /app/add-account.sh\n")
	}
	for i, st := range sts {
		name := st.Nickname
		if name == "" {
			name = "未命名账号"
		}
		fmt.Fprintf(&b, "  [%d] %s (uid=%s)\n", i+1, name, st.UID)
		fmt.Fprintf(&b, "      积分 %d | 签到 %s | 状态 %s\n", st.Credits, checkinText(byUID[st.UID]), healthText(st))
	}
	b.WriteString("==========================================================")
	log.Printf("%s", b.String())
}

// shortVer 截短构建版本号（CI 注入完整 sha 时取前 7 位）。
func shortVer(v string) string {
	if v == "" {
		return "dev"
	}
	if len(v) > 7 {
		return v[:7]
	}
	return v
}

// fmtHours 把小时列表格式化为 "03:00,15:00"。
func fmtHours(hs []int) string {
	parts := make([]string, 0, len(hs))
	for _, h := range hs {
		parts = append(parts, fmt.Sprintf("%02d:00", h))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

// clip 单行化并按字符数截断（超长以 … 结尾），用于报告展示。
func clip(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// checkinText 把签到结果转成可读文案。
func checkinText(o scheduler.CheckinOutcome) string {
	switch o.Result {
	case "ok":
		return "签到成功"
	case "already":
		return "今日已签到"
	case "not_enabled":
		return "今日未开放"
	case "rate_limited":
		if o.Detail != "" {
			return "限流，稍后自动重试（" + clip(o.Detail, 70) + "）"
		}
		return "限流，稍后自动重试"
	case "error":
		if o.Detail != "" {
			return "失败（" + clip(o.Detail, 70) + "）"
		}
		return "失败"
	case "skipped":
		if o.Detail != "" {
			return "跳过（" + o.Detail + "）"
		}
		return "跳过"
	default:
		return "未查询"
	}
}

// healthText 汇总账号在池中的状态。
func healthText(st pool.Status) string {
	switch {
	case st.Disabled:
		return "已禁用（需重新登录）"
	case !st.Enabled:
		return "已停用（面板软开关）"
	case st.Cooling:
		return "冷却中（至 " + st.Until.Local().Format("01-02 15:04") + "）"
	default:
		return "可用"
	}
}
