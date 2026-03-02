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

// Package slurm contains code for accessing compute resources via Slurm.
package slurm

import (
	"bufio"
	"context"
	"strconv"
	"strings"

	"os"
	"syscall"

	"hpk/internal/compute"
	"hpk/pkg/process"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/json"
)

func TotalResources() corev1.ResourceList {
	var (
		totalCPU       resource.Quantity
		totalMem       resource.Quantity
		totalStorage   resource.Quantity
		totalEphemeral resource.Quantity
		totalPods      resource.Quantity
	)
	if compute.Environment.RunSlurm {
		for _, node := range getClusterStats().Nodes {
			nodeResources := node.ResourceList()

			if cpu := nodeResources.Cpu(); !cpu.IsZero() {
				totalCPU.Add(*cpu)
			}

			if mem := nodeResources.Memory(); !mem.IsZero() {
				totalMem.Add(*mem)
			}

			if storage := nodeResources.Storage(); !storage.IsZero() {
				totalStorage.Add(*storage)
			}

			if ephemeral := nodeResources.StorageEphemeral(); !ephemeral.IsZero() {
				totalEphemeral.Add(*ephemeral)
			}

			if pods := nodeResources.Pods(); !pods.IsZero() {
				totalPods.Add(*pods)
			}
		}
	} else {
		// Need: totalCPU, totalMem, totalStorage, totalEphemeral, totalPods
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
	}

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

type NodeInfo struct {
	Architecture  string `json:"architecture"`
	KernelVersion string `json:"operating_system"`

	Name     string `json:"name"`
	CPUs     uint64 `json:"cpus"`
	CPUCores uint64 `json:"cores"`
	GPUs     uint64 `json:"nvidia.com/gpu"`

	EphemeralStorage uint64 `json:"temporary_disk"`

	// FreeMemory ... reported in MegaBytes
	//[TODO: temporarily changed it to int64 due to sometimes slurm declares freememory as "-2"]
	FreeMemory int64    `json:"free_memory"`
	Partitions []string `json:"partitions"`
}

// ResourceList converts the Slurm-reported stats into Kubernetes-Stats.
func (i NodeInfo) ResourceList() corev1.ResourceList {
	return corev1.ResourceList{
		"cpu":       *resource.NewQuantity(int64(i.CPUs), resource.DecimalSI),
		"memory":    *resource.NewScaledQuantity(int64(i.FreeMemory), resource.Mega),
		"ephemeral": *resource.NewQuantity(int64(i.EphemeralStorage), resource.DecimalSI),
		"pods":      resource.MustParse("110"),
		"gpu":       *resource.NewQuantity(int64(i.GPUs), resource.DecimalSI),
	}
}

type Stats struct {
	Nodes []NodeInfo `json:"nodes"`
}

func getClusterStats() Stats {
	out, err := process.Execute(Slurm.StatsCmd, "--long", "--json")
	if err != nil {
		compute.SystemPanic(err, "stats query error. out : '%s'", out)
	}

	var info Stats

	if err := json.Unmarshal(out, &info); err != nil {
		compute.SystemPanic(err, "stats decoding error")
	}

	return info
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