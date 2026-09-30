package controller

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// readString reads one length-prefixed field (RFC 4253 wire format) from b.
func readString(t *testing.T, b []byte) (field, rest []byte) {
	t.Helper()
	if len(b) < 4 {
		t.Fatalf("wire data truncated")
	}
	n := binary.BigEndian.Uint32(b)
	if uint32(len(b)-4) < n {
		t.Fatalf("wire field truncated")
	}
	return b[4 : 4+n], b[4+n:]
}

// parseAuthorizedKey decodes an "ssh-ed25519 <base64> <comment>" line into the raw public key.
func parseAuthorizedKey(t *testing.T, line string) ed25519.PublicKey {
	t.Helper()
	fields := strings.Fields(line)
	if len(fields) != 3 || fields[0] != "ssh-ed25519" {
		t.Fatalf("authorized key %q: want 'ssh-ed25519 <key> <comment>'", line)
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		t.Fatalf("authorized key base64: %v", err)
	}
	keyType, rest := readString(t, blob)
	pub, rest := readString(t, rest)
	if string(keyType) != "ssh-ed25519" || len(pub) != ed25519.PublicKeySize || len(rest) != 0 {
		t.Fatalf("authorized key blob: want type + 32-byte key")
	}
	return ed25519.PublicKey(pub)
}

// parseOpenSSHPrivateKey decodes an unencrypted OpenSSH ed25519 private key.
func parseOpenSSHPrivateKey(t *testing.T, pemBytes []byte) ed25519.PrivateKey {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "OPENSSH PRIVATE KEY" {
		t.Fatalf("private key is not an OPENSSH PRIVATE KEY PEM block")
	}
	b, ok := bytes.CutPrefix(block.Bytes, append([]byte("openssh-key-v1"), 0))
	if !ok {
		t.Fatalf("private key has no openssh-key-v1 magic")
	}
	cipher, b := readString(t, b)
	kdf, b := readString(t, b)
	_, b = readString(t, b) // kdf options
	if string(cipher) != "none" || string(kdf) != "none" {
		t.Fatalf("cipher/kdf = %s/%s, want none/none", cipher, kdf)
	}
	if len(b) < 4 || binary.BigEndian.Uint32(b) != 1 {
		t.Fatalf("want exactly one key")
	}
	_, b = readString(t, b[4:]) // public key blob
	private, _ := readString(t, b)
	if len(private)%8 != 0 || len(private) < 8 || !bytes.Equal(private[:4], private[4:8]) {
		t.Fatalf("private section: bad padding or check ints")
	}
	keyType, rest := readString(t, private[8:])
	_, rest = readString(t, rest) // public key
	priv, _ := readString(t, rest)
	if string(keyType) != "ssh-ed25519" || len(priv) != ed25519.PrivateKeySize {
		t.Fatalf("private key: want a 64-byte ed25519 key")
	}
	return ed25519.PrivateKey(priv)
}

func TestBuildSSHKeySecret(t *testing.T) {
	secret, err := BuildSSHKeySecret("demo", "labs")
	if err != nil {
		t.Fatal(err)
	}
	if secret.Name != "lab-session-demo-ssh" || secret.Namespace != "labs" {
		t.Errorf("secret = %s/%s, want labs/lab-session-demo-ssh", secret.Namespace, secret.Name)
	}
	if secret.Labels["session-id"] != "demo" {
		t.Errorf("session-id label = %q, want demo", secret.Labels["session-id"])
	}

	priv := parseOpenSSHPrivateKey(t, secret.Data[SSHPrivateKeyKey])

	pubLine := string(secret.Data[SSHPublicKeyKey])
	if !strings.HasSuffix(pubLine, " dozlab-session-demo\n") {
		t.Errorf("public key line %q, want comment dozlab-session-demo", pubLine)
	}
	if pub := parseAuthorizedKey(t, pubLine); !bytes.Equal(pub, priv.Public().(ed25519.PublicKey)) {
		t.Error("public key doesn't match the private key")
	}

	other, err := BuildSSHKeySecret("demo", "labs")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(other.Data[SSHPrivateKeyKey], secret.Data[SSHPrivateKeyKey]) {
		t.Error("two calls produced the same key")
	}
}

func TestEnsureSSHKeySecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := &LabSession{
		ObjectMeta: metav1.ObjectMeta{Name: "s1", Namespace: "labs", UID: "uid-1"},
		Spec:       LabSessionSpec{SessionID: "demo"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	ctx := context.Background()
	key := types.NamespacedName{Name: "lab-session-demo-ssh", Namespace: "labs"}

	created, err := EnsureSSHKeySecret(ctx, c, scheme, session, "demo", "labs")
	if err != nil || !created {
		t.Fatalf("first call: created=%v err=%v, want created", created, err)
	}
	var first corev1.Secret
	if err := c.Get(ctx, key, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.OwnerReferences) != 1 || first.OwnerReferences[0].UID != "uid-1" || first.OwnerReferences[0].Controller == nil || !*first.OwnerReferences[0].Controller {
		t.Errorf("owner references = %+v, want the session as controller", first.OwnerReferences)
	}

	// A second reconcile keeps the existing key: the VM already trusts it.
	created, err = EnsureSSHKeySecret(ctx, c, scheme, session, "demo", "labs")
	if err != nil || created {
		t.Fatalf("second call: created=%v err=%v, want kept", created, err)
	}
	var second corev1.Secret
	if err := c.Get(ctx, key, &second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Data[SSHPrivateKeyKey], second.Data[SSHPrivateKeyKey]) {
		t.Error("second call replaced the key")
	}
}
