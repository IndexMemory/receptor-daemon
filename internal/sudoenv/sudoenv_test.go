package sudoenv

import (
	"os/user"
	"strconv"
	"testing"
)

func TestRealUserForEUIDUsesCurrentProcessWhenNotRoot(t *testing.T) {
	home, uid, err := RealUserForEUID(501) // any non-zero euid
	if err != nil {
		t.Fatal(err)
	}
	if home == "" {
		t.Error("expected a non-empty home directory")
	}
	if uid != 501 {
		t.Errorf("expected uid 501 (the euid passed in), got %d", uid)
	}
}

func TestRealUserForEUIDResolvesSudoUserWhenRoot(t *testing.T) {
	self, err := user.Current()
	if err != nil {
		t.Skip("no current user available in this environment")
	}
	t.Setenv("SUDO_USER", self.Username)

	home, uid, err := RealUserForEUID(0)
	if err != nil {
		t.Fatal(err)
	}
	if home != self.HomeDir {
		t.Errorf("expected home %q (self's, via SUDO_USER), got %q", self.HomeDir, home)
	}
	wantUID, err := strconv.Atoi(self.Uid)
	if err != nil {
		t.Skip("could not parse self uid")
	}
	if uid != wantUID {
		t.Errorf("expected uid %d, got %d", wantUID, uid)
	}
}

func TestRealUserForEUIDErrorsWhenRootWithoutSudoUser(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	if _, _, err := RealUserForEUID(0); err == nil {
		t.Fatal("expected an error when running as root with no SUDO_USER set")
	}
}

func TestRealUserForEUIDErrorsOnUnknownSudoUser(t *testing.T) {
	t.Setenv("SUDO_USER", "this-user-almost-certainly-does-not-exist-12345")
	if _, _, err := RealUserForEUID(0); err == nil {
		t.Fatal("expected an error when SUDO_USER doesn't resolve to a real account")
	}
}

func TestUsernameForEUIDResolvesSudoUserWhenRoot(t *testing.T) {
	t.Setenv("SUDO_USER", "someone")
	got, err := UsernameForEUID(0)
	if err != nil {
		t.Fatal(err)
	}
	if got != "someone" {
		t.Errorf("expected %q, got %q", "someone", got)
	}
}

func TestUsernameForEUIDErrorsWhenRootWithoutSudoUser(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	if _, err := UsernameForEUID(0); err == nil {
		t.Fatal("expected an error when running as root with no SUDO_USER set")
	}
}
