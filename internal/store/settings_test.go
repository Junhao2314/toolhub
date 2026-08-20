package store

import "testing"

func TestNormalizeSubagentBundleDeduplicatesAndTrims(t *testing.T) {
	got, err := normalizeSubagentBundle("Pi", []string{" officecli ", "officecli", "ui-ux-pro-max-cn"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"officecli", "ui-ux-pro-max-cn"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
}

func TestNormalizeSubagentBundleRejectsUnsafeSlug(t *testing.T) {
	if _, err := normalizeSubagentBundle("Kimi frontend", []string{"../outside"}); err == nil {
		t.Fatal("unsafe Skill slug was accepted")
	}
}
