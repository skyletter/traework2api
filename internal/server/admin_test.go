package server

import (
	"strings"
	"testing"
)

func TestAdminPageHasOneClickLoginControls(t *testing.T) {
	html := string(adminPageHTML)
	for _, want := range []string{
		`id="loginBtn"`,
		"一键授权登录",
		`id="copyLoginBtn"`,
		"function copyLoginUrl()",
		"window.open('about:blank', '_blank')",
		"const copied = document.execCommand('copy')",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("admin page missing %q", want)
		}
	}
}
