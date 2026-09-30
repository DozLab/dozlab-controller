package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Each lab session gets its own SSH key pair, stored in a Secret owned by the session.
// The public half goes to the init-rootfs container, which writes it into the VM's
// cloud-init seed (root's authorized_keys); the private half goes to the terminal sidecar.
const (
	SSHPrivateKeyKey = "id_ed25519"
	SSHPublicKeyKey  = "id_ed25519.pub"
)

// SSHKeySecretName is the name of a session's SSH key Secret.
func SSHKeySecretName(sessionID string) string {
	return fmt.Sprintf("lab-session-%s-ssh", sessionID)
}

// BuildSSHKeySecret generates a new ed25519 key pair for the session. The private key is in
// OpenSSH's format, which both the terminal sidecar (golang.org/x/crypto/ssh) and `ssh -i` read.
func BuildSSHKeySecret(sessionID, namespace string) (*corev1.Secret, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ssh key: %w", err)
	}
	comment := "dozlab-session-" + sessionID
	privPEM, err := openSSHPrivateKey(pub, priv, comment)
	if err != nil {
		return nil, fmt.Errorf("encode ssh key: %w", err)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SSHKeySecretName(sessionID),
			Namespace: namespace,
			Labels:    map[string]string{"app": "lab-environment", "session-id": sessionID},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			SSHPrivateKeyKey: privPEM,
			SSHPublicKeyKey:  authorizedKey(pub, comment),
		},
	}, nil
}

// sshString appends a length-prefixed field (RFC 4253 wire format).
func sshString(b, field []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(field)))
	return append(b, field...)
}

// publicKeyBlob is the wire-format ed25519 public key: key type, then key.
func publicKeyBlob(pub ed25519.PublicKey) []byte {
	return sshString(sshString(nil, []byte("ssh-ed25519")), pub)
}

// authorizedKey formats an ed25519 public key as an authorized_keys line.
func authorizedKey(pub ed25519.PublicKey, comment string) []byte {
	return []byte("ssh-ed25519 " + base64.StdEncoding.EncodeToString(publicKeyBlob(pub)) + " " + comment + "\n")
}

// openSSHPrivateKey encodes an unencrypted ed25519 key in OpenSSH's private key format
// (PROTOCOL.key in the OpenSSH sources): magic, cipher/kdf "none", one public key, then the
// private section (two matching check ints, the key, the comment, padding to 8 bytes).
func openSSHPrivateKey(pub ed25519.PublicKey, priv ed25519.PrivateKey, comment string) ([]byte, error) {
	var check [4]byte
	if _, err := rand.Read(check[:]); err != nil {
		return nil, err
	}
	private := append(check[:], check[:]...)
	private = sshString(private, []byte("ssh-ed25519"))
	private = sshString(private, pub)
	private = sshString(private, priv) // 64 bytes: seed followed by the public key
	private = sshString(private, []byte(comment))
	for i := byte(1); len(private)%8 != 0; i++ {
		private = append(private, i)
	}

	b := append([]byte("openssh-key-v1"), 0)
	b = sshString(b, []byte("none")) // cipher
	b = sshString(b, []byte("none")) // kdf
	b = sshString(b, nil)            // kdf options
	b = binary.BigEndian.AppendUint32(b, 1)
	b = sshString(b, publicKeyBlob(pub))
	b = sshString(b, private)
	return pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: b}), nil
}

// EnsureSSHKeySecret creates the session's SSH key Secret, owned by owner, unless it exists.
// An existing Secret is kept: the VM of a running session already trusts its key. It only
// creates (no get), so the controller needs no read or watch access to Secrets.
func EnsureSSHKeySecret(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner metav1.Object, sessionID, namespace string) (created bool, err error) {
	name := SSHKeySecretName(sessionID)
	secret, err := BuildSSHKeySecret(sessionID, namespace)
	if err != nil {
		return false, err
	}
	if err := controllerutil.SetControllerReference(owner, secret, scheme); err != nil {
		return false, fmt.Errorf("set owner reference on ssh key secret: %w", err)
	}
	if err := c.Create(ctx, secret); err != nil {
		if errors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("create ssh key secret %s: %w", name, err)
	}
	return true, nil
}
