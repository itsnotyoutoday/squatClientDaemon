package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHKeyRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	k := SSHKeyFromEd25519(pub, "nereus@laptop")
	p, err := ParseSSHPublicKey(k.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if p.Fingerprint() != k.Fingerprint() || p.Comment != "nereus@laptop" {
		t.Fatalf("%+v", p)
	}
	msg := SSHChallengeMessage("nereus", []byte("nonce"), []byte("tlsfp"))
	sig := SSHSignatureBlob(ed25519.Sign(priv, msg))
	if !p.VerifySSHSignature(msg, sig) {
		t.Fatal("valid signature rejected")
	}
	// bound to user, nonce and server
	for _, other := range [][]byte{
		SSHChallengeMessage("alice", []byte("nonce"), []byte("tlsfp")),
		SSHChallengeMessage("nereus", []byte("other"), []byte("tlsfp")),
		SSHChallengeMessage("nereus", []byte("nonce"), []byte("otherfp")),
	} {
		if p.VerifySSHSignature(other, sig) {
			t.Fatal("signature accepted for a different challenge")
		}
	}
	// a different key does not verify
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	if SSHKeyFromEd25519(pub2, "").VerifySSHSignature(msg, sig) {
		t.Fatal("wrong key accepted")
	}
	// malformed signatures
	for _, bad := range [][]byte{nil, {1, 2, 3}, sig[:len(sig)-1], append(append([]byte{}, sig...), 0)} {
		if p.VerifySSHSignature(msg, bad) {
			t.Fatalf("accepted malformed signature %x", bad)
		}
	}
}

func TestParseSSHPublicKeyRefusals(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	good := SSHKeyFromEd25519(pub, "x").Marshal()
	for name, line := range map[string]string{
		"empty":    "",
		"one word": "ssh-ed25519",
		"options":  `command="/bin/sh" ` + good,
		"bad b64":  "ssh-ed25519 !!!!",
		"mismatch": "ssh-rsa " + strings.Fields(good)[1],
		"rsa":      "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC7 x",
	} {
		if _, err := ParseSSHPublicKey(line); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseSSHPublicKey("ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC7"); err == nil {
		t.Error("rsa accepted")
	}
}

// the fingerprint must equal what OpenSSH prints (cross-check with ssh-keygen when it is installed)
func TestFingerprintMatchesSSHKeygen(t *testing.T) {
	path, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	if out, err := exec.Command(path, "-q", "-t", "ed25519", "-N", "", "-C", "test@host", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen: %v %s", err, out)
	}
	line, _ := os.ReadFile(key + ".pub")
	k, err := ParseSSHPublicKey(string(line))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(path, "-l", "-f", key+".pub").Output()
	if err != nil {
		t.Skip(err)
	}
	if !strings.Contains(string(out), k.Fingerprint()) {
		t.Fatalf("ssh-keygen says %q, we say %s", out, k.Fingerprint())
	}
	if k.Comment != "test@host" {
		t.Fatalf("comment %q", k.Comment)
	}
}
