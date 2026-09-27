// addaccount.go `tw2api add-account` 子命令：容器内交互式添加 TRAE SOLO 账号。
//
// 流程（对齐 login.sh）：
//  1. 生成 machine_id/device_id 并构造登录链接，打印给用户
//  2. 用户在浏览器登录 → 跳到打不开的 127.0.0.1 回调页（正常现象）
//  3. 用户粘贴地址栏完整回调链接
//  4. ExchangeToken 换 token → GetUserInfo 取账号信息
//  5. 落盘 auths/trae-{uid}.json（原子写）→ 自动签到
//
// 用法（容器内）：
//
//	docker exec -it <容器名> /app/add-account.sh
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"trae2api-web/internal/auth"
	"trae2api-web/internal/upstream"
)

const (
	addLoginURL    = "https://www.trae.cn/authorization"
	addClientID    = "en1oxy7wnw8j9n" // SOLO stable
	addAppVersion  = "0.1.52"
	addAPIHost     = "https://api.trae.com.cn"
	addPluginVer   = "2.3.62834"
	addCallbackURL = "http://127.0.0.1:18080/authorize"
)

// runAddAccount 容器内交互式添加账号。
func runAddAccount(args []string) {
	fs := flag.NewFlagSet("add-account", flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "配置文件路径（不存在时用默认值+环境变量）")
	authDirFlag := fs.String("auth-dir", "", "凭证落盘目录（默认取配置 auth_dir 或 ./auths）")
	_ = fs.Parse(args)

	cfg, err := Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[错误] 加载配置失败：%v\n", err)
		os.Exit(1)
	}
	dir := cfg.AuthDir
	if *authDirFlag != "" {
		dir = *authDirFlag
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[错误] 创建凭证目录失败：%v\n", err)
		os.Exit(1)
	}

	machineID := randHex(16)
	deviceID := randHex(16)

	// 登录链接参数集与 login.sh / 官方客户端保持一致。
	params := url.Values{
		"login_version":     {"1"},
		"auth_from":         {"solo"},
		"login_channel":     {"native_ide"},
		"plugin_version":    {addPluginVer},
		"auth_type":         {"local"},
		"client_id":         {addClientID},
		"redirect":          {"0"},
		"login_trace_id":    {randHex(8)},
		"auth_callback_url": {addCallbackURL},
		"machine_id":        {machineID},
		"device_id":         {deviceID},
		"x_device_id":       {deviceID},
		"x_machine_id":      {machineID},
		"x_device_brand":    {"PC"},
		"x_device_type":     {"PC"},
		"x_os_version":      {"1.0"},
		"x_app_version":     {addAppVersion},
		"x_app_type":        {"stable"},
	}
	loginURL := addLoginURL + "?" + params.Encode()

	fmt.Println("============================================================")
	fmt.Println("  TRAE SOLO 账号添加")
	fmt.Println("============================================================")
	fmt.Println()
	fmt.Println("步骤：")
	fmt.Println("  1. 在浏览器打开下面的登录链接（建议无痕窗口），用手机号/验证码登录")
	fmt.Println("  2. 登录成功后浏览器会跳到一个打不开的 127.0.0.1 页面 —— 这是正常的")
	fmt.Println("  3. 复制浏览器地址栏的完整链接，粘贴到下面")
	fmt.Println()
	fmt.Println("登录链接：")
	fmt.Println()
	fmt.Println("  " + loginURL)
	fmt.Println()

	fmt.Print("粘贴回调链接后回车（内容含敏感凭证，请勿外传）：\n> ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintln(os.Stderr, "\n[错误] 读取输入失败")
		os.Exit(1)
	}
	callback := strings.TrimSpace(line)
	if callback == "" {
		fmt.Fprintln(os.Stderr, "[错误] 未输入回调链接，已取消")
		os.Exit(1)
	}

	u, err := url.Parse(callback)
	if err != nil || u.Scheme == "" || u.RawQuery == "" {
		fmt.Fprintln(os.Stderr, "[错误] 回调链接格式不正确（应以 http://127.0.0.1:18080/authorize?... 开头）")
		os.Exit(1)
	}
	q := u.Query()

	// 回调里的 userInfo（URL 编码 JSON）作 uid/nickname 的兜底。
	uidHint, nickHint := "", ""
	if raw := q.Get("userInfo"); raw != "" {
		var ui map[string]any
		if json.Unmarshal([]byte(raw), &ui) == nil {
			uidHint = anyToString(ui["UserID"])
			nickHint = anyToString(ui["ScreenName"])
		}
	}
	refreshToken := q.Get("refreshToken")
	if refreshToken == "" {
		if raw := q.Get("userJwt"); raw != "" {
			var uj map[string]any
			if json.Unmarshal([]byte(raw), &uj) == nil {
				refreshToken = anyToString(uj["RefreshToken"])
			}
		}
	}
	if refreshToken == "" {
		fmt.Fprintln(os.Stderr, "[错误] 回调链接里没有 refreshToken，请确认登录成功并复制了完整链接")
		os.Exit(1)
	}

	client := upstream.New()
	a := &auth.Auth{
		RefreshToken: refreshToken,
		Domain:       "trae.cn",
		ApiHost:      addAPIHost,
		MachineID:    machineID,
		DeviceID:     deviceID,
	}

	fmt.Println()
	fmt.Println("[*] 正在换取 Token ...")
	if err := client.RefreshToken(a); err != nil {
		fmt.Fprintf(os.Stderr, "[错误] ExchangeToken 失败：%v\n", err)
		fmt.Fprintln(os.Stderr, "      回调链接有时效，请重新运行本命令后再登录一次。")
		os.Exit(1)
	}

	uid, nickname, enterpriseID, uiErr := client.GetUserInfo(a)
	if uid == "" {
		uid, nickname = uidHint, nickHint
	} else if nickname == "" {
		nickname = nickHint
	}
	if uiErr != nil && uid == "" {
		fmt.Fprintf(os.Stderr, "[错误] 获取账号信息失败：%v\n", uiErr)
		os.Exit(1)
	}
	if uid == "" {
		fmt.Fprintln(os.Stderr, "[错误] 未能获取账号 UID，token 可能无效")
		os.Exit(1)
	}
	a.UID = uid
	a.Nickname = nickname
	a.EnterpriseID = enterpriseID

	a.FilePath = auth.FilePathFor(dir, uid)
	if err := a.SaveAtomic(); err != nil {
		fmt.Fprintf(os.Stderr, "[错误] 保存凭证失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Println("[成功] 凭证已保存：" + a.FilePath)
	fmt.Printf("       账号：%s (uid=%s)\n", nickname, uid)

	// 自动签到（失败不影响使用，对齐 login.sh 行为）。
	fmt.Println("[*] 尝试自动签到 ...")
	identity := upstream.CheckinIdentity(a)
	checkinDevice, _ := upstream.CheckinDevice(identity, 0)
	checkedIn, _, enable, cerr := client.CheckinStatus(a, checkinDevice)
	switch {
	case cerr != nil:
		fmt.Printf("    签到查询失败（不影响使用）：%v\n", cerr)
	case checkedIn:
		fmt.Println("    今日已签到")
	case !enable:
		fmt.Println("    签到未开放")
	default:
		if cerr := client.CheckinClaim(a, checkinDevice); cerr != nil {
			fmt.Printf("    签到失败（不影响使用，服务会稍后自动重试）：%v\n", cerr)
		} else {
			fmt.Println("    签到成功")
		}
	}

	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println("  接下来（在 NAS 宿主机执行，非容器内）：")
	fmt.Println("    1. docker restart <你的容器名>        # 使其加载新账号")
	fmt.Println("    2. 打开 http://<NAS_IP>:7864/admin 面板查看账号")
	fmt.Println("    * 如容器未挂载 config.json，默认凭证目录为 /app/auths")
	fmt.Println("============================================================")
}

// randHex 生成 n 字节随机数的 hex 字符串。
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		fmt.Fprintf(os.Stderr, "[错误] 随机数生成失败：%v\n", err)
		os.Exit(1)
	}
	return hex.EncodeToString(b)
}

// anyToString 把 JSON 反序列化出的任意类型宽松转为字符串。
func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
