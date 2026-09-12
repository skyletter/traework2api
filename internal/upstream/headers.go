// headers.go SOLO 三类请求头：对话（SOLOHeaders）/ ug（UgHeaders）/ oauth（OAuthHeaders）。
package upstream

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"

	"trae2api-web/internal/auth"
)

const clientUA = "Trae/" + IdeVersion

// SOLOHeaders 设置 llm_utils_chat / get_detail_param 所需的 SOLO 专属头。
// 规则来自 SPEC §1 SOLO headers（实测必须）。
func SOLOHeaders(req *http.Request, a *auth.Auth, stream bool) {
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", clientUA)
	at := a.JWT() // 读锁快照，防与 RefreshToken 写并发竞态
	req.Header.Set("Authorization", "Cloud-IDE-JWT "+at)
	req.Header.Set("X-Cloudide-Token", at)
	req.Header.Set("X-Ide-Token", at)
	if a.UID != "" {
		req.Header.Set("X-Uid", a.UID)
	}
	req.Header.Set("X-App-Id", AppID)
	req.Header.Set("X-App-Version", "default")
	req.Header.Set("X-Ide-Version", IdeVersion)
	req.Header.Set("X-Ide-Version-Code", IdeVersionCode)
	req.Header.Set("X-App-Version-Code", IdeVersionCode)
	req.Header.Set("X-Ide-Version-Type", "stable")
	req.Header.Set("X-Device-Type", "windows")
	req.Header.Set("X-OS-Version", OSVersion)
	req.Header.Set("X-Device-Brand", DeviceBrand)
	req.Header.Set("Request-Traffic-Type", "prod")
	if a.MachineID != "" {
		req.Header.Set("X-Machine-Id", a.MachineID)
	}
	if a.DeviceID != "" {
		req.Header.Set("X-Device-Id", a.DeviceID)
	}
}

// UgHeaders 设置签到/积分（api.trae.cn）所需头。
func UgHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", clientUA)
	req.Header.Set("Authorization", "Cloud-IDE-JWT "+a.JWT()) // 读锁快照
	req.Header.Set("X-User-Region", "CN")
	if a.DeviceID != "" {
		req.Header.Set("X-Device-Id", a.DeviceID)
	}
}

// CheckinIdentity 返回签到设备派生的账号稳定标识：UID 优先（与 JWT data.id
// 同源，token 刷新不变），缺失时回退登录 DeviceID。
func CheckinIdentity(a *auth.Auth) string {
	if a.UID != "" {
		return a.UID
	}
	return a.DeviceID
}

// CheckinDeviceID 返回账号稳定的签到设备 ID（16 位数字）。
// generation 为 9074 限流后的轮换代数（0 = 基线）。
// Source: autumnsentiment/Trae2api-cn src/trae_client.py:443-466 @ c698b19 (2026-09-01)
// Divergence: 身份取 UID（与 JWT data.id 同源，见 CheckinIdentity），
//
//	不解析 JWT；操作员 pin 由 CheckinDevice 处理。
//
// Verified: TestCheckinDeviceIDStable（含 Python 交叉向量 u1→4302850041909017）
func CheckinDeviceID(identity string, generation int) string {
	if identity == "" {
		return ""
	}
	material := identity
	if generation > 0 {
		material = fmt.Sprintf("%s#gen%d", identity, generation)
	}
	sum := sha256.Sum256([]byte(material))
	n := new(big.Int).SetBytes(sum[:])
	n.Mod(n, big.NewInt(1e16))
	return fmt.Sprintf("%016d", n)
}

// CheckinDevice 返回签到请求实际使用的设备 ID。
// 操作员 pin（TRAE_CHECKIN_DEVICE_ID 全局，或 TRAE_CHECKIN_DEVICE_IDS_JSON
// 按 UID 映射）优先；pin 命中时 pinned=true，调用方不得轮换。
// Source: autumnsentiment/Trae2api-cn src/trae_client.py:356-370 @ b0bf9c2 (2026-08-29)
// Divergence: 键只查 UID（本服务账号标识即 UID）；不支持 JWT 解析回退。
func CheckinDevice(identity string, generation int) (id string, pinned bool) {
	if v := checkinPin(identity); v != "" {
		return v, true
	}
	return CheckinDeviceID(identity, generation), false
}

// checkinPin 读取操作员 pin：单账号映射优先于全局值。
func checkinPin(identity string) string {
	if raw := strings.TrimSpace(os.Getenv("TRAE_CHECKIN_DEVICE_IDS_JSON")); raw != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			if v := strings.TrimSpace(m[identity]); v != "" {
				return v
			}
		}
	}
	return strings.TrimSpace(os.Getenv("TRAE_CHECKIN_DEVICE_ID"))
}

// CheckinHeaders 设置签到 status/claim 的极简头：只带派生设备 ID 与品牌/类型。
// 故意不带 UA/Accept/Region（Source: Trae2api-cn build_checkin_headers @ b0bf9c2）。
// brand/type 沿用本项目 SOLO 常量（DeviceBrand/"windows"），不抄对方 CN 值。
func CheckinHeaders(req *http.Request, a *auth.Auth, deviceID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Cloud-IDE-JWT "+a.JWT()) // 读锁快照
	if deviceID != "" {
		req.Header.Set("X-Device-Id", deviceID)
	} else if a.DeviceID != "" {
		req.Header.Set("X-Device-Id", a.DeviceID)
	}
	req.Header.Set("X-Device-Brand", DeviceBrand)
	req.Header.Set("X-Device-Type", "windows")
}

// OAuthHeaders 设置 ExchangeToken / GetUserInfo 所需头（无签名，仅 UA）。
func OAuthHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", clientUA)
}
