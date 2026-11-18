package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// webapp copies the example to a temp dir with the given .config.cue.
func webapp(t *testing.T, config string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("examples/webapp")); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, ".config.cue"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func cli(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCheck(t *testing.T) {
	if code, out, errs := cli("check", webapp(t, "")); code != 0 || !strings.Contains(out, "complete") {
		t.Errorf("defaults should be complete: %d %q %q", code, out, errs)
	}
	dir := webapp(t, `server: protocol: "https"`+"\n"+`server: socket_path: "/x"`+"\n")
	code, _, errs := cli("check", dir)
	if code != 1 || !strings.Contains(errs, "server.cert_file") || !strings.Contains(errs, "server.cue:") {
		t.Errorf("missing cert_file should fail with its position: %d %q", code, errs)
	}
	if !strings.Contains(errs, "warning: server.socket_path") {
		t.Errorf("stale edit should be warned about: %q", errs)
	}
}

func TestExport(t *testing.T) {
	dir := webapp(t, `database: {type: "postgres", user: "u", password: "s3cr3t"}`+"\n")

	code, out, errs := cli("export", dir)
	var v map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("default format should be json: %d %q %q", code, out, errs)
	}

	// Flags after DIR; format from the extension; secret => 0600.
	file := filepath.Join(t.TempDir(), "app.yaml")
	if code, _, errs := cli("export", dir, "-o", file); code != 0 {
		t.Fatalf("export -o: %d %q", code, errs)
	}
	b, _ := os.ReadFile(file) //nolint:gosec // a file in t.TempDir()
	if !strings.Contains(string(b), "type: postgres") {
		t.Errorf("yaml expected:\n%s", b)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("file with a secret should be 0600, is %v", st.Mode().Perm())
	}

	// Without secrets: 0644, even when overwriting a 0600 file.
	plain := webapp(t, "")
	if code, _, errs := cli("export", "-f", "yaml", "-o", file, plain); code != 0 {
		t.Fatalf("export without secret: %d %q", code, errs)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o644 {
		t.Errorf("file without secrets should be 0644, is %v", st.Mode().Perm())
	}

	if code, _, _ := cli("export", "-f", "toml", plain); code != 1 {
		t.Errorf("unknown format should fail with 1, got %d", code)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		{"tree", "-o", "x", "."}, // -o only for export
		{"check", "a", "b"},      // one DIR
		{"export", "-z", "."},    // unknown flag
	} {
		if code, _, errs := cli(args...); code != 2 || !strings.Contains(errs, "usage:") {
			t.Errorf("%v: code %d, stderr %q", args, code, errs)
		}
	}
}

func TestVersion(t *testing.T) {
	if code, out, _ := cli("version"); code != 0 || !strings.HasPrefix(out, "cuemenu dev") {
		t.Errorf("version: %d %q", code, out)
	}
}
