package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsAlready(t *testing.T) {
	// 明确"已签到"标记 → true
	if !isAlready("今日已签到") {
		t.Error("已签到 should be true")
	}
	if !isAlready("you have already checked in") {
		t.Error("already checked in should be true")
	}
	// 歧义/错误路径 → false（不误判为已签）
	if isAlready("checkin service error") {
		t.Error("checkin service error should NOT be already")
	}
	if isAlready("upstream 429: checkin rate limited") {
		t.Error("429 rate limit should NOT be already")
	}
	if isAlready("code=400 bad request") {
		t.Error("code=400 should NOT be already (removed ambiguous marker)")
	}
	if isAlready("") {
		t.Error("empty should be false")
	}
	// 9095 是"设备已代签（别的账号）"，不是本账号已签 → false
	if isAlready("code 9095: device already checked in for another account") {
		t.Error("9095 device-scoped message must NOT count as already checked in")
	}
}

// CLI 与调度器共用 state.json 的代数：调度器已轮换到 gen N 时，
// CLI 必须用同一代签到 ID，否则会反复撞已被限流的基线 ID。
func TestGenerationForReadsStateFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	state := `{"accounts":{"u9":{"credits":500,"checkin_device_gen":2}}}`
	if err := os.WriteFile(fp, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if g := generationFor(fp, "u9"); g != 2 {
		t.Errorf("generation=%d want 2 from state file", g)
	}
	if g := generationFor(fp, "missing"); g != 0 {
		t.Errorf("missing account generation=%d want 0", g)
	}
	if g := generationFor("", "u9"); g != 0 {
		t.Errorf("empty state path generation=%d want 0", g)
	}
}
