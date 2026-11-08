package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const releaseUsage = `Usage:
  mangaveyra release keygen --out <private key file>
      Create an Ed25519 key pair. The private key is written to the file (mode 0600);
      the public key is printed for -ldflags "-X ...internal/update.TrustedKeys=<public key>".
  mangaveyra release asset --os <GOOS> --arch <GOARCH> --url <download URL> [--format binary|tar.gz] <file>
      Print the manifest asset entry (size and SHA-256) for a release file.
  mangaveyra release sign --key <private key file> <manifest.json>
      Check the manifest and write the detached signature to <manifest.json>.sig.
  mangaveyra release verify [--pub <public key>] <manifest.json>
      Verify <manifest.json>.sig with the given key (default: this build's trusted keys).
`

// RunReleaseCLI implements `mangaveyra release <keygen|asset|sign|verify>`,
// the owner's helper for publishing signed releases (ADR 0013 §6).
func RunReleaseCLI(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, releaseUsage)
		return errors.New("missing release command")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("release "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, releaseUsage) }
	switch sub {
	case "keygen":
		out := fs.String("out", "", "private key file to create")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *out == "" {
			return errors.New("release keygen: --out is required")
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("release keygen: %w", err)
		}
		if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv)); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "private key: %s (keep it offline; anyone with it can sign releases)\n", *out)
		fmt.Fprintf(stdout, "public key:  %s\n", base64.StdEncoding.EncodeToString(pub))
		fmt.Fprintf(stdout, "build with:  -ldflags \"-X github.com/MaramHarsha/MangaVeyra/internal/update.TrustedKeys=%s\"\n",
			base64.StdEncoding.EncodeToString(pub))
		return nil
	case "asset":
		goos := fs.String("os", "", "GOOS of the binary (linux, darwin...)")
		goarch := fs.String("arch", "", "GOARCH of the binary (amd64, arm64...)")
		u := fs.String("url", "", "download URL")
		format := fs.String("format", FormatBinary, "binary or tar.gz")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() != 1 || *goos == "" || *goarch == "" || *u == "" {
			return errors.New("release asset: --os, --arch, --url and one file are required")
		}
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			return err
		}
		a := Asset{OS: *goos, Arch: *goarch, URL: *u, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, Format: *format}
		if a.Format == FormatBinary {
			a.Format = ""
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(a)
	case "sign":
		keyPath := fs.String("key", "", "private key file from release keygen")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() != 1 || *keyPath == "" {
			return errors.New("release sign: --key and one manifest file are required")
		}
		key, err := readPrivateKey(*keyPath)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		man, err := ParseManifest(raw)
		if err != nil {
			return fmt.Errorf("release sign: %w", err)
		}
		sig := SignManifest(raw, key)
		if err := os.WriteFile(fs.Arg(0)+".sig", sig, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "signed %s %s (%s channel, %d assets) → %s.sig\n", fs.Arg(0), man.Version, man.Channel, len(man.Assets), fs.Arg(0))
		return nil
	case "verify":
		pub := fs.String("pub", "", "base64 public key (default: this build's trusted keys)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("release verify: one manifest file is required")
		}
		keys := trustedKeys()
		if *pub != "" {
			var err error
			if keys, err = ParseKeys(*pub); err != nil {
				return err
			}
		}
		raw, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(fs.Arg(0) + ".sig")
		if err != nil {
			return err
		}
		man, err := VerifyManifest(raw, sig, keys)
		if err != nil {
			return fmt.Errorf("release verify: %w", err)
		}
		fmt.Fprintf(stdout, "ok: %s %s (%s channel), %d assets\n", fs.Arg(0), man.Version, man.Channel, len(man.Assets))
		return nil
	case "-h", "--help", "help":
		fmt.Fprint(stdout, releaseUsage)
		return nil
	}
	fmt.Fprint(stderr, releaseUsage)
	return fmt.Errorf("unknown release command %q", sub)
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(k) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("release: %s is not a key from `mangaveyra release keygen`", path)
	}
	return ed25519.PrivateKey(k), nil
}
