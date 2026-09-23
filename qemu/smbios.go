package qemu

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"uuid"
)

const (
	smbiosModel    = "iMac19,1"
	serialAlphabet = "ABCDEFGHIJKLMNPQRSTUVWXYZ0123456789" // Apple omits O (0/O ambiguity)
)

// SMBIOS is a per-VM Apple machine identity injected into OpenCore PlatformInfo/Generic; format-valid and unique but NOT Apple-validated.
type SMBIOS struct {
	Model  string `json:"model"`  // SystemProductName (fixed; proven to boot Tahoe)
	Serial string `json:"serial"` // SystemSerialNumber
	MLB    string `json:"mlb"`    // board serial
	UUID   string `json:"uuid"`   // SystemUUID
	ROM    string `json:"rom"`    // 6-byte ROM as hex; default guest NIC MAC outside CNI
}

// RandomSMBIOS generates a unique per-VM identity (fixed model + random serial/MLB/UUID/ROM).
func RandomSMBIOS() (SMBIOS, error) {
	serial, err := randString(12)
	if err != nil {
		return SMBIOS{}, err
	}
	mlb, err := randString(17)
	if err != nil {
		return SMBIOS{}, err
	}
	rom, err := randROM()
	if err != nil {
		return SMBIOS{}, err
	}
	return SMBIOS{Model: smbiosModel, Serial: serial, MLB: mlb, UUID: strings.ToUpper(uuid.NewV4().String()), ROM: rom}, nil
}

// MAC returns the ROM formatted as the default guest NIC MAC; CNI supplies its own runtime MAC.
func (s SMBIOS) MAC() string {
	b, err := hex.DecodeString(s.ROM)
	if err != nil || len(b) != 6 {
		return ""
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

func randString(n int) (string, error) {
	b, err := randBytes(n)
	if err != nil {
		return "", err
	}
	for i, c := range b {
		b[i] = serialAlphabet[int(c)%len(serialAlphabet)]
	}
	return string(b), nil
}

func randROM() (string, error) {
	b, err := randBytes(6)
	if err != nil {
		return "", err
	}
	b[0] = (b[0] & 0xfe) | 0x02 // locally administered, unicast
	return hex.EncodeToString(b), nil
}

func randBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}
	return b, nil
}
