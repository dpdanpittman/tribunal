package chain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// QueueEntry represents one failed real-time commit waiting to be retried
// at plan-close. We persist the full ExecuteMsg + the original failure so
// the operator can debug what went wrong without re-deriving it.
type QueueEntry struct {
	EnqueuedAt time.Time   `json:"enqueued_at"`
	PlanID     string      `json:"plan_id"`
	FindingID  string      `json:"finding_id,omitempty"`
	Reason     string      `json:"reason"`
	Msg        *ExecuteMsg `json:"msg"`
}

// Queue is an append-only JSONL retry log at <project>/.tribunal/chain-queue.jsonl.
// It serializes failed real-time commits for the hybrid settlement path.
// Successful retry leaves the entry in place; the queue is rotated on
// plan-close so old entries don't pile up forever.
type Queue struct {
	path string
}

// NewQueue returns a Queue rooted at the given path.
func NewQueue(path string) *Queue {
	return &Queue{path: path}
}

// DefaultQueuePath returns the conventional queue path inside a project's
// .tribunal/ directory.
func DefaultQueuePath(projectRoot string) string {
	return filepath.Join(projectRoot, ".tribunal", "chain-queue.jsonl")
}

// Enqueue appends a new retry entry. Safe to call from multiple
// goroutines on Linux because O_APPEND writes are atomic for sub-PIPE_BUF
// payloads; we accept slightly larger payloads because our entries are
// JSONL and only a single process writes at a time in practice.
func (q *Queue) Enqueue(e QueueEntry) error {
	if err := os.MkdirAll(filepath.Dir(q.path), 0o755); err != nil {
		return err
	}
	if e.EnqueuedAt.IsZero() {
		e.EnqueuedAt = time.Now().UTC()
	}
	data, err := json.Marshal(&e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(q.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data = append(data, '\n')
	_, err = f.Write(data)
	return err
}

// Drain returns and removes every entry for the given plan. Returns
// entries in enqueue order. If planID is empty, drains every entry.
func (q *Queue) Drain(planID string) ([]QueueEntry, error) {
	all, err := q.All()
	if err != nil {
		return nil, err
	}
	if planID == "" {
		_ = os.Remove(q.path)
		return all, nil
	}
	keep := all[:0]
	drained := []QueueEntry{}
	for _, e := range all {
		if e.PlanID == planID {
			drained = append(drained, e)
		} else {
			keep = append(keep, e)
		}
	}
	if err := q.rewrite(keep); err != nil {
		return nil, err
	}
	return drained, nil
}

// All reads every queued entry. Useful for `tribunal chain queue list`.
func (q *Queue) All() ([]QueueEntry, error) {
	data, err := os.ReadFile(q.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var entries []QueueEntry
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var e QueueEntry
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("queue parse: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (q *Queue) rewrite(entries []QueueEntry) error {
	if len(entries) == 0 {
		_ = os.Remove(q.path)
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(q.path), "chain-queue-*.jsonl")
	if err != nil {
		return err
	}
	for _, e := range entries {
		b, err := json.Marshal(&e)
		if err != nil {
			tmp.Close()
			_ = os.Remove(tmp.Name())
			return err
		}
		if _, err := tmp.Write(append(b, '\n')); err != nil {
			tmp.Close()
			_ = os.Remove(tmp.Name())
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), q.path)
}

