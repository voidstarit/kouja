package pki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/voidstarit/kouja/internal/fsutil"
)

type journalEntry struct {
	Serial    string    `json:"serial"`
	RevokedAt time.Time `json:"revoked_at"`
	NotAfter  time.Time `json:"not_after"`
	Reason    string    `json:"reason"`
}

type journal struct {
	path    string
	mu      sync.Mutex
	entries map[string]journalEntry
	healthy bool
}

func openJournal(path string) (*journal, error) {
	j := &journal{path: path, entries: make(map[string]journalEntry), healthy: true}
	data, err := os.ReadFile(path) //nolint:gosec // operator-configured journal path
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pki: read journal: %w", err)
	}
	if len(data) == 0 {
		return j, nil
	}
	lines := splitLines(data)
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry journalEntry
		if err := json.Unmarshal(line, &entry); err != nil || entry.Serial == "" {
			j.healthy = false
			continue
		}
		j.entries[entry.Serial] = entry
		if index == len(lines)-1 && data[len(data)-1] != '\n' {
			j.healthy = false
		}
	}
	return j, nil
}

func splitLines(data []byte) [][]byte {
	lines := make([][]byte, 0)
	start := 0
	for index, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:index])
			start = index + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func (j *journal) has(serial string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, ok := j.entries[serial]
	return ok
}

func (j *journal) append(entry journalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.healthy {
		return ErrJournalUnhealthy
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return fmt.Errorf("pki: create journal directory: %w", err)
	}
	file, err := os.OpenFile(j.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600) //nolint:gosec // configured journal path
	if err != nil {
		return fmt.Errorf("pki: open journal: %w", err)
	}
	info, statErr := file.Stat()
	if statErr == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := file.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			tail := make([]byte, info.Size())
			if _, err := file.ReadAt(tail, 0); err == nil {
				lines := splitLines(tail)
				if len(lines) == 0 || !json.Valid(lines[len(lines)-1]) {
					_ = file.Close()
					j.healthy = false
					return ErrJournalUnhealthy
				}
				if _, err := file.Write([]byte("\n")); err != nil {
					_ = file.Close()
					return fmt.Errorf("pki: separate journal tail: %w", err)
				}
			}
		}
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("pki: encode journal entry: %w", err)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("pki: append journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("pki: sync journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("pki: close journal: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(j.path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	j.entries[entry.Serial] = entry
	return nil
}

func (j *journal) snapshot(ctx context.Context, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	data, err := os.ReadFile(j.path) //nolint:gosec // configured journal path
	if errors.Is(err, os.ErrNotExist) {
		data = nil
	} else if err != nil {
		return fmt.Errorf("pki: read journal: %w", err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return ErrJournalUnhealthy
	}
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var entry journalEntry
		if err := json.Unmarshal(line, &entry); err != nil || entry.Serial == "" {
			return ErrJournalUnhealthy
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("pki: create snapshot directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".journal-*")
	if err != nil {
		return fmt.Errorf("pki: create journal snapshot: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, destination); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(destination)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (j *journal) compact(now time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var data []byte
	for _, entry := range j.entries {
		if !entry.NotAfter.After(now) {
			continue
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if err := fsutil.WriteFileAtomic(j.path, data, 0o600); err != nil && !errors.Is(err, fsutil.ErrDurabilityUncertain) {
		return err
	}
	j.healthy = true
	return nil
}

// SnapshotJournal implements the backup snapshot contract.
func (s *Service) SnapshotJournal(ctx context.Context, destination string) error {
	if s == nil || s.journal == nil {
		return ErrCAUnavailable
	}
	return s.journal.snapshot(ctx, destination)
}
