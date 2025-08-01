package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Collector interface.
type Collector interface {
	Collect(context.Context) error
	Flush(context.Context) error
	Close(context.Context) error
}

// File encapsulates the current day's temp file, its date, and provides rotation/flush logic.
type File struct {
	mu      sync.Mutex
	today   time.Time // midnight UTC of the current day this file represents
	f       *os.File  // underlying temp file (telemetry-<day>.json.tmp)
	baseDir string    // directory where files live
}

// NewFile initializes the file for today, creating the temp file if needed.
func NewFile(baseDir string) (*File, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir baseDir: %w", err)
	}
	y, m, d := time.Now().UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	f := &File{today: today, baseDir: baseDir}
	if err := f.openTempLocked(); err != nil {
		return nil, err
	}
	return f, nil
}

// openTempLocked must be called under lock. It opens the temp file for f.today.
func (f *File) openTempLocked() error {
	// build path
	d := f.today.Format(time.DateOnly)
	tempPath := filepath.Join(f.baseDir, fmt.Sprintf("telemetry-%s.json.tmp", d))
	var err error
	f.f, err = os.OpenFile(tempPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening temp file: %w", err)
	}
	return nil
}

// EnsureOpen makes sure the file is ready for writing (rotates if needed and opens if missing).
func (f *File) EnsureOpen() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.rotateIfNeededLocked(); err != nil {
		return err
	}
	if f.f == nil {
		return f.openTempLocked()
	}
	return nil
}

// rotateIfNeededLocked performs rotation if the UTC day has advanced. Caller must hold lock.
func (f *File) rotateIfNeededLocked() error {
	y, m, d := time.Now().UTC().Date()
	newDay := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	if newDay.Equal(f.today) {
		return nil // nothing to do
	}
	// close existing file if open
	if f.f != nil {
		_ = f.f.Sync()
		_ = f.f.Close()
	}
	// rename old temp to final summary
	oldDay := f.today.Format(time.DateOnly)
	tempPath := filepath.Join(f.baseDir, fmt.Sprintf("telemetry-%s.json.tmp", oldDay))
	finalPath := filepath.Join(f.baseDir, fmt.Sprintf("%s.json", oldDay))
	if _, err := os.Stat(tempPath); err == nil {
		_ = os.Rename(tempPath, finalPath)
	}
	// switch to new day
	f.today = newDay
	// open new temp file for the new day
	tempPathNew := filepath.Join(f.baseDir, fmt.Sprintf("telemetry-%s.json.tmp", f.today.Format(time.DateOnly)))
	var err error
	f.f, err = os.OpenFile(tempPathNew, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening new temp file after rotation: %w", err)
	}
	return nil
}

// Append writes a byte slice to the temp file (with newline) and flushes.
func (f *File) Append(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.f == nil {
		if err := f.openTempLocked(); err != nil {
			return err
		}
	}
	if _, err := f.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.f.Sync()
}

// Flush syncs the underlying file to disk (does not rotate).
func (f *File) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.f == nil {
		return nil
	}
	return f.f.Sync()
}

// CloseAndRotate flushes, closes, and rotates the current temp file into final summary unconditionally.
func (f *File) CloseAndRotate() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.f != nil {
		if err := f.f.Sync(); err != nil {
			return err
		}
		if err := f.f.Close(); err != nil {
			return err
		}
	}
	oldDay := f.today.Format(time.DateOnly)
	tempPath := filepath.Join(f.baseDir, fmt.Sprintf("telemetry-%s.json.tmp", oldDay))
	finalPath := filepath.Join(f.baseDir, fmt.Sprintf("%s.json", oldDay))
	if _, err := os.Stat(tempPath); err == nil {
		if err := os.Rename(tempPath, finalPath); err != nil {
			return err
		}
	}
	f.f = nil
	return nil
}

// ClusterEntry holds per-cluster in-memory bucketed counters and scrapes.
type ClusterEntry struct {
	mu      sync.Mutex
	Buckets map[string]uint64
	Scrapes uint64
}

func newClusterEntry() *ClusterEntry {
	return &ClusterEntry{Buckets: make(map[string]uint64)}
}

func (ce *ClusterEntry) increment(bucket string, delta uint64) {
	ce.mu.Lock()
	defer ce.mu.Unlock()
	if bucket == "scrapes" {
		ce.Scrapes += delta
		return
	}
	ce.Buckets[bucket] += delta
}

// LocalCollector orchestrates, but Collect is intentionally unimplemented pending your signal.

type LocalCollector struct {
	file    *File
	entries map[string]*ClusterEntry // per-cluster in-memory state
}

func NewLocalCollectorWithFile(baseDir string) (*LocalCollector, error) {
	f, err := NewFile(baseDir)
	if err != nil {
		return nil, err
	}
	return &LocalCollector{file: f, entries: make(map[string]*ClusterEntry)}, nil
}

func (l *LocalCollector) getOrCreateEntry(cluster string) *ClusterEntry {
	if e, ok := l.entries[cluster]; ok {
		return e
	}
	e := newClusterEntry()
	l.entries[cluster] = e
	return e
}

func (l *LocalCollector) Collect(ctx context.Context) error {
	// TODO: implement scraping logic. Should start by calling l.file.EnsureOpen()
	return nil
}

func (l *LocalCollector) Flush(ctx context.Context) error {
	return l.file.Flush()
}

func (l *LocalCollector) Close(ctx context.Context) error {
	return l.file.CloseAndRotate()
}

// buckets
func bucketNodeCount(n int) string {
	switch {
	case n <= 10:
		return "node.count:1-10"
	case n <= 50:
		return "node.count:11-50"
	case n <= 100:
		return "node.count:51-100"
	default:
		return "node.count:101+"
	}
}

func bucketUserServiceCount(n int) string {
	switch {
	case n == 0:
		return "userServiceCount:0"
	case n <= 5:
		return "userServiceCount:1-5"
	case n <= 10:
		return "userServiceCount:6-10"
	case n <= 25:
		return "userServiceCount:11-25"
	default:
		return "userServiceCount:26+"
	}
}

func bucketCPU(millicores int) string {
	switch {
	case millicores <= 4000:
		return "node.cpu.total:0-4k"
	case millicores <= 8000:
		return "node.cpu.total:4k-8k"
	case millicores <= 16000:
		return "node.cpu.total:8k-16k"
	default:
		return "node.cpu.total:16k+"
	}
}

func bucketMemory(bytes uint64) string {
	const GB = 1024 * 1024 * 1024
	switch {
	case bytes <= 16*GB:
		return "node.memory.bytes:0-16GB"
	case bytes <= 32*GB:
		return "node.memory.bytes:16-32GB"
	case bytes <= 64*GB:
		return "node.memory.bytes:32-64GB"
	default:
		return "node.memory.bytes:64GB+"
	}
}

func bucketGPUTotal(gpu int) string {
	switch {
	case gpu == 0:
		return "node.gpu.total:0"
	case gpu <= 2:
		return "node.gpu.total:1-2"
	case gpu <= 8:
		return "node.gpu.total:3-8"
	default:
		return "node.gpu.total:9+"
	}
}

var kubeVersionBuckets = map[string]struct{}{
	"1.29": {},
	"1.30": {},
	"1.31": {},
	"1.33": {},
	"1.34": {},
	"1.35": {},
	"1.36": {},
}

func bucketKubeVersion(v string) string {
	if _, ok := kubeVersionBuckets[v]; ok {
		return fmt.Sprintf("node.info.kubeVersion:%s", v)
	}
	return "node.info.kubeVersion:other"
}

func bucketOS(osname string) string {
	switch strings.ToLower(osname) {
	case "linux":
		return "node.info.os:linux"
	case "windows":
		return "node.info.os:windows"
	default:
		return "node.info.os:other"
	}
}

func bucketArch(arch string) string {
	switch strings.ToLower(arch) {
	case "amd64":
		return "node.info.arch:amd64"
	case "arm64":
		return "node.info.arch:arm64"
	default:
		return "node.info.arch:other"
	}
}

func bucketProvider(p string) string {
	switch strings.ToLower(p) {
	case "aws":
		return "providers:aws"
	case "azure":
		return "providers:azure"
	case "openstack":
		return "providers:openstack"
	case "gcp":
		return "providers:gcp"
	default:
		return "providers:other"
	}
}

func bucketSyncMode(mode string) string {
	switch strings.ToLower(mode) {
	case "continuous":
		return "syncMode:Continuous"
	case "manual":
		return "syncMode:Manual"
	default:
		return fmt.Sprintf("syncMode:%s", mode)
	}
}

func bucketPodsWithGPURequests(n int) string {
	switch {
	case n == 0:
		return "pods.with_gpu_requests:0"
	case n <= 4:
		return "pods.with_gpu_requests:1-4"
	case n <= 16:
		return "pods.with_gpu_requests:5-16"
	default:
		return "pods.with_gpu_requests:17+"
	}
}

func bucketKubevirtVMIs(n int) string {
	switch {
	case n == 0:
		return "kubevirt.vmis:0"
	case n <= 5:
		return "kubevirt.vmis:1-5"
	case n <= 10:
		return "kubevirt.vmis:6-10"
	default:
		return "kubevirt.vmis:11+"
	}
}

func bucketGPUBytesCapacity(typ string, bytes uint64) string {
	const GB = 1024 * 1024 * 1024
	prefix := fmt.Sprintf("gpu.%s.bytes_capacity:", typ)
	switch {
	case bytes == 0:
		return prefix + "0"
	case bytes <= 4*GB:
		return prefix + "1-4GB"
	case bytes <= 8*GB:
		return prefix + "5-8GB"
	default:
		return prefix + "9GB+"
	}
}

func bucketGPUBytesUsed(typ string, bytes uint64) string {
	const GB = 1024 * 1024 * 1024
	prefix := fmt.Sprintf("gpu.%s.bytes:", typ)
	switch {
	case bytes == 0:
		return prefix + "0"
	case bytes <= 4*GB:
		return prefix + "1-4GB"
	case bytes <= 8*GB:
		return prefix + "5-8GB"
	default:
		return prefix + "9GB+"
	}
}
