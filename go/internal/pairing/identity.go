// Package pairing owns the daemon's ECDSA P-256 pairing key and the values
// the extension checks: the signature over the hello challenge, the commitment
// to the daemon's nonce and the six-digit code both sides show.
//
// The formats are the extension's, not ours: the public key is base64 SPKI
// (DER), the private key a PKCS #8 PEM, signatures are base64 IEEE P1363
// (r || s, 32 bytes each) over SHA-256, and codes are derived exactly as
// pairingCode() in @browsermcp/protocol does. A change here is a protocol
// change: an extension built against the TypeScript daemon must pair with this
// one byte for byte.
package pairing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
)

// Product names the values in the pairing payload and the code, matching
// RELAY_PRODUCT in @browsermcp/protocol.
const Product = "browsermcp"

// Identity is ~/.browsermcp/identity.json: the public key as base64 SPKI and
// the private key as a PKCS #8 PEM, the same shape the TypeScript daemon
// writes.
type Identity struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`

	key *ecdsa.PrivateKey
}

// Path is identity.json in home.
func Path(home string) string { return filepath.Join(home, "identity.json") }

// LoadOrCreate reads the identity in home, or makes one and links it into
// place. Linking fails when another daemon created one at the same moment, so
// two daemons starting together agree on the key the extension paired with.
func LoadOrCreate(home string) (*Identity, error) {
	file := Path(home)
	identity, err := readIdentity(file)
	if err != nil {
		return nil, err
	}
	if identity != nil {
		return identity, nil
	}
	return createIdentity(file)
}

func readIdentity(file string) (*Identity, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var identity Identity
	if err := json.Unmarshal(data, &identity); err != nil {
		return nil, nil
	}
	if err := identity.parse(); err != nil {
		return nil, nil
	}
	return &identity, nil
}

func createIdentity(file string) (*Identity, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	identity := &Identity{
		PublicKey:  base64.StdEncoding.EncodeToString(publicDER),
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
		key:        privateKey,
	}
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return nil, err
	}
	temp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".*.tmp")
	if err != nil {
		return nil, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	// 0600 on POSIX. On Windows this only clears the read-only attribute; the
	// key inherits the directory's NTFS ACL (owner, SYSTEM, Administrators).
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return nil, err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return nil, err
	}
	if err := temp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(tempName, file); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Another daemon won the race; use its key.
			existing, readErr := readIdentity(file)
			if readErr == nil && existing != nil {
				return existing, nil
			}
			return identity, nil
		}
		// A filesystem without hard links: create the file exclusively instead.
		file, createErr := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			existing, readErr := readIdentity(file.Name())
			if readErr == nil && existing != nil {
				return existing, nil
			}
			return nil, createErr
		}
		if _, writeErr := file.Write(data); writeErr != nil {
			file.Close()
			return nil, writeErr
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, closeErr
		}
	}
	return identity, nil
}

func (i *Identity) parse() error {
	if i.PrivateKey == "" || i.PublicKey == "" {
		return errors.New("identity is incomplete")
	}
	block, _ := pem.Decode([]byte(i.PrivateKey))
	if block == nil {
		return errors.New("identity private key is not PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("identity private key: %w", err)
	}
	ecdsaKey, ok := key.(*ecdsa.PrivateKey)
	if !ok || ecdsaKey.Curve != elliptic.P256() {
		return errors.New("identity private key is not ECDSA P-256")
	}
	i.key = ecdsaKey
	return nil
}

// Sign returns the base64 IEEE P1363 signature of payload's UTF-8 bytes over
// SHA-256, which is what WebCrypto verifies in the extension.
func (i *Identity) Sign(payload string) (string, error) {
	if i.key == nil {
		if err := i.parse(); err != nil {
			return "", err
		}
	}
	digest := sha256.Sum256([]byte(payload))
	r, s, err := ecdsa.Sign(rand.Reader, i.key, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 2*32)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return base64.StdEncoding.EncodeToString(signature), nil
}

// Verify reports whether signature is a valid IEEE P1363 signature of payload
// by publicKey (base64 SPKI). Used by tests and by the pairing conformance
// fixtures.
func Verify(publicKey, payload, signature string) (bool, error) {
	publicDER, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return false, err
	}
	parsed, err := x509.ParsePKIXPublicKey(publicDER)
	if err != nil {
		return false, err
	}
	ecdsaKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return false, errors.New("public key is not ECDSA")
	}
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false, err
	}
	if len(raw) != 64 {
		return false, fmt.Errorf("signature is %d bytes, want 64 (IEEE P1363)", len(raw))
	}
	digest := sha256.Sum256([]byte(payload))
	return ecdsa.Verify(ecdsaKey, digest[:], new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:])), nil
}

// Payload is the string the daemon signs: it names the port the extension
// dialed and the commitment to the daemon's nonce, so a program on another
// port cannot relay the challenge.
func Payload(challenge string, port int, commitment string) string {
	return fmt.Sprintf("%s-pair-v1:%d:%s:%s", Product, port, challenge, commitment)
}

// Commitment is base64 SHA-256 of the daemon nonce's 32 bytes.
func Commitment(nonce string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return base64.StdEncoding.EncodeToString(digest[:]), nil
}

// NewNonce is base64 of 32 random bytes, like createPairingNonce().
func NewNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// Code is the six-digit short authentication string both sides show, as
// `482 913`: the first 4 bytes of the SHA-256 of
// `browsermcp-pair-code-v1:<publicKey>:<extensionNonce>:<daemonNonce>`, big
// endian, mod 1,000,000, zero-padded, split 3+3.
func Code(publicKey, extensionNonce, daemonNonce string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s-pair-code-v1:%s:%s:%s",
		Product, publicKey, extensionNonce, daemonNonce)))
	digits := fmt.Sprintf("%06d", binary.BigEndian.Uint32(digest[:4])%1_000_000)
	return digits[:3] + " " + digits[3:]
}
