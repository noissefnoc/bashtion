package bashtion

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseCgroupV2Max(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected int
	}{
		{"max default", "max 100000\n", 0},
		{"2 cores", "200000 100000\n", 2},
		{"0.5 cores ceil to 1", "50000 100000\n", 1},
		{"invalid format", "invalid\n", 0},
		{"empty", "", 0},
		{"zero period", "100000 0\n", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "cpu.max")
			if err := os.WriteFile(file, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("failed to write test file: %v", err)
			}
			got := parseCgroupV2Max(tt.content)
			if got != tt.expected {
				t.Errorf("parseCgroupV2Max() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestParseCgroupV1Quota(t *testing.T) {
	tests := []struct {
		name     string
		quota    string
		period   string
		expected int
	}{
		{"unlimited quota", "-1\n", "100000\n", 0},
		{"4 cores", "400000\n", "100000\n", 4},
		{"1 core", "100000\n", "100000\n", 1},
		{"zero period", "100000\n", "0\n", 0},
		{"invalid quota", "abc\n", "100000\n", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCgroupV1Quota(tt.quota, tt.period)
			if got != tt.expected {
				t.Errorf("parseCgroupV1Quota() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestDefaultJobs(t *testing.T) {
	jobs := DefaultJobs()
	if jobs < 1 {
		t.Errorf("DefaultJobs() = %d, expected >= 1", jobs)
	}
	if jobs > runtime.NumCPU() && runtime.NumCPU() > 0 {
		t.Errorf("DefaultJobs() = %d, expected <= runtime.NumCPU() (%d)", jobs, runtime.NumCPU())
	}
}

func TestDetectCgroupCPUQuotaFrom_SubpathFallback(t *testing.T) {
	// Cgroup v2 subpath lookup
	v2Root := t.TempDir()
	v2Sub := filepath.Join(v2Root, "sys", "fs", "cgroup", "kubepods", "pod1")
	if err := os.MkdirAll(v2Sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v2Root, "sys", "fs", "cgroup", "cpu.max"), []byte("max 100000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v2Sub, "cpu.max"), []byte("200000 100000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	procV2 := filepath.Join(v2Root, "proc_cgroup")
	if err := os.WriteFile(procV2, []byte("0::/kubepods/pod1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detectCgroupCPUQuotaFrom(procV2, filepath.Join(v2Root, "sys", "fs", "cgroup")); got != 2 {
		t.Errorf("cgroup v2 subpath quota = %d, want 2", got)
	}

	// Cgroup v1 subpath lookup
	v1Root := t.TempDir()
	v1Sub := filepath.Join(v1Root, "sys", "fs", "cgroup", "cpu", "docker", "cid1")
	if err := os.MkdirAll(v1Sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v1Sub, "cpu.cfs_quota_us"), []byte("300000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v1Sub, "cpu.cfs_period_us"), []byte("100000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	procV1 := filepath.Join(v1Root, "proc_cgroup")
	if err := os.WriteFile(procV1, []byte("4:cpu,cpuacct:/docker/cid1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detectCgroupCPUQuotaFrom(procV1, filepath.Join(v1Root, "sys", "fs", "cgroup")); got != 3 {
		t.Errorf("cgroup v1 subpath quota = %d, want 3", got)
	}
}
