package service

import "testing"

func TestGuardAgainstRootPerUserInstallBlocksNonSystemAsRoot(t *testing.T) {
	err := guardAgainstRootPerUserInstallEUID(Options{System: false}, 0)
	if err == nil {
		t.Fatal("expected an error for a per-user install running as root")
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
