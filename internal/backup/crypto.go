package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var backupMagic = []byte("KOUJA1")

// LoadBackupKey reads a 32-byte backup key from raw bytes, base64, or hex.
func LoadBackupKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-configured backup key
	if err != nil {
		return nil, fmt.Errorf("backup: read encryption key: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if decoded, err := base64.StdEncoding.DecodeString(text); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(text); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := hex.DecodeString(text); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(data) == 32 {
		return data, nil
	}
	if len(data) == 33 && data[32] == '\n' {
		return data[:32], nil
	}
	if len(data) == 34 && data[32] == '\r' && data[33] == '\n' {
		return data[:32], nil
	}
	return nil, fmt.Errorf("backup: encryption key must be 32 raw bytes, 32-byte base64, or 64 hex characters")
}

// EncryptFile encrypts src into dst using AES-256-GCM with a per-chunk nonce.
func EncryptFile(src, dst string, key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("backup: encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	input, err := os.Open(src) //nolint:gosec // generated archive path
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.CreateTemp(filepath.Dir(dst), ".kouja-encrypted-*")
	if err != nil {
		return err
	}
	temp := output.Name()
	defer func() { _ = os.Remove(temp) }()
	if err := output.Chmod(0o600); err != nil {
		_ = output.Close()
		return err
	}
	if _, err := output.Write(backupMagic); err != nil {
		_ = output.Close()
		return err
	}
	buffer := make([]byte, 1<<20)
	for {
		n, readErr := input.Read(buffer)
		if n > 0 {
			nonce := make([]byte, gcm.NonceSize())
			if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
				_ = output.Close()
				return err
			}
			sealed := gcm.Seal(nil, nonce, buffer[:n], nil)
			if _, err := output.Write(nonce); err != nil {
				_ = output.Close()
				return err
			}
			length := uint32(len(sealed)) //nolint:gosec // chunks are capped at 1 MiB
			if err := writeUint32(output, length); err != nil {
				_ = output.Close()
				return err
			}
			if _, err := output.Write(sealed); err != nil {
				_ = output.Close()
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = output.Close()
			return readErr
		}
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	return os.Rename(temp, dst)
}

// DecryptFile decrypts an EncryptFile archive into dst.
func DecryptFile(src, dst string, key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("backup: encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	input, err := os.Open(src) //nolint:gosec // operator-selected archive
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	magic := make([]byte, len(backupMagic))
	if _, err := io.ReadFull(input, magic); err != nil || string(magic) != string(backupMagic) {
		return fmt.Errorf("backup: archive is not an encrypted kouja backup")
	}
	output, err := os.CreateTemp(filepath.Dir(dst), ".kouja-decrypted-*")
	if err != nil {
		return err
	}
	temp := output.Name()
	defer func() { _ = os.Remove(temp) }()
	if err := output.Chmod(0o600); err != nil {
		_ = output.Close()
		return err
	}
	for {
		nonce := make([]byte, gcm.NonceSize())
		if _, err := io.ReadFull(input, nonce); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = output.Close()
			return err
		}
		length, err := readUint32(input)
		if err != nil {
			_ = output.Close()
			return err
		}
		sealed := make([]byte, length)
		if _, err := io.ReadFull(input, sealed); err != nil {
			_ = output.Close()
			return err
		}
		plain, err := gcm.Open(nil, nonce, sealed, nil)
		if err != nil {
			_ = output.Close()
			return fmt.Errorf("backup: decrypt archive: %w", err)
		}
		if _, err := output.Write(plain); err != nil {
			_ = output.Close()
			return err
		}
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	return os.Rename(temp, dst)
}

func writeUint32(writer io.Writer, value uint32) error {
	buffer := make([]byte, 4)
	binary.BigEndian.PutUint32(buffer, value)
	_, err := writer.Write(buffer)
	return err
}

func readUint32(reader io.Reader) (uint32, error) {
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buffer), nil
}
