package updater

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"v1.0.0", "1.0.0", 0},
		{"1.2.3", "1.2.4", -1},
		{"2.0.0", "1.99.99", 1},
		{"1.2.3+build.7", "1.2.3", 0},
	}
	for _, test := range tests {
		got, err := CompareVersions(test.left, test.right)
		if err != nil {
			t.Fatalf("CompareVersions(%q, %q): %v", test.left, test.right, err)
		}
		if got != test.want {
			t.Fatalf("CompareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
	for _, invalid := range []string{"dev", "1.0", "1.0.0-beta.1", "01.0.0"} {
		if _, err := NormalizeVersion(invalid); err == nil {
			t.Fatalf("NormalizeVersion(%q) succeeded", invalid)
		}
	}
}

func TestBinaryAssetName(t *testing.T) {
	if got, ok := BinaryAssetName("amd64"); !ok || got != "mailmanager-linux-amd64" {
		t.Fatalf("amd64 asset = %q, %v", got, ok)
	}
	if _, ok := BinaryAssetName("386"); ok {
		t.Fatal("386 must not be supported")
	}
}
