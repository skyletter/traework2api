// signin 一次性批量签到工具：遍历 ./auths/trae-*.json 全部账号，
// 自动 RefreshToken（过期时），逐个签到，顺手查积分。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"trae2api-web/internal/auth"
	"trae2api-web/internal/pool"
	"trae2api-web/internal/upstream"
)

type row struct {
	file   string
	uid    string
	nick   string
	status string // OK | ALREADY | FAIL | AUTH_INVALID | LOAD_ERR
	detail string
	remain int64
	hasRem bool
}

// generationFor 从调度器的 state.json 读取账号的签到设备代数，
// 使 CLI 与调度器使用同一代设备 ID（缺文件/缺账号时回退 0，只读不写）。
func generationFor(stateFp, uid string) int {
	if stateFp == "" {
		return 0
	}
	return pool.New(stateFp).CheckinGeneration(uid)
}

func main() {
	dir := "auths"
	stateFp := ""
	for _, arg := range os.Args[1:] {
		if v, ok := strings.CutPrefix(arg, "--state="); ok {
			stateFp = v
		} else if dir == "auths" {
			dir = arg
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "trae-*.json"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "no auth files in %s\n", dir)
		os.Exit(1)
	}
	sort.Strings(files)
	up := upstream.New()

	var rows []row
	okN, alreadyN, failN := 0, 0, 0
	for _, f := range files {
		r := row{file: filepath.Base(f)}
		raw, err := os.ReadFile(f)
		if err != nil {
			r.status, r.detail = "LOAD_ERR", err.Error()
			rows = append(rows, r)
			failN++
			continue
		}
		a, err := auth.Parse(raw)
		if err != nil {
			r.status, r.detail = "LOAD_ERR", err.Error()
			rows = append(rows, r)
			failN++
			continue
		}
		a.FilePath = f
		r.uid, r.nick = a.UID, a.Nickname

		// refresh 过期 token
		if a.NeedsRefresh(2 * time.Hour) {
			if err := up.RefreshToken(a); err != nil {
				if ue, ok := err.(*upstream.Error); ok && ue.Kind == upstream.ErrSessionDead {
					r.status = "AUTH_INVALID"
				} else {
					r.status = "FAIL"
				}
				r.detail = "refresh: " + short(err.Error())
				rows = append(rows, r)
				failN++
				continue
			}
			_ = a.SaveAtomic()
		}

		// 签到（代数来自调度器 state.json，与调度器同代；9074/业务码失败如实上报，不重试）
		deviceID, _ := upstream.CheckinDevice(upstream.CheckinIdentity(a), generationFor(stateFp, a.UID))
		checkedIn, _, enable, serr := up.CheckinStatus(a, deviceID)
		switch {
		case serr != nil:
			if isAlready(serr.Error()) {
				r.status = "ALREADY"
				r.detail = short(serr.Error())
				alreadyN++
			} else {
				r.status = "FAIL"
				r.detail = short(serr.Error())
				failN++
			}
		case checkedIn:
			r.status = "ALREADY"
			r.detail = "already checked in"
			alreadyN++
		case !enable:
			r.status = "FAIL"
			r.detail = "checkin disabled"
			failN++
		default:
			if err := up.CheckinClaim(a, deviceID); err != nil {
				r.status = "FAIL"
				r.detail = short(err.Error())
				failN++
			} else {
				r.status = "OK"
				okN++
			}
		}
		// 查积分
		if remain, qerr := up.UserEntUsage(a); qerr == nil {
			r.remain, r.hasRem = remain, true
		}
		rows = append(rows, r)
	}

	// 报告
	fmt.Printf("uid                                  | nick        | status       | remain | detail\n")
	fmt.Printf("-------------------------------------+-------------+--------------+--------+------------------------------\n")
	for _, r := range rows {
		remain := "-"
		if r.hasRem {
			remain = fmt.Sprintf("%d", r.remain)
		}
		fmt.Printf("%-36s | %-11s | %-12s | %-6s | %s\n",
			trunc(r.uid, 36), trunc(r.nick, 11), r.status, remain, r.detail)
	}
	fmt.Printf("\ntotal=%d ok=%d already=%d fail=%d\n", len(rows), okN, alreadyN, failN)
}

// isAlready 已签判定：仅匹配明确表示"今日已签到"的业务错误。
// 只用无歧义标记，避免 429/5xx body 含 "checkin" 字样被误判为已签。
// 9095（设备已代签，多为别的账号）永远不算本账号已签。
func isAlready(msg string) bool {
	s := strings.ToLower(msg)
	if strings.Contains(s, "9095") {
		return false
	}
	return strings.Contains(s, "已签到") ||
		strings.Contains(s, "already check") ||
		strings.Contains(s, "already checked")
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func short(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 60 {
		return s[:60]
	}
	return s
}
