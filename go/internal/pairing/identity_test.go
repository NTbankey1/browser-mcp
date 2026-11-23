package pairing

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateKeepsTheKey(t *testing.T) {
	home := t.TempDir()
	first, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	if first.PublicKey == "" || first.PrivateKey == "" {
		t.Fatal("identity is incomplete")
	}
	info, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("identity.json mode is %o, want 600", mode)
	}
	second, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	if second.PublicKey != first.PublicKey {
		t.Fatal("reloading the identity made a new key")
	}
}

func TestSignVerifyCommitmentAndCode(t *testing.T) {
	identity, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	commitment, err := Commitment(nonce)
	if err != nil {
		t.Fatal(err)
	}
	payload := Payload("challenge", 9010, commitment)
	signature, err := identity.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(identity.PublicKey, payload, signature)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("the signature did not verify")
	}
	if ok, _ := Verify(identity.PublicKey, payload+"x", signature); ok {
		t.Fatal("a changed payload verified")
	}
	code := Code(identity.PublicKey, "ext-nonce", nonce)
	if len(code) != 7 || code[3] != ' ' {
		t.Fatalf("code %q is not `NNN NNN`", code)
	}
}

// TestNodeAgrees pins the wire format: Node verifies our signature, derives
// the same SPKI and computes the same pairing code. Skipped where Node is not
// installed.
func TestNodeAgrees(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	home := t.TempDir()
	identity, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	daemonNonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	commitment, err := Commitment(daemonNonce)
	if err != nil {
		t.Fatal(err)
	}
	payload := Payload("ZmFrZS1jaGFsbGVuZ2U=", 9010, commitment)
	signature, err := identity.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	extensionNonce := "ZXh0LW5vbmNl"

	script := filepath.Join(t.TempDir(), "check.mjs")
	if err := os.WriteFile(script, []byte(nodeCheckScript), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, script,
		Path(home), payload, signature, extensionNonce, daemonNonce).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, output)
	}
	var got struct {
		OK     bool   `json:"ok"`
		Code   string `json:"code"`
		SPKI   string `json:"spki"`
		Commit string `json:"commitment"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("node output %q: %v", output, err)
	}
	if !got.OK {
		t.Fatal("node did not verify the Go signature")
	}
	if got.SPKI != identity.PublicKey {
		t.Fatal("node derived a different SPKI from the same private key")
	}
	if want := Code(identity.PublicKey, extensionNonce, daemonNonce); got.Code != want {
		t.Fatalf("node code %q, Go code %q", got.Code, want)
	}
	if got.Commit != commitment {
		t.Fatalf("node commitment %q, Go commitment %q", got.Commit, commitment)
	}
}

// TestNodeIdentityLoads checks the other direction: a key Node made is one we
// can sign with and pair against.
func TestNodeIdentityLoads(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	home := t.TempDir()
	script := `const {generateKeyPairSync}=require("node:crypto");const fs=require("node:fs");
const {publicKey,privateKey}=generateKeyPairSync("ec",{namedCurve:"prime256v1"});
fs.writeFileSync(process.argv[1],JSON.stringify({publicKey:publicKey.export({type:"spki",format:"der"}).toString("base64"),privateKey:privateKey.export({type:"pkcs8",format:"pem"}).toString()},{mode:0o600}))`
	if output, err := exec.Command(node, "-e", script, Path(home)).CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, output)
	}
	identity, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := identity.Sign("payload")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(identity.PublicKey, "payload", signature)
	if err != nil || !ok {
		t.Fatalf("could not sign with a Node-made key: ok=%v err=%v", ok, err)
	}
}

const nodeCheckScript = `
import { createHash, createPrivateKey, createPublicKey, verify } from "node:crypto";
import { readFileSync } from "node:fs";

const [file, payload, signature, extensionNonce, daemonNonce] = process.argv.slice(2);
const identity = JSON.parse(readFileSync(file, "utf8"));
const key = createPrivateKey(identity.privateKey);
const ok = verify(
  "sha256",
  Buffer.from(payload, "utf8"),
  { key, dsaEncoding: "ieee-p1363" },
  Buffer.from(signature, "base64"),
);
function pairingCode(publicKey, extensionNonce, daemonNonce) {
  const digest = createHash("sha256")
    .update("browsermcp-pair-code-v1:" + publicKey + ":" + extensionNonce + ":" + daemonNonce)
    .digest();
  const digits = String(digest.readUInt32BE(0) % 1000000).padStart(6, "0");
  return digits.slice(0, 3) + " " + digits.slice(3);
}
const commitment = createHash("sha256")
  .update(Buffer.from(daemonNonce, "base64"))
  .digest("base64");
const spki = createPublicKey(key).export({ type: "spki", format: "der" }).toString("base64");
console.log(JSON.stringify({ ok, code: pairingCode(identity.publicKey, extensionNonce, daemonNonce), spki, commitment }));
`
