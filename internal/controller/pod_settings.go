package controller

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/api/resource"
)

// PodSettings holds the controller-wide settings for lab session pods.
// None of the lab images are published to a public registry yet, so the
// images have no defaults and must be configured.
type PodSettings struct {
	// VMImage runs Firecracker; its own entrypoint (start-firecracker) boots the VM.
	VMImage string
	// InitImage writes the VM rootfs into the vm-kernels volume (dozlab-rootfs-manager init-setup).
	InitImage string
	// TerminalImage is the terminal sidecar that SSHes into the VM.
	TerminalImage string
	// SSHKeySecret is the Secret, in the session's namespace, holding the private key
	// whose public half is in the VM's authorized_keys.
	SSHKeySecret string
	// SSHKeySecretKey is the key inside SSHKeySecret.
	SSHKeySecretKey string
	// SSHUser is the user the terminal sidecar logs in as.
	SSHUser string
	// VMDiskSize is the size the rootfs is grown to (a Kubernetes quantity).
	VMDiskSize string
	// StorageClass is the StorageClass for the session PVCs. Empty uses the
	// cluster's default StorageClass.
	StorageClass string
}

// BindFlags registers the settings as flags. Each flag defaults to its
// environment variable, so either can be used.
func (s *PodSettings) BindFlags(fs *flag.FlagSet) {
	fs.StringVar(&s.VMImage, "vm-image", os.Getenv("DOZLAB_VM_IMAGE"),
		"Firecracker VM image (env DOZLAB_VM_IMAGE). Required.")
	fs.StringVar(&s.InitImage, "init-image", os.Getenv("DOZLAB_INIT_IMAGE"),
		"Rootfs init image (env DOZLAB_INIT_IMAGE). Required.")
	fs.StringVar(&s.TerminalImage, "terminal-image", os.Getenv("DOZLAB_TERMINAL_IMAGE"),
		"Terminal sidecar image (env DOZLAB_TERMINAL_IMAGE). Required.")
	fs.StringVar(&s.SSHKeySecret, "ssh-key-secret", envOr("DOZLAB_SSH_KEY_SECRET", "lab-ssh-key"),
		"Secret holding the VM SSH private key (env DOZLAB_SSH_KEY_SECRET).")
	fs.StringVar(&s.SSHKeySecretKey, "ssh-key-secret-key", envOr("DOZLAB_SSH_KEY_SECRET_KEY", "id_ed25519"),
		"Key in the SSH key Secret (env DOZLAB_SSH_KEY_SECRET_KEY).")
	fs.StringVar(&s.SSHUser, "ssh-user", envOr("DOZLAB_SSH_USER", "root"),
		"User the terminal sidecar logs into the VM as (env DOZLAB_SSH_USER).")
	fs.StringVar(&s.VMDiskSize, "vm-disk-size", envOr("DOZLAB_VM_DISK_SIZE", "4Gi"),
		"Size the VM rootfs is grown to (env DOZLAB_VM_DISK_SIZE).")
	fs.StringVar(&s.StorageClass, "storage-class", os.Getenv("DOZLAB_STORAGE_CLASS"),
		"StorageClass for session PVCs (env DOZLAB_STORAGE_CLASS). Empty uses the cluster default.")
}

// Validate reports missing or malformed settings.
func (s PodSettings) Validate() error {
	var errs []error
	if s.VMImage == "" {
		errs = append(errs, errors.New("vm image is required (--vm-image or DOZLAB_VM_IMAGE)"))
	}
	if s.InitImage == "" {
		errs = append(errs, errors.New("init image is required (--init-image or DOZLAB_INIT_IMAGE)"))
	}
	if s.TerminalImage == "" {
		errs = append(errs, errors.New("terminal image is required (--terminal-image or DOZLAB_TERMINAL_IMAGE)"))
	}
	if s.SSHKeySecret == "" || s.SSHKeySecretKey == "" {
		errs = append(errs, errors.New("ssh key secret and key are required"))
	}
	if s.SSHUser == "" {
		errs = append(errs, errors.New("ssh user is required"))
	}
	if q, err := resource.ParseQuantity(s.VMDiskSize); err != nil {
		errs = append(errs, fmt.Errorf("invalid vm disk size %q: %w", s.VMDiskSize, err))
	} else if q.Value() < 1<<20 {
		errs = append(errs, fmt.Errorf("vm disk size %q is below 1Mi", s.VMDiskSize))
	}
	return errors.Join(errs...)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
