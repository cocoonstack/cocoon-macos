package qemu

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const (
	smbiosModel    = "iMac19,1"
	serialAlphabet = "ABCDEFGHIJKLMNPQRSTUVWXYZ0123456789" // Apple omits O (0/O ambiguity)
)

// SMBIOS is a per-VM Apple machine identity injected into OpenCore PlatformInfo/Generic so
// cloned VMs do not all share one identity. The values are format-valid + unique but NOT
// Apple-validated: iServices/App Store registration is the consumer's policy concern.
type SMBIOS struct {
	Model  string `json:"model"`  // SystemProductName (fixed; proven to boot Tahoe)
	Serial string `json:"serial"` // SystemSerialNumber
	MLB    string `json:"mlb"`    // board serial
	UUID   string `json:"uuid"`   // SystemUUID
	ROM    string `json:"rom"`    // 6-byte ROM as hex; also the guest en0 MAC
}

// MAC returns the ROM formatted as the guest NIC MAC (so en0 == ROM, as iServices expects).
func (s SMBIOS) MAC() string {
	b, err := hex.DecodeString(s.ROM)
	if err != nil || len(b) != 6 {
		return ""
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
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
	uuid, err := randUUID()
	if err != nil {
		return SMBIOS{}, err
	}
	rom, err := randROM()
	if err != nil {
		return SMBIOS{}, err
	}
	return SMBIOS{Model: smbiosModel, Serial: serial, MLB: mlb, UUID: uuid, ROM: rom}, nil
}

func randString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i, c := range b {
		b[i] = serialAlphabet[int(c)%len(serialAlphabet)]
	}
	return string(b), nil
}

func randUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func randROM() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[0] = (b[0] & 0xfe) | 0x02 // locally administered, unicast
	return hex.EncodeToString(b), nil
}
