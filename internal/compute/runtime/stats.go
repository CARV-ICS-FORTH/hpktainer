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
	"strconv"
	"strings"
	"syscall"

	"hpk/internal/compute"
	"hpk/pkg/process"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TotalResources() corev1.ResourceList {
	var (
		totalCPU       resource.Quantity
		totalMem       resource.Quantity
		totalStorage   resource.Quantity
		totalEphemeral resource.Quantity
		totalPods      resource.Quantity
	)

	cpuCount := int64(getCPUCount())
	cpuQuantity := resource.NewQuantity(cpuCount, resource.DecimalSI)
	totalCPU.Add(*cpuQuantity)

	mem := getTotalMemory()
	memQuantity := resource.NewScaledQuantity(int64(mem), resource.Mega)
	totalMem.Add(*memQuantity)

	storage := getTotalStorage("/")
	storageQuantity := resource.NewScaledQuantity(int64(storage), resource.Mega)
	totalStorage.Add(*storageQuantity)

	ephemeral := getTotalStorage("/")
	ephemeralQuantity := resource.NewScaledQuantity(int64(ephemeral), resource.Mega)
	totalEphemeral.Add(*ephemeralQuantity)

	podsQuantity := resource.MustParse("110")
	totalPods.Add(podsQuantity)

	return corev1.ResourceList{
		corev1.ResourceCPU:              totalCPU,
		corev1.ResourceMemory:           totalMem,
		corev1.ResourceStorage:          totalStorage,
		corev1.ResourceEphemeralStorage: totalEphemeral,
		corev1.ResourcePods:             totalPods,
	}
}

func AllocatableResources(ctx context.Context) corev1.ResourceList {
	return TotalResources()
}

func getTotalMemory() uint64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		compute.SystemPanic(err, "Opening /proc/meminfo")
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}

			kb, _ := strconv.ParseUint(parts[1], 10, 64)
			return kb / 1024
		}
	}

	compute.SystemPanic(err, "MemTotal not found")

	return 0
}

func getTotalStorage(path string) uint64 {
	var stat syscall.Statfs_t

	err := syscall.Statfs(path, &stat)
	if err != nil {
		compute.SystemPanic(err, "Syscall Statfs")
		return 0
	}

	totalBytes := stat.Blocks * uint64(stat.Bsize)

	return totalBytes / (1024 * 1024)
}

func getCPUCount() uint64 {
	out, err := process.Execute("lscpu", "-p=CPU")
	if err != nil {
		compute.SystemPanic(err, "lscpu query error")
		return 0
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	var maxCPU uint64 = 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip comments and empty lines
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		cpu, _ := strconv.ParseUint(line, 10, 64)
		if cpu > maxCPU {
			maxCPU = cpu
		}
	}

	// lscpu outputs 0-indexed CPU numbers, so add 1 to get the count
	return maxCPU + 1
}