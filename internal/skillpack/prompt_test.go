package skillpack

import (
	"strings"
	"testing"
)

func TestPromptUsesGitHub(t *testing.T) {
	p := Prompt("https://art.example.com", "art_x", "u")
	for _, want := range []string{
		"https://codeload.github.com/JDLu2003/cloud-artifact/tar.gz/refs/heads/main",
		"--strip-components=2 cloud-artifact-main/skill/artifact",
		"https://github.com/JDLu2003/cloud-artifact/tree/main/skill/artifact",
		"https://art.example.com/skill.tar.gz", // GitHub 不可用时的备用
	} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词缺少 %q", want)
		}
	}
}
