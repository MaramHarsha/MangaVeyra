package update

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseCLIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "release.key")
	var out, errOut bytes.Buffer
	if err := RunReleaseCLI([]string{"keygen", "--out", keyPath}, &out, &errOut); err != nil {
		t.Fatalf("keygen: %v %s", err, errOut.String())
	}
	if fi, err := os.Stat(keyPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	pub := regexp.MustCompile(`public key:\s+(\S+)`).FindStringSubmatch(out.String())
	if pub == nil {
		t.Fatalf("no public key printed: %s", out.String())
	}
	if err := RunReleaseCLI([]string{"keygen", "--out", keyPath}, &out, &errOut); err == nil {
		t.Fatal("keygen must not overwrite a key")
	}

	bin := filepath.Join(dir, "mangaveyra")
	_ = os.WriteFile(bin, []byte("release binary"), 0o755)
	out.Reset()
	if err := RunReleaseCLI([]string{"asset", "--os", "linux", "--arch", "amd64", "--url", "https://releases.example.org/mv", bin}, &out, &errOut); err != nil {
		t.Fatalf("asset: %v", err)
	}
	var a Asset
	if err := json.Unmarshal(out.Bytes(), &a); err != nil || a.Size != 14 || a.SHA256 != sum([]byte("release binary")) {
		t.Fatalf("asset = %+v %v", a, err)
	}

	m := sampleManifest()
	m.Assets = []Asset{a}
	manPath := filepath.Join(dir, "stable.json")
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(manPath, b, 0o644)
	out.Reset()
	if err := RunReleaseCLI([]string{"sign", "--key", keyPath, manPath}, &out, &errOut); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := RunReleaseCLI([]string{"verify", "--pub", pub[1], manPath}, &out, &errOut); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// The default build has no trusted key, so verify without --pub refuses.
	if err := RunReleaseCLI([]string{"verify", manPath}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "no trusted release key") {
		t.Fatalf("verify without a key: %v", err)
	}
	_ = os.WriteFile(manPath, bytes.Replace(b, []byte("1.0.2"), []byte("1.0.9"), 1), 0o644)
	if err := RunReleaseCLI([]string{"verify", "--pub", pub[1], manPath}, &out, &errOut); err == nil {
		t.Fatal("a tampered manifest verified")
	}
}
