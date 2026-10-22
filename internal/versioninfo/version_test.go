package versioninfo

import (
	"regexp"
	"testing"
)

func TestReleaseCatalog(t *testing.T) {
	info, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Releases) == 0 || info.Releases[0].Version != info.Current {
		t.Fatal("当前版本必须对应最新记录")
	}
	seen := map[string]bool{}
	for _, release := range info.Releases {
		if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(release.Version) || seen[release.Version] || len(release.Changes) == 0 {
			t.Fatalf("无效版本记录：%s", release.Version)
		}
		seen[release.Version] = true
	}
}
