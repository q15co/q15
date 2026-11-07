package subagent

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/atomicfile"
)

type namePool struct {
	locale string
	given  []string
	family []string
}

const nameStride = 7919

func sessionNameCount() uint64 {
	var count uint64
	for _, pool := range sessionNamePools {
		count += uint64(len(pool.given) * len(pool.family))
	}
	return count
}

func sessionID(sequence uint64) string {
	// A stride coprime to the catalog size visits every pair exactly once
	// before repeating, spreading consecutive sessions across locales.
	total := sessionNameCount()
	index := ((sequence - 1) % total) * nameStride % total
	for _, pool := range sessionNamePools {
		count := uint64(len(pool.given) * len(pool.family))
		if index < count {
			familyCount := uint64(len(pool.family))
			return fmt.Sprintf("%s-%s-%d", pool.given[index/familyCount],
				pool.family[index%familyCount], sequence)
		}
		index -= count
	}
	panic("subagent name catalog is empty")
}

func (m *Manager) allocateIDLocked() (string, error) {
	if m.next == math.MaxUint64 {
		return "", errors.New("subagent session sequence is exhausted")
	}
	m.next++
	// Reserve the sequence before creating a record or launching work. A
	// failed launch can skip a number, but retention must never free one.
	if m.records != nil {
		if err := m.records.saveSequence(m.next); err != nil {
			return "", err
		}
	}
	return sessionID(m.next), nil
}

func (s *recordStore) saveSequence(sequence uint64) error {
	if err := atomicfile.WriteBytes(filepath.Join(s.root, ".sequence"),
		[]byte(strconv.FormatUint(sequence, 10)+"\n")); err != nil {
		return fmt.Errorf("reserve subagent session sequence: %w", err)
	}
	return nil
}

func (s *recordStore) loadSequence(records []sessionRecord) (uint64, error) {
	var highest uint64
	for _, record := range records {
		// UUID-based records predate the durable sequence and stay readable.
		if strings.HasPrefix(record.ID, "subagent-") {
			continue
		}
		sequence, err := strconv.ParseUint(record.ID[strings.LastIndex(record.ID, "-")+1:], 10, 64)
		if err != nil || sequence == 0 {
			return 0, fmt.Errorf("invalid subagent session sequence in %q", record.ID)
		}
		highest = max(highest, sequence)
	}
	file, err := os.Open(filepath.Join(s.root, ".sequence"))
	if os.IsNotExist(err) {
		if highest != 0 {
			return 0, errors.New(
				"subagent session sequence is missing; refusing to reuse session ids",
			)
		}
		return 0, s.saveSequence(0)
	}
	if err != nil {
		return 0, fmt.Errorf("open subagent session sequence: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 32))
	if err != nil {
		return 0, fmt.Errorf("read subagent session sequence: %w", err)
	}
	sequence, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || len(data) >= 32 || sequence < highest {
		return 0, errors.New("invalid subagent session sequence; refusing to reuse session ids")
	}
	return sequence, nil
}
