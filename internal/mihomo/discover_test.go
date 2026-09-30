/**
 * 控制接口查找测试：配置文件解析与命令行里带空格的配置路径。
 */
package mihomo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFromConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte("mixed-port: 7897\nexternal-controller: ':9097'\nsecret: 'abc 123'\nexternal-controller-unix: /tmp/x/y.sock\ndns:\n  secret: nope\n"), 0o600)
	got := fromConfig(p, "测试")
	if len(got) != 2 || got[0].controller != "unix:/tmp/x/y.sock" || got[1].controller != "http://127.0.0.1:9097" || got[0].secret != "abc 123" {
		t.Fatalf("解析错误: %+v", got)
	}
}

func TestCoreArgs(t *testing.T) {
	cases := map[string]string{
		"/Applications/Clash Verge.app/Contents/MacOS/verge-mihomo -d /Users/u/Library/Application Support/x -f /Users/u/Library/Application Support/x/clash-verge.yaml": "/Users/u/Library/Application Support/x/clash-verge.yaml",
		"/usr/local/bin/mihomo -f /etc/mihomo/config.yaml -d /etc/mihomo":                                                                                                "/etc/mihomo/config.yaml",
	}
	for line, want := range cases {
		m := coreArgsRe.FindStringSubmatch(line)
		if m == nil || m[1] != want {
			t.Fatalf("%s => %v", line, m)
		}
	}
}
