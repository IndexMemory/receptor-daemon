package service

import (
	"os/user"
	"testing"
)

func TestGuardAgainstRootPerUserInstallBlocksNonSystemAsRootWithoutSudoUser(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	err := guardAgainstRootPerUserInstallEUID(Options{System: false}, 0)
	if err == nil {
		t.Fatal("expected an error for a per-user install running as root with no resolvable invoking user")
	}
}

func TestGuardAgainstRootPerUserInstallAllowsNonSystemAsRootWithResolvableSudoUser(t *testing.T) {
	self, err := user.Current()
	if err != nil {
		t.Skip("no current user available in this environment")
	}
	t.Setenv("SUDO_USER", self.Username)
	if err := guardAgainstRootPerUserInstallEUID(Options{System: false}, 0); err != nil {
		t.Fatalf("expected no error for a per-user install running as root via a genuine sudo invocation, got %v", err)
	}
}

func TestGuardAgainstRootPerUserInstallAllowsNonSystemAsNonRoot(t *testing.T) {
	err := guardAgainstRootPerUserInstallEUID(Options{System: false}, 501)
	if err != nil {
		t.Fatalf("expected no error for a per-user install as a normal user, got %v", err)
	}
}

func TestGuardAgainstRootPerUserInstallAllowsSystemAsRoot(t *testing.T) {
	err := guardAgainstRootPerUserInstallEUID(Options{System: true}, 0)
	if err != nil {
		t.Fatalf("expected no error for a --system install as root, got %v", err)
	}
}
