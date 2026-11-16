//go:build unix

package bashtion

import (
	"os"
	stdpath "path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// DefaultJobs determines the optimal concurrency level for parallel worker jobs.
// It inspects Linux cgroup CPU quotas (cgroup v1 and v2, common in Docker and Kubernetes)
// to avoid thread over-allocation when host CPU cores exceed container quotas.
func DefaultJobs() int {
	numCPU := runtime.NumCPU()
	if runtime.GOOS != "linux" {
		if numCPU < 1 {
			return 1
		}
		return numCPU
	}

	quota := detectCgroupCPUQuota()
	if quota > 0 && quota < numCPU {
		return quota
	}
	if numCPU < 1 {
		return 1
	}
	return numCPU
}

func detectCgroupCPUQuota() int {
	return detectCgroupCPUQuotaFrom("/proc/self/cgroup", "/sys/fs/cgroup")
}

func detectCgroupCPUQuotaFrom(procCgroupPath, cgroupRoot string) int {
	v2Subpath, v1Subpath := parseProcSelfCgroup(procCgroupPath)

	// 1. Try cgroup v2: unified hierarchy path first (walking up to cgroupRoot), then root cpu.max.
	for _, sub := range cgroupHierarchyCandidates(v2Subpath) {
		p := filepath.Join(cgroupRoot, filepath.FromSlash(sub), "cpu.max")
		if data, err := os.ReadFile(p); err == nil {
			if q := parseCgroupV2Max(string(data)); q > 0 {
				return q
			}
		}
	}

	// 2. Try cgroup v1: cpu controller subpath first (walking up to cgroupRoot/cpu), then root.
	for _, sub := range cgroupHierarchyCandidates(v1Subpath) {
		dir := filepath.Join(cgroupRoot, "cpu", filepath.FromSlash(sub))
		quotaData, err1 := os.ReadFile(filepath.Join(dir, "cpu.cfs_quota_us"))
		periodData, err2 := os.ReadFile(filepath.Join(dir, "cpu.cfs_period_us"))
		if err1 == nil && err2 == nil {
			if q := parseCgroupV1Quota(string(quotaData), string(periodData)); q > 0 {
				return q
			}
		}
	}

	return 0
}

func parseProcSelfCgroup(procCgroupPath string) (v2Path, v1CPUPath string) {
	data, err := os.ReadFile(procCgroupPath)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		controllers := parts[1]
		cgPath := strings.TrimPrefix(stdpath.Clean("/"+parts[2]), "/")
		if parts[0] == "0" && controllers == "" {
			if v2Path == "" {
				v2Path = cgPath
			}
			continue
		}
		for _, ctrl := range strings.Split(controllers, ",") {
			if ctrl == "cpu" && v1CPUPath == "" {
				v1CPUPath = cgPath
			}
		}
	}
	return v2Path, v1CPUPath
}

func cgroupHierarchyCandidates(sub string) []string {
	var out []string
	cur := strings.Trim(stdpath.Clean("/"+sub), "/")
	for cur != "" && cur != "." {
		out = append(out, cur)
		parent := stdpath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	out = append(out, "")
	return out
}

func parseCgroupV2Max(content string) int {
	fields := strings.Fields(content)
	if len(fields) < 2 || fields[0] == "max" {
		return 0
	}
	quota, err1 := strconv.ParseInt(fields[0], 10, 64)
	period, err2 := strconv.ParseInt(fields[1], 10, 64)
	if err1 != nil || err2 != nil || quota <= 0 || period <= 0 {
		return 0
	}
	cpus := int((quota + period - 1) / period)
	if cpus < 1 {
		return 1
	}
	return cpus
}

func parseCgroupV1Quota(quotaStr, periodStr string) int {
	q, err1 := strconv.ParseInt(strings.TrimSpace(quotaStr), 10, 64)
	p, err2 := strconv.ParseInt(strings.TrimSpace(periodStr), 10, 64)
	if err1 != nil || err2 != nil || q <= 0 || p <= 0 {
		return 0
	}
	cpus := int((q + p - 1) / p)
	if cpus < 1 {
		return 1
	}
	return cpus
}
