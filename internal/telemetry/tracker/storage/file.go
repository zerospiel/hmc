package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store interface {
	OpenWeekly(ctx context.Context) error
	Inc(ctx context.Context, metricKey string, delta int64) error
	Flush(ctx context.Context) error
}

type fileStore struct {
	dir       string
	filename  string
	mu        sync.Mutex
	data      map[string]int64
	dirty     bool
	weekStart time.Time
}

func NewFileStore(dir string) Store {
	return &fileStore{
		dir:  dir,
		data: make(map[string]int64),
	}
}

func (fs *fileStore) OpenWeekly(ctx context.Context) error {
	// determine ISO week
	now := time.Now().UTC()
	year, week := now.ISOWeek()
	start := isoWeekStart(year, week)

	if fs.weekStart.Equal(start) && fs.filename != "" {
		return nil // already open
	}

	fs.Flush(ctx) // flush old
	fs.data = make(map[string]int64)
	fs.weekStart = start
	fs.filename = fmt.Sprintf("%04d-W%02d.json", year, week)

	path := filepath.Join(fs.dir, fs.filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	// load existing
	var tmp map[string]int64
	dec := json.NewDecoder(f)
	if err := dec.Decode(&tmp); err == nil {
		fs.data = tmp
	}
	return nil
}

func (fs *fileStore) Inc(ctx context.Context, key string, delta int64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.data[key] += delta
	fs.dirty = true
	return nil
}

func (fs *fileStore) Flush(ctx context.Context) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.dirty || fs.filename == "" {
		return nil
	}
	tmpPath := filepath.Join(fs.dir, fs.filename+".tmp")
	finalPath := filepath.Join(fs.dir, fs.filename)

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(fs.data); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return err
	}
	fs.dirty = false
	return nil
}

// helper to get Monday of ISO week
func isoWeekStart(year, week int) time.Time {
	t := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	_, isoWeek := t.ISOWeek()
	delta := (week - isoWeek) * 24 * 7
	return t.AddDate(0, 0, delta-int(t.Weekday())+1)
}
