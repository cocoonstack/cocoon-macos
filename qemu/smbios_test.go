package qemu

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"testing"
)

func TestRandomSMBIOS(t *testing.T) {
	s, err := RandomSMBIOS()
	if err != nil {
		t.Fatalf("RandomSMBIOS: %v", err)
	}
	if s.Model != smbiosModel {
		t.Errorf("Model = %q, want %q", s.Model, smbiosModel)
	}
	if len(s.Serial) != 12 {
		t.Errorf("Serial %q len = %d, want 12", s.Serial, len(s.Serial))
	}
	if len(s.MLB) != 17 {
		t.Errorf("MLB %q len = %d, want 17", s.MLB, len(s.MLB))
	}
	alpha := regexp.MustCompile(`^[A-Z0-9]+$`)
	if !alpha.MatchString(s.Serial) || !alpha.MatchString(s.MLB) {
		t.Errorf("Serial/MLB not uppercase-alnum: %q %q", s.Serial, s.MLB)
	}
	if !regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-4[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12}$`).MatchString(s.UUID) {
		t.Errorf("UUID %q is not a v4 UUID", s.UUID)
	}
	rom, err := hex.DecodeString(s.ROM)
	if err != nil || len(rom) != 6 {
		t.Fatalf("ROM %q not 6 hex bytes: %v", s.ROM, err)
	}
	if rom[0]&0x02 == 0 || rom[0]&0x01 != 0 {
		t.Errorf("ROM[0] = %#x, want locally-administered unicast", rom[0])
	}
	if mac := s.MAC(); mac != fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", rom[0], rom[1], rom[2], rom[3], rom[4], rom[5]) {
		t.Errorf("MAC %q does not match ROM %q", mac, s.ROM)
	}
}

func TestRandomSMBIOSUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s, err := RandomSMBIOS()
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{s.Serial, s.MLB, s.UUID, s.ROM} {
			if seen[v] {
				t.Fatalf("collision on %q after %d iterations", v, i)
			}
			seen[v] = true
		}
	}
}
