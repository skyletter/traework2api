package upstream

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"trae2api-web/internal/auth"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   ErrKind
	}{
		{200, `{"code":1005,"message":"plan limit","extra":{"plan":2}}`, ErrPlanLimit},
		{200, `{"code":1005,"msg":"权益不足"}`, ErrPlanLimit},
		{429, ``, ErrSoftRate},
		{401, `{"code":1001,"msg":"login required"}`, ErrSessionDead},
		{401, ``, ErrSessionDead},
		{404, ``, ErrNotFound},
		{500, `boom`, ErrServer},
		{503, `unavailable`, ErrServer},
		{400, `{"code":11101,"msg":"bad param"}`, ErrClient},
		{200, `{"checked_in":false}`, ErrNone},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.body); got != c.want {
			t.Errorf("Classify(%d,%q)=%v want %v", c.status, c.body, got, c.want)
		}
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testClient(fn rtFunc) *Client {
	return &Client{
		HTTP:      &http.Client{Transport: fn},
		AgentHost: "https://agent.example",
		UgHost:    "https://ug.example",
		OAuthHost: "https://oauth.example",
		ClientID:  ClientID,
	}
}

func TestRefreshTokenExchange(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, EpExchange) {
			return nil, errors.New("wrong path: " + r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			return nil, errors.New("missing content-type")
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"ClientID":"en1oxy7wnw8j9n"`)) || !bytes.Contains(body, []byte(`"RefreshToken":"oldrt"`)) {
			return nil, errors.New("bad body: " + string(body))
		}
		return jsonResp(200, `{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786805537,"TokenExpireDuration":1209600}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "oldrt", ExpiresAt: 1, ApiHost: "https://oauth.example"}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if a.AccessToken != "newat" || a.RefreshToken != "newrt" {
		t.Errorf("tokens not updated: %+v", a)
	}
	if a.ExpiresAt != 1786805537 {
		t.Errorf("expiresAt=%d", a.ExpiresAt)
	}
}

// TestRefreshTokenExchangeMilliseconds 覆盖上游 TokenExpireAt 返回毫秒的场景：
// 必须归一化为 Unix 秒后再写 auth.ExpiresAt。
func TestRefreshTokenExchangeMilliseconds(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786847930141,"TokenExpireDuration":1209600}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "oldrt", ExpiresAt: 1, ApiHost: "https://oauth.example"}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if a.ExpiresAt != 1786847930 {
		t.Errorf("expiresAt=%d want 1786847930 (毫秒转秒)", a.ExpiresAt)
	}
}

func TestRefreshTokenIfNeededSkipsFresh(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResp(200, `{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786847930141}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999, ApiHost: "https://oauth.example"}
	refreshed, err := c.RefreshTokenIfNeeded(a, 24*3600*1e9)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed {
		t.Error("fresh token should not refresh")
	}
	if calls != 0 {
		t.Errorf("ExchangeToken should not be called, calls=%d", calls)
	}
	if a.AccessToken != "at" {
		t.Error("token should remain unchanged")
	}
}

func TestRefreshTokenIfNeededRefreshesExpired(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResp(200, `{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786847930141}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1, ApiHost: "https://oauth.example"}
	refreshed, err := c.RefreshTokenIfNeeded(a, 24*3600*1e9)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed || calls != 1 {
		t.Errorf("expired token should refresh once, refreshed=%v calls=%d", refreshed, calls)
	}
	if a.AccessToken != "newat" || a.RefreshToken != "newrt" {
		t.Errorf("tokens not updated: %+v", a)
	}
}

func TestRefreshTokenUsesAuthApiHost(t *testing.T) {
	var gotHost string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		gotHost = r.URL.Scheme + "://" + r.URL.Host
		return jsonResp(200, `{"Result":{"Token":"newat"}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1, ApiHost: "https://custom.example"}
	if err := c.RefreshToken(a); err != nil {
		t.Fatal(err)
	}
	if gotHost != "https://custom.example" {
		t.Errorf("host=%s want auth.apiHost", gotHost)
	}
}

func TestChatStreamSendsHeadersAndRewritesBody(t *testing.T) {
	var gotAuth, gotUID, gotAppID, gotIdeVer string
	var gotBody []byte
	c := testClient(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		gotUID = r.Header.Get("X-Uid")
		gotAppID = r.Header.Get("X-App-Id")
		gotIdeVer = r.Header.Get("X-Ide-Version")
		gotBody, _ = io.ReadAll(r.Body)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("event:done\ndata:{\"finish_reason\":\"stop\"}\n\n")),
		}, nil
	})
	a := &auth.Auth{AccessToken: "at", UID: "u1", MachineID: "m1", DeviceID: "d1"}
	rc, status, respBody, err := c.ChatStream(a, []byte(`{"model":"glm-5.2","messages":[]}`))
	if err != nil || status != 200 {
		t.Fatalf("chat: status=%d err=%v", status, err)
	}
	if respBody != nil {
		t.Errorf("200 response should carry nil body, got %q", respBody)
	}
	rc.Close()
	if gotAuth != "Cloud-IDE-JWT at" || gotUID != "u1" {
		t.Errorf("headers: auth=%q uid=%q", gotAuth, gotUID)
	}
	if gotAppID != AppID || gotIdeVer != IdeVersion {
		t.Errorf("app headers: appid=%q idever=%q", gotAppID, gotIdeVer)
	}
	if !bytes.Contains(gotBody, []byte(`"stream":true`)) || !bytes.Contains(gotBody, []byte(`"function":"solo_work_lite"`)) {
		t.Errorf("body not rewritten: %s", gotBody)
	}
}

func TestChatStreamUsesDedicatedStreamClient(t *testing.T) {
	// StreamHTTP 优先于 HTTP 被 ChatStream 使用（无总超时的长 SSE 流客户端）。
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("event:done\ndata:{\"finish_reason\":\"stop\"}\n\n")),
		}, nil
	})
	c.StreamHTTP = &http.Client{Transport: c.HTTP.Transport} // 无 Timeout
	rc, status, _, err := c.ChatStream(&auth.Auth{AccessToken: "at", UID: "u1"}, []byte(`{"model":"glm-5.2","messages":[]}`))
	if err != nil || status != 200 {
		t.Fatalf("chat: status=%d err=%v", status, err)
	}
	rc.Close()
	if c.StreamHTTP.Timeout != 0 {
		t.Errorf("stream client should have no total timeout, got %v", c.StreamHTTP.Timeout)
	}
}

func TestChatStreamHTTPError(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(429, `rate limited`), nil
	})
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	_, status, respBody, err := c.ChatStream(a, []byte(`{}`))
	if status != 429 {
		t.Errorf("status=%d", status)
	}
	if err != nil {
		t.Fatalf("429 should come via status, err=%v", err)
	}
	if Classify(status, string(respBody)) != ErrSoftRate {
		t.Errorf("not classified soft rate: %q", respBody)
	}
}

func TestUserEntUsageAggregation(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, EpEntUsage) {
			return nil, errors.New("wrong path: " + r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Cloud-IDE-JWT at" {
			return nil, errors.New("missing auth header")
		}
		return jsonResp(200, `{"is_credits_billing":true,"user_entitlement_pack_list":[
			{"entitlement_base_info":{"quota":{"credits_limit":2000}}},
			{"entitlement_base_info":{"quota":{"credits_limit":500}}}
		]}`), nil
	})
	remain, err := c.UserEntUsage(&auth.Auth{AccessToken: "at"})
	if err != nil {
		t.Fatalf("ent usage: %v", err)
	}
	if remain != 2500 {
		t.Errorf("remain=%d want 2500", remain)
	}
}

func TestCheckinStatusAndClaim(t *testing.T) {
	var path string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		if got := r.Header.Get("X-Device-Brand"); got != "83DG" {
			return nil, errors.New("missing X-Device-Brand 83DG, got " + got)
		}
		if got := r.Header.Get("X-Device-Type"); got != "windows" {
			return nil, errors.New("missing X-Device-Type windows, got " + got)
		}
		if got := r.Header.Get("User-Agent"); got != "" {
			return nil, errors.New("checkin must not send User-Agent, got " + got)
		}
		if got := r.Header.Get("X-User-Region"); got != "" {
			return nil, errors.New("checkin must not send X-User-Region, got " + got)
		}
		return jsonResp(200, `{"checked_in":false,"credits":200,"enable":true}`), nil
	})
	checkedIn, credits, enable, err := c.CheckinStatus(&auth.Auth{AccessToken: "at"}, CheckinDeviceID("", 0))
	if err != nil {
		t.Fatal(err)
	}
	if checkedIn || !enable || credits != 200 {
		t.Errorf("status: checked=%v enable=%v credits=%d", checkedIn, enable, credits)
	}
	if path != EpCheckinStatus {
		t.Errorf("path=%s", path)
	}
}

func TestIsModelConfigMismatch(t *testing.T) {
	if !IsModelConfigMismatch(400, `{"code":4001,"message":"model config is empty"}`) {
		t.Error("4001 model-config-empty must match")
	}
	if IsModelConfigMismatch(400, `{"code":4001,"message":"We're sorry, the param is invalid."}`) {
		t.Error("other 4001 messages must not match")
	}
	if IsModelConfigMismatch(400, `{"code":40012,"message":"model config is empty"}`) {
		t.Error("40012 must not match 4001 (non-digit guard)")
	}
	if IsModelConfigMismatch(429, `{"code":4001,"message":"model config is empty"}`) {
		t.Error("non-400 status must not match")
	}
	if IsModelConfigMismatch(400, `{"message":"model config is empty"}`) {
		t.Error("missing code must not match")
	}
}

func TestCheckinStatusRateLimited(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"code":9074,"message":"当前使用人数太多，请稍后再试"}`), nil
	})
	_, _, _, err := c.CheckinStatus(&auth.Auth{AccessToken: "at"}, CheckinDeviceID("u1", 0))
	if !errors.Is(err, ErrCheckinRateLimited) {
		t.Fatalf("status 9074 err=%v want ErrCheckinRateLimited (else scheduler skips rotation)", err)
	}
}

func TestCheckinDevicePin(t *testing.T) {
	t.Setenv("TRAE_CHECKIN_DEVICE_ID", "pinned-device-1")
	id, pinned := CheckinDevice("u1", 3)
	if !pinned || id != "pinned-device-1" {
		t.Fatalf("env pin ignored: id=%q pinned=%v", id, pinned)
	}
}

func TestCheckinDevicePinPerAccount(t *testing.T) {
	t.Setenv("TRAE_CHECKIN_DEVICE_IDS_JSON", `{"u1":"per-acct-9"}`)
	id, pinned := CheckinDevice("u1", 0)
	if !pinned || id != "per-acct-9" {
		t.Fatalf("per-account pin ignored: id=%q pinned=%v", id, pinned)
	}
	if id2, pinned2 := CheckinDevice("u2", 0); pinned2 || id2 == "" || id2 == "per-acct-9" {
		t.Errorf("unlisted account must derive, got id=%q pinned=%v", id2, pinned2)
	}
}

func TestCheckinDeviceDerived(t *testing.T) {
	t.Setenv("TRAE_CHECKIN_DEVICE_ID", "")
	t.Setenv("TRAE_CHECKIN_DEVICE_IDS_JSON", "")
	id, pinned := CheckinDevice("u1", 0)
	if pinned || id != CheckinDeviceID("u1", 0) {
		t.Errorf("no pin must derive baseline: id=%q pinned=%v", id, pinned)
	}
}

func TestCheckinClaimSurfacesRateLimit(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"code":9074,"message":"当前使用人数太多，请稍后再试"}`), nil
	})
	err := c.CheckinClaim(&auth.Auth{AccessToken: "at"}, CheckinDeviceID("u1", 0))
	if err == nil {
		t.Fatal("9074 claim should error, got nil (silent success hides rate limit)")
	}
	if !errors.Is(err, ErrCheckinRateLimited) {
		t.Errorf("err=%v want ErrCheckinRateLimited", err)
	}
}

func TestCheckinClaimSurfacesBusinessError(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"code":9004,"message":"order params incorrect"}`), nil
	})
	if err := c.CheckinClaim(&auth.Auth{AccessToken: "at"}, CheckinDeviceID("u1", 0)); err == nil {
		t.Fatal("nonzero business code should error, got nil")
	}
}

func TestCheckinDeviceIDStable(t *testing.T) {
	a := CheckinDeviceID("u1", 0)
	b := CheckinDeviceID("u1", 0)
	if a == "" || a != b {
		t.Fatalf("device id not stable: %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("device id len=%d want 16 (%q)", len(a), a)
	}
	for _, ch := range a {
		if ch < '0' || ch > '9' {
			t.Errorf("device id not numeric: %q", a)
			break
		}
	}
	if c := CheckinDeviceID("u1", 1); c == a {
		t.Errorf("generation bump should rotate device id, both %q", a)
	}
	if d := CheckinDeviceID("u2", 0); d == a {
		t.Errorf("different identity should differ, both %q", a)
	}
	if e := CheckinDeviceID("", 0); e != "" {
		t.Errorf("empty identity should yield empty id, got %q", e)
	}
	// 与 Trae2api-cn Python 实现逐值对齐（sha256 取模 1e16，零填充 16 位）。
	if v := CheckinDeviceID("u1", 0); v != "4302850041909017" {
		t.Errorf("device id vector mismatch: got %q want 4302850041909017", v)
	}
}

func TestCheckinStatusSendsDerivedDeviceID(t *testing.T) {
	var got string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Get("X-Device-Id")
		return jsonResp(200, `{"checked_in":false,"credits":200,"enable":true}`), nil
	})
	want := CheckinDeviceID("u1", 0)
	if _, _, _, err := c.CheckinStatus(&auth.Auth{AccessToken: "at", UID: "u1"}, want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("X-Device-Id=%q want derived %q", got, want)
	}
}
