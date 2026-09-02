package sno

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/term"
)

// decryptPassword decrypts an OpenSSL "enc -aes-256-cbc" password file of
// the form "Salted__" + 8-byte salt + PKCS#7 ciphertext. It first tries the
// modern OpenSSL 3.x key derivation (PBKDF2-HMAC-SHA256, 2048 iterations)
// and falls back to the legacy EVP_BytesToKey (MD5) derivation, mirroring
// the python implementation that invoked openssl with -pbkdf2 then without.
//
// A nil/empty passphrase prompts on the controlling terminal (getpass).
func decryptPassword(encFile, passphrase string) (string, error) {
	path := encFile
	if !filepath.IsAbs(path) {
		if _, err := os.Stat(path); err != nil {
			return "", NewError("%s not found", path)
		}
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
	}
	if passphrase == "" {
		pw, err := readTerminalPassword("Enter passphrase to decrypt iDRAC password: ")
		if err != nil {
			return "", NewError("read passphrase: %v", err)
		}
		passphrase = pw
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return "", NewError("read %s: %v", path, err)
	}
	const header = "Salted__"
	if len(raw) < len(header)+8+16 || string(raw[:len(header)]) != header {
		return "", NewError("%s: not an openssl aes-256-cbc file (missing Salted__ header)", path)
	}
	salt := raw[len(header) : len(header)+8]
	ciphertext := raw[len(header)+8:]

	// 1. Modern: PBKDF2-HMAC-SHA256.
	if out, err := decryptAESEnc(raw, salt, ciphertext, derivePBKDF2(passphrase, salt)); err == nil {
		return out, nil
	}
	// 2. Legacy: EVP_BytesToKey (MD5).
	if out, err := decryptAESEnc(raw, salt, ciphertext, deriveEVPKeyMaterial(passphrase, salt, 48)); err == nil {
		return out, nil
	}
	return "", NewError("decrypt failed (wrong passphrase or corrupt file)")
}

// decryptAESEnc runs AES-256-CBC with PKCS#7 unpadding; it reports an error
// on padding/validation issues so callers can try the other derivation.
func decryptAESEnc(_ []byte, salt, ciphertext []byte, keyIV []byte) (string, error) {
	block, err := aes.NewCipher(keyIV[:32])
	if err != nil {
		return "", err
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("ciphertext not block aligned")
	}
	out := make([]byte, len(ciphertext))
	cbc := cipher.NewCBCDecrypter(block, keyIV[32:48])
	cbc.CryptBlocks(out, ciphertext)
	n, err := pkcs7Unpad(out, aes.BlockSize)
	if err != nil {
		return "", err
	}
	return string(out[:n]), nil
}

// derivePBKDF2 returns 48 bytes (32 key + 16 IV) using
// PBKDF2-HMAC-SHA256, 2048 iterations (OpenSSL 3.x defaults).
func derivePBKDF2(passphrase string, salt []byte) []byte {
	return pbkdf2.Key([]byte(passphrase), salt, 2048, 48, sha256.New)
}

// deriveEVPKeyMaterial implements OpenSSL EVP_BytesToKey with the MD5 hash
// (legacy default for openssl enc): D_i = MD5(D_{i-1} + S + salt), 64 bytes.
func deriveEVPKeyMaterial(passphrase string, salt []byte, n int) []byte {
	data := make([]byte, 0, 64)
	prev := []byte(nil)
	for len(data) < n {
		h := md5.New()
		h.Write(prev)
		h.Write([]byte(passphrase))
		h.Write(salt)
		prev = h.Sum(nil)
		data = append(data, prev...)
	}
	return data[:n]
}

// pkcs7Unpad validates and strips PKCS#7 padding. It rejects both
// non-1..blockSize padding and padding that does not match the whole tail.
func pkcs7Unpad(b []byte, blockSize int) (int, error) {
	if len(b) == 0 {
		return 0, fmt.Errorf("empty padded data")
	}
	pad := int(b[len(b)-1])
	if pad == 0 || pad > blockSize || pad > len(b) {
		return 0, fmt.Errorf("invalid padding %d", pad)
	}
	for i := len(b) - pad; i < len(b); i++ {
		if int(b[i]) != pad {
			return 0, fmt.Errorf("invalid padding bytes")
		}
	}
	return len(b) - pad, nil
}

// readTerminalPassword reads a password from the controlling terminal with
// echo disabled (getpass equivalent).
func readTerminalPassword(prompt string) (string, error) {
	if os.Getenv("CI") != "" {
		// Never block on a prompt in CI.
		return "", fmt.Errorf("no passphrase supplied (set IDRAC_PW)")
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("no controlling terminal and no passphrase supplied")
	}
	fmt.Fprint(os.Stderr, prompt)
	defer fmt.Fprintln(os.Stderr)
	b, err := term.ReadPassword(fd)
	return strings.TrimSpace(string(b)), err
}

// encryptPasswordFile is the test helper that produces the same file format
// as `openssl enc -aes-256-cbc -pbkdf2 -salt`.
func encryptPasswordFilePBKDF2(plaintext, passphrase string) []byte {
	salt := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	keyIV := derivePBKDF2(passphrase, salt)
	block, _ := aes.NewCipher(keyIV[:32])
	padded := pkcs7Pad([]byte(plaintext), aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, keyIV[32:48]).CryptBlocks(out, padded)
	res := make([]byte, 0, 8+8+len(out))
	res = append(res, []byte("Salted__")...)
	res = append(res, salt...)
	res = append(res, out...)
	return res
}

// encryptPasswordFile is the test helper for the legacy derivation
// (openssl enc -aes-256-cbc without -pbkdf2).
func encryptPasswordFileLegacy(plaintext, passphrase string) []byte {
	salt := []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}
	keyIV := deriveEVPKeyMaterial(passphrase, salt, 48)
	block, _ := aes.NewCipher(keyIV[:32])
	padded := pkcs7Pad([]byte(plaintext), aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, keyIV[32:48]).CryptBlocks(out, padded)
	res := make([]byte, 0, 8+8+len(out))
	res = append(res, []byte("Salted__")...)
	res = append(res, salt...)
	res = append(res, out...)
	return res
}

// pkcs7Pad pads b to a multiple of blockSize with PKCS#7 padding.
func pkcs7Pad(b []byte, blockSize int) []byte {
	pad := blockSize - len(b)%blockSize
	res := append(make([]byte, 0, len(b)+pad), b...)
	for i := 0; i < pad; i++ {
		res = append(res, byte(pad))
	}
	return res
}
