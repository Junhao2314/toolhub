package skills

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
)

func TestScanZIPRejectsTraversal(t *testing.T) {
	data := makeZIP(t, map[string]string{"../SKILL.md": "---\nname: Bad\n---\n"}, nil)
	if _, err := ScanZIP(data, DefaultLimits); err == nil {
		t.Fatal("path traversal was accepted")
	}
}

func TestScanZIPRejectsSymlink(t *testing.T) {
	data := makeZIP(t, map[string]string{"SKILL.md": "---\nname: Bad\n---\n"}, map[string]os.FileMode{"escape": os.ModeSymlink | 0777})
	if _, err := ScanZIP(data, DefaultLimits); err == nil {
		t.Fatal("symlink was accepted")
	}
}

func TestCanonicalHashIgnoresEntryOrder(t *testing.T) {
	first := makeZIPOrdered(t, []zipEntry{{"SKILL.md", "---\nname: Example\ndescription: test\nlicense: MIT\n---\nbody"}, {"notes.txt", "hello"}})
	second := makeZIPOrdered(t, []zipEntry{{"notes.txt", "hello"}, {"SKILL.md", "---\nname: Example\ndescription: test\nlicense: MIT\n---\nbody"}})
	a, err := ScanZIP(first, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ScanZIP(second, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 {
		t.Fatalf("canonical hashes differ: %s %s", a.SHA256, b.SHA256)
	}
}

func TestScanReportsRiskSignals(t *testing.T) {
	data := makeZIP(t, map[string]string{
		"SKILL.md": "---\nname: Deploy Helper\ndescription: deploys apps\nallowed-tools: [Bash, Read]\n---\nSee https://example.com/docs",
		"run.sh":   "#!/bin/sh\nAPI_KEY=abcdefghijklmnop\n",
	}, nil)
	pkg, err := ScanZIP(data, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Report.RiskLevel != "high" || len(pkg.Report.Scripts) != 1 || len(pkg.Report.Findings) == 0 {
		t.Fatalf("unexpected report: %+v", pkg.Report)
	}
}

type zipEntry struct{ name, body string }

func makeZIP(t *testing.T, files map[string]string, modes map[string]os.FileMode) []byte {
	t.Helper()
	entries := make([]zipEntry, 0, len(files)+len(modes))
	for name, body := range files {
		entries = append(entries, zipEntry{name, body})
	}
	data := makeZIPOrdered(t, entries)
	if len(modes) == 0 {
		return data
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, item := range entries {
		file, _ := writer.Create(item.name)
		_, _ = file.Write([]byte(item.body))
	}
	for name, mode := range modes {
		header := &zip.FileHeader{Name: name}
		header.SetMode(mode)
		file, _ := writer.CreateHeader(header)
		_, _ = file.Write([]byte("../outside"))
	}
	_ = writer.Close()
	return buffer.Bytes()
}

func makeZIPOrdered(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, item := range entries {
		file, err := writer.Create(item.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write([]byte(item.body))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
