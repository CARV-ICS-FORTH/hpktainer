// Copyright © 2022 FORTH-ICS
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package runtime contains code for accessing compute resources.
package runtime

import (
	"bufio"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"skifflet/internal/compute"
)

// TotalResources derives truthful capacity bounded by Slurm allocations, cgroups, and CPU affinity.
func TotalResources() corev1.ResourceList {
	cpuCount := getEffectiveCPUCount()
	cpuQuantity := resource.NewQuantity(int64(cpuCount), resource.DecimalSI)

	memBytes := getEffectiveMemoryBytes(cpuCount)
	memQuantity := resource.NewQuantity(int64(memBytes), resource.BinarySI)

	storagePath := "/"
	if compute.Skiff.PodsDir() != "" {
		storagePath = compute.Skiff.PodsDir()
	} else if compute.Environment.WorkingDirectory != "" {
		storagePath = compute.Environment.WorkingDirectory
	}
	storageBytes := getEffectiveStorageBytes(storagePath)
	storageQuantity := resource.NewQuantity(int64(storageBytes), resource.BinarySI)

	podsQuantity := resource.MustParse("110")

	res := corev1.ResourceList{
		corev1.ResourceCPU:              *cpuQuantity,
		corev1.ResourceMemory:           *memQuantity,
		corev1.ResourceStorage:          *storageQuantity,
		corev1.ResourceEphemeralStorage: *storageQuantity,
		corev1.ResourcePods:             podsQuantity,
	}

	gpus := getEffectiveGPUCount()
	if gpus > 0 {
		res[corev1.ResourceName("nvidia.com/gpu")] = *resource.NewQuantity(int64(gpus), resource.DecimalSI)
	}

	return res
}

// AllocatableResources derives allocatable capacity by reserving control-plane and runtime overhead.
func AllocatableResources(ctx context.Context) corev1.ResourceList {
	total := TotalResources()
	allocatable := total.DeepCopy()

	// Reserve 100m CPU if >= 2 cores
	cpuVal := total.Cpu().MilliValue()
	if cpuVal >= 2000 {
		reservedCPU := resource.NewMilliQuantity(100, resource.DecimalSI)
		allocatableCPU := resource.NewMilliQuantity(cpuVal-100, resource.DecimalSI)
		allocatable[corev1.ResourceCPU] = *allocatableCPU
		_ = reservedCPU
	}

	// Reserve memory (5% of total, capped between 64Mi and 512Mi)
	memBytes := total.Memory().Value()
	resMem := memBytes / 20
	if resMem < 64*1024*1024 {
		resMem = 64 * 1024 * 1024
	}
	if resMem > 512*1024*1024 {
		resMem = 512 * 1024 * 1024
	}
	if memBytes > resMem {
		allocatable[corev1.ResourceMemory] = *resource.NewQuantity(memBytes-resMem, resource.BinarySI)
	}

	return allocatable
}

// HasGPUAllocation reports whether GPUs are allocated to the node/task.
func HasGPUAllocation() bool {
	return getEffectiveGPUCount() > 0
}

func getEffectiveCPUCount() int64 {
	// 1. Check Slurm environment
	if val := os.Getenv("SLURM_CPUS_ON_NODE"); val != "" {
		if c, err := strconv.ParseInt(val, 10, 64); err == nil && c > 0 {
			return c
		}
	}
	if val := os.Getenv("SLURM_JOB_CPUS_PER_NODE"); val != "" {
		// Handle formats like "4", "4(x2)"
		parts := strings.Split(val, "(")
		if c, err := strconv.ParseInt(parts[0], 10, 64); err == nil && c > 0 {
			return c
		}
	}

	// 2. Check cgroup cpu quota (cgroup v2: /sys/fs/cgroup/cpu.max)
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 2 && fields[0] != "max" {
			quota, errQ := strconv.ParseInt(fields[0], 10, 64)
			period, errP := strconv.ParseInt(fields[1], 10, 64)
			if errQ == nil && errP == nil && period > 0 {
				cpus := quota / period
				if cpus > 0 {
					return cpus
				}
			}
		}
	}

	// 3. Fallback to CPU affinity via runtime.NumCPU()
	cpus := int64(runtime.NumCPU())
	if cpus > 0 {
		return cpus
	}
	return 1
}

func getEffectiveMemoryBytes(cpuCount int64) uint64 {
	// 1. Check Slurm environment
	if val := os.Getenv("SLURM_MEM_PER_NODE"); val != "" {
		if m, err := strconv.ParseUint(val, 10, 64); err == nil && m > 0 {
			return m * 1024 * 1024 // Slurm reports in MB
		}
	}
	if val := os.Getenv("SLURM_MEM_PER_CPU"); val != "" {
		if m, err := strconv.ParseUint(val, 10, 64); err == nil && m > 0 {
			return m * uint64(cpuCount) * 1024 * 1024
		}
	}

	// 2. Check cgroup memory limit (cgroup v2: /sys/fs/cgroup/memory.max)
	if data, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		str := strings.TrimSpace(string(data))
		if str != "max" {
			if limit, err := strconv.ParseUint(str, 10, 64); err == nil && limit > 0 && limit < (1<<62) {
				return limit
			}
		}
	}
	// Check cgroup v1: /sys/fs/cgroup/memory/memory.limit_in_bytes
	if data, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		str := strings.TrimSpace(string(data))
		if limit, err := strconv.ParseUint(str, 10, 64); err == nil && limit > 0 && limit < (1<<62) {
			return limit
		}
	}

	// 3. Fallback to /proc/meminfo
	return getHostTotalMemoryBytes()
}

func getHostTotalMemoryBytes() uint64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 1024 * 1024 * 1024 // 1Gi fallback
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				if kb, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
					return kb * 1024 // bytes
				}
			}
		}
	}
	return 1024 * 1024 * 1024
}

func getEffectiveStorageBytes(path string) uint64 {
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		// Try root if path failed
		if errRoot := syscall.Statfs("/", &stat); errRoot != nil {
			return 10 * 1024 * 1024 * 1024 // 10Gi fallback
		}
	}

	return stat.Blocks * uint64(stat.Bsize)
}

func getEffectiveGPUCount() int {
	for _, env := range []string{"CUDA_VISIBLE_DEVICES", "NVIDIA_VISIBLE_DEVICES", "SLURM_STEP_GPUS"} {
		if val := strings.TrimSpace(os.Getenv(env)); val != "" && val != "none" && val != "void" {
			parts := strings.Split(val, ",")
			count := 0
			for _, p := range parts {
				if strings.TrimSpace(p) != "" {
					count++
				}
			}
			if count > 0 {
				return count
			}
		}
	}
	return 0
}
