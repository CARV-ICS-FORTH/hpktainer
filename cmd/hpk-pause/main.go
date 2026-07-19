// Copyright © 2023 FORTH-ICS
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

package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"hpk/internal/compute"
	"hpk/internal/compute/endpoint"
	"hpk/internal/compute/image"
	"hpk/internal/compute/podhandler"
	kubecontainer "hpk/pkg/container"
	"hpk/pkg/version"

	"github.com/rs/zerolog/log"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func getPodDetails(clientset *kubernetes.Clientset, namespace string, podID string) (*v1.Pod, error) {
	// Create a context with a 20-second timeout per request attempt
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podID, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return pod, nil
}

func fileExists(filename string) bool {
	if filename == "" {
		return false
	}
	info, err := os.Stat(filename)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

type containerTracker struct {
	mu        sync.Mutex
	processes map[int]*exec.Cmd
}

func newContainerTracker() *containerTracker {
	return &containerTracker{
		processes: make(map[int]*exec.Cmd),
	}
}

func (ct *containerTracker) Add(cmd *exec.Cmd) {
	if ct == nil || cmd == nil || cmd.Process == nil {
		return
	}
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.processes[cmd.Process.Pid] = cmd
}

func (ct *containerTracker) Remove(cmd *exec.Cmd) {
	if ct == nil || cmd == nil || cmd.Process == nil {
		return
	}
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.processes, cmd.Process.Pid)
}

func (ct *containerTracker) SignalAll(sig os.Signal) {
	if ct == nil {
		return
	}
	ct.mu.Lock()
	cmds := make([]*exec.Cmd, 0, len(ct.processes))
	for _, cmd := range ct.processes {
		cmds = append(cmds, cmd)
	}
	ct.mu.Unlock()

	for _, cmd := range cmds {
		if cmd != nil && cmd.Process != nil {
			log.Info().Msgf("Sending signal %v to process %d", sig, cmd.Process.Pid)
			pgid, err := syscall.Getpgid(cmd.Process.Pid)
			if err == nil && pgid > 1 {
				if sysSig, ok := sig.(syscall.Signal); ok {
					_ = syscall.Kill(-pgid, sysSig)
				}
			}
			_ = cmd.Process.Signal(sig)
		}
	}
}

func main() {
	var podID string
	var namespaceID string
	var wg sync.WaitGroup

	flag.StringVar(&podID, "pod", "", "Pod ID to query Kubernetes")
	flag.StringVar(&namespaceID, "namespace", "", "Pod ID to query Kubernetes")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("hpk-pause version: %s (built: %s)\n", version.Version, version.BuildTime)
		os.Exit(0)
	}

	if podID == "" || namespaceID == "" {
		log.Fatal().Msg("Please provide both the pod and namespace.")
	}

	config, err := clientcmd.BuildConfigFromFlags("", filepath.Join("/k8s-data", "kubeconfig"))
	if err != nil {
		log.Fatal().Err(err).Msg("Error building kubeconfig")
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal().Err(err).Msg("Error creating Kubernetes client")
	}

	// Main loop to keep asking for pod details
	timeout := time.After(5 * time.Minute)

acquire_pod_loop:
	for {
		select {
		case <-timeout:
			log.Error().Msg("Timeout reached. Exiting.")
			os.Exit(1)
		default:
			_, err := getPodDetails(clientset, namespaceID, podID)
			if err != nil {
				log.Error().Err(err).Msg("Error getting pod details. Retrying...")
				time.Sleep(5 * time.Second) // Adjust retry interval as needed
				continue
			}

			break acquire_pod_loop
		}
	}

	pod, err := getPodDetails(clientset, namespaceID, podID)
	if err != nil {
		panic(err)
	}

	podKey := client.ObjectKeyFromObject(pod)
	hpk := endpoint.HPK(pod.Annotations["workingDirectory"])
	podPath := hpk.Pod(podKey)

	pausePID := os.Getpid()
	if err := os.MkdirAll(podPath.ControlFileDir(), 0755); err == nil {
		if err := os.WriteFile(podPath.PauseJobIDPath(), []byte(fmt.Sprintf("pid://%d", pausePID)), 0644); err != nil {
			log.Error().Err(err).Msg("Failed to write pause jobid file")
		}
	}

	tracker := newContainerTracker()

	ctx, cancel := context.WithCancel(context.Background())
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGCHLD)

	go func() {
		for {
			select {
			case signo := <-signalChan:
				switch signo {
				case syscall.SIGINT, syscall.SIGTERM:
					log.Info().Msgf("Received %v. Terminating container children...", signo)
					tracker.SignalAll(syscall.SIGTERM)

					done := make(chan struct{})
					go func() {
						wg.Wait()
						close(done)
					}()

					select {
					case <-done:
						log.Info().Msg("All containers terminated gracefully")
					case <-time.After(30 * time.Second):
						log.Warn().Msg("Grace period (30s) expired, sending SIGKILL to remaining containers")
						tracker.SignalAll(syscall.SIGKILL)
						<-done
					}

					cancel()
					return

				case syscall.SIGCHLD:
					log.Info().Msg("Received SIGCHLD.")
					for {
						pid, err := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
						if pid <= 0 {
							if err != nil && err != syscall.ECHILD {
								log.Error().Err(err).Msg("Error reaping child process")
							}
							break
						}
						log.Info().Msgf("Reaped pid: %v", pid)
					}
				}
			case <-ctx.Done():
				log.Info().Msg("Containers and context have terminated. Exiting...")
				return
			}
		}
	}()

	if err := prepareContainers(pod); err != nil {
		log.Error().Err(err).Msg("Error preparing container environment")
		return
	}

	if len(pod.Spec.InitContainers) > 0 {
		if err := handleInitContainers(pod, tracker); err != nil {
			log.Error().Err(err).Msg("Error executing init containers")
			return
		}
	}

	if err := handleContainers(pod, &wg, tracker); err != nil {
		log.Error().Err(err).Msg("Error executing main containers")
		return
	}

	log.Info().Msg("Containers have started. Now waiting on context or signals")
	<-ctx.Done()

}

func prepareContainers(pod *v1.Pod) error {
	if err := prepareDNS(pod); err != nil {
		return fmt.Errorf("could not prepare DNS : %v", err)
	}
	if err := announceIP(pod); err != nil {
		return fmt.Errorf("could not announce ip : %v", err)
	}
	if err := cleanEnvironment(); err != nil {
		return fmt.Errorf("could not clear the environment : %v", err)
	}
	if err := prepareApptainerRuntimeDirs(); err != nil {
		return fmt.Errorf("could not prepare apptainer runtime dirs : %v", err)
	}
	return nil
}

func prepareApptainerRuntimeDirs() error {
	const baseDir = "/tmp/.hpk-apptainer"
	tmpDir := filepath.Join(baseDir, "tmp")
	cacheDir := filepath.Join(baseDir, "cache")

	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("could not create tmp dir '%s': %v", tmpDir, err)
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("could not create cache dir '%s': %v", cacheDir, err)
	}

	if err := os.Setenv("APPTAINER_TMPDIR", tmpDir); err != nil {
		return fmt.Errorf("could not set APPTAINER_TMPDIR: %v", err)
	}
	if err := os.Setenv("SINGULARITY_TMPDIR", tmpDir); err != nil {
		return fmt.Errorf("could not set SINGULARITY_TMPDIR: %v", err)
	}
	if err := os.Setenv("TMPDIR", tmpDir); err != nil {
		return fmt.Errorf("could not set TMPDIR: %v", err)
	}

	if err := os.Setenv("APPTAINER_CACHEDIR", cacheDir); err != nil {
		return fmt.Errorf("could not set APPTAINER_CACHEDIR: %v", err)
	}
	if err := os.Setenv("SINGULARITY_CACHEDIR", cacheDir); err != nil {
		return fmt.Errorf("could not set SINGULARITY_CACHEDIR: %v", err)
	}

	return nil
}

func announceIP(pod *v1.Pod) error {
	podKey := client.ObjectKeyFromObject(pod)
	hpk := endpoint.HPK(pod.Annotations["workingDirectory"])
	podPath := hpk.Pod(podKey)

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return fmt.Errorf("could not get interfaces from host: %v", err)
	}
	var ipAddresses []string
	for _, addr := range addrs {
		// Add only if the address is an IP address
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			ipAddresses = append(ipAddresses, ipNet.IP.String())
		}
	}
	ipString := strings.Join(ipAddresses, " ")

	if err := os.WriteFile(podPath.IPAddressPath(), []byte(ipString), os.ModePerm); err != nil {
		return fmt.Errorf("error writing to .ip file: %v", err)
	}
	return nil
}

func cleanEnvironment() error {

	envVars := []string{
		"LD_LIBRARY_PATH",
		"SINGULARITY_COMMAND",
		"SINGULARITY_CONTAINER",
		"SINGULARITY_ENVIRONMENT",
		"SINGULARITY_NAME",
		"SINGULARITY_BIND",
		"SINGULARITY_BINDPATH",
		"SINGULARITY_MOUNT",
		"APPTAINER_APPNAME",
		"APPTAINER_COMMAND",
		"APPTAINER_CONTAINER",
		"APPTAINER_ENVIRONMENT",
		"APPTAINER_NAME",
		"APPTAINER_BIND",
		"APPTAINER_BINDPATH",
		"APPTAINER_MOUNT",
	}

	for _, name := range envVars {
		if err := os.Unsetenv(name); err != nil {
			return fmt.Errorf("could not clear the environment variable %s: %v", name, err)
		}
	}
	return nil
}

func getHostResolvConf(kubeDNSIP string) string {
	paths := []string{"/etc/resolv.conf", "/run/systemd/resolve/resolv.conf"}
	var raw string
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			content := string(b)
			if strings.Contains(content, "127.0.0.53") && p == "/etc/resolv.conf" {
				if sysb, sysErr := os.ReadFile("/run/systemd/resolve/resolv.conf"); sysErr == nil && len(strings.TrimSpace(string(sysb))) > 0 {
					content = string(sysb)
				}
			}
			if len(strings.TrimSpace(content)) > 0 {
				raw = content
				break
			}
		}
	}

	if raw == "" {
		return "nameserver 1.1.1.1\n"
	}

	var validLines []string
	hasNameserver := false
	hasExternalNameserver := false
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "nameserver") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				ipStr := parts[1]
				ip := net.ParseIP(ipStr)
				if ip != nil && (ip.IsLoopback() || ipStr == kubeDNSIP) {
					continue
				}
				hasNameserver = true
				if !strings.HasPrefix(ipStr, "10.0.") {
					hasExternalNameserver = true
				}
			}
		}
		validLines = append(validLines, line)
	}

	if !hasNameserver || !hasExternalNameserver {
		validLines = append(validLines, "nameserver 1.1.1.1")
	}

	return strings.Join(validLines, "\n") + "\n"
}

func prepareDNS(pod *v1.Pod) error {
	if err := os.MkdirAll("/scratch/etc", 0755); err != nil {
		return fmt.Errorf("could not create /scratch/etc folder: %v", err)
	}

	kubeDNSIP := os.Getenv("KUBEDNS_IP")
	if kubeDNSIP == "" {
		return fmt.Errorf("KUBEDNS_IP environment variable not set")
	}

	var resolvConfContent string
	isCoreDNS := strings.HasPrefix(pod.Name, "coredns") || (pod.Labels != nil && pod.Labels["k8s-app"] == "kube-dns")
	if pod.Spec.DNSPolicy == v1.DNSDefault || isCoreDNS {
		resolvConfContent = getHostResolvConf(kubeDNSIP)
	} else if pod.Spec.DNSPolicy == v1.DNSNone {
		resolvConfContent = ""
	} else {
		resolvConfContent = fmt.Sprintf(
			"search %s.svc.cluster.local svc.cluster.local cluster.local\nnameserver %s\noptions ndots:5\n",
			pod.Namespace, kubeDNSIP,
		)
	}

	if pod.Spec.DNSConfig != nil {
		var lines []string
		if resolvConfContent != "" {
			lines = append(lines, strings.TrimSpace(resolvConfContent))
		}
		if len(pod.Spec.DNSConfig.Searches) > 0 {
			lines = append(lines, fmt.Sprintf("search %s", strings.Join(pod.Spec.DNSConfig.Searches, " ")))
		}
		for _, ns := range pod.Spec.DNSConfig.Nameservers {
			lines = append(lines, fmt.Sprintf("nameserver %s", ns))
		}
		for _, opt := range pod.Spec.DNSConfig.Options {
			if opt.Value != nil {
				lines = append(lines, fmt.Sprintf("options %s:%s", opt.Name, *opt.Value))
			} else {
				lines = append(lines, fmt.Sprintf("options %s", opt.Name))
			}
		}
		resolvConfContent = strings.Join(lines, "\n") + "\n"
	}

	if err := os.WriteFile("/scratch/etc/resolv.conf", []byte(resolvConfContent), os.ModePerm); err != nil {
		return fmt.Errorf("error writing to resolv.conf: %v", err)
	}

	// Add hostname to /scratch/etc/hosts
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("error getting hostname: %v", err)
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return fmt.Errorf("could not get interfaces from host: %v", err)
	}

	var ipAddresses []string
	for _, addr := range addrs {
		// Add only if the address is an IP address
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			ipAddresses = append(ipAddresses, ipNet.IP.String())
		}
	}
	ipString := strings.Join(ipAddresses, " ") + " " + hostname

	hostsContent := fmt.Sprintf("127.0.0.1 localhost\n%s \n", ipString)

	if err := os.WriteFile("/scratch/etc/hosts", []byte(hostsContent), os.ModePerm); err != nil {
		return fmt.Errorf("error writing to hosts: %v", err)
	}
	DebugDNSInfo(resolvConfContent, hostsContent)
	return nil
}

func DebugDNSInfo(resolvConfContent string, hostsContent string) {
	fmt.Printf("====================================================================\n%s\n", resolvConfContent)
	fmt.Printf("====================================================================\n%s", hostsContent)
	fmt.Println("====================================================================")

}

func handleInitContainers(pod *v1.Pod, tracker *containerTracker) error {
	isDebug := os.Getenv("DEBUG_MODE") == "true"
	podKey := client.ObjectKeyFromObject(pod)
	hpk := endpoint.HPK(pod.Annotations["workingDirectory"])
	podPath := hpk.Pod(podKey)
	for _, container := range pod.Spec.InitContainers {
		effectiSecurityContext := podhandler.DetermineEffectiveSecurityContext(pod, &container)
		uid, gid := podhandler.DetermineEffectiveRunAsUser(effectiSecurityContext)
		log.Info().Msgf("Spawning init container: %s", container.Name)
		instanceName := fmt.Sprintf("%s_%s_%s", pod.GetNamespace(), pod.GetName(), container.Name)

		containerPath := podPath.Container(container.Name)
		envFilePath := containerPath.EnvFilePath()

		// Environment File Handling
		if fileExists(envFilePath) {
			output, err := exec.Command("sh", "-c", envFilePath).CombinedOutput()
			if err != nil {
				return fmt.Errorf("error executing EnvFilePath: %v, output: %s", err, output)
			}
			envFileName := filepath.Join("/scratch", instanceName+".env")
			if err := os.WriteFile(envFileName, output, 0644); err != nil {
				return fmt.Errorf("error writing env file: %v", err)
			}
		}

		executionMode := "exec"
		if container.Command == nil {
			executionMode = "run"
		}

		binds := make([]string, len(container.VolumeMounts))

		// check the code from https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/kubelet_pods.go#L196
		for i, mount := range container.VolumeMounts {
			hostPath := filepath.Join(podPath.VolumeDir(), mount.Name)

			subPath := mount.SubPath
			if mount.SubPathExpr != "" {

				podEnv, err := podhandler.FromServicesForPod(context.Background(), pod)
				if err != nil {
					compute.SystemPanic(err, "failed to get service env vars for pod '%s'", podKey)
				}
				path, err := kubecontainer.ExpandContainerVolumeMounts(mount, podEnv)
				if err != nil {
					compute.SystemPanic(err, "cannot expand env variables for container '%s' of pod '%s'", container.Name, podKey)
				}
				subPath = path
			}

			if subPath != "" {
				if filepath.IsAbs(subPath) {
					return fmt.Errorf("error SubPath '%s' must not be an absolute path", subPath)
				}

				subPathFile := filepath.Join(hostPath, subPath)

				// mount the subpath
				hostPath = subPathFile
			}

			accessMode := "rw"
			if mount.ReadOnly {
				accessMode = "ro"
			}

			binds[i] = hostPath + ":" + mount.MountPath + ":" + accessMode
		}

		// Apptainer Command Construction
		apptainerVerbosity := "--quiet"
		if isDebug {
			apptainerVerbosity = "--debug"
		}
		apptainerArgs := []string{
			apptainerVerbosity, executionMode, "--nv", "--cleanenv", "--writable-tmpfs", "--no-mount", "home,bind-paths", "--unsquash",
		}
		var allBinds []string
		if fileExists("/scratch/etc/resolv.conf") {
			allBinds = append(allBinds, "/scratch/etc/resolv.conf:/etc/resolv.conf", "/scratch/etc/hosts:/etc/hosts")
		}
		allBinds = append(allBinds, binds...)

		if len(allBinds) > 0 {
			apptainerArgs = append(apptainerArgs, "--bind", strings.Join(allBinds, ","))
		}
		if uid != 0 || gid != 0 {
			apptainerArgs = append(apptainerArgs, "--security", fmt.Sprintf("uid:%d,gid:%d", uid, gid), "--userns")
		}

		if fileExists(envFilePath) {
			apptainerArgs = append(apptainerArgs, "--env-file", filepath.Join("/scratch", instanceName+".env"))
		}

		apptainerArgs = append(apptainerArgs, hpk.ImageDir()+image.ParseImageName(container.Image))
		apptainerArgs = append(apptainerArgs, kubecontainer.ExpandContainerCommandOnlyStatic(container.Command, container.Env)...)
		apptainerArgs = append(apptainerArgs, kubecontainer.ExpandContainerCommandOnlyStatic(container.Args, container.Env)...)

		// Get the PID
		pid := os.Getpid()
		if err := os.WriteFile(containerPath.IDPath(), []byte(fmt.Sprintf("pid://%d", pid)), 0644); err != nil {
			return fmt.Errorf("failed to create pid file") // Log the error
		}

		// Execute Apptainer (Blocking)
		log.Debug().Msg(fmt.Sprintf("ApptainerArgs: %v", apptainerArgs))
		cmd := exec.Command("apptainer", apptainerArgs...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = os.Environ()

		// Open log file
		logFile, err := os.Create(containerPath.LogsPath())
		if err != nil {
			return fmt.Errorf("failed to create log file: %v", err)
		}
		defer logFile.Close()

		// // Redirect output to log file
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err := cmd.Start(); err != nil {
			log.Error().Err(err).Msgf("Error starting init container: %s", container.Name)
			return fmt.Errorf("init container start failed: %v", err)
		}
		tracker.Add(cmd)

		if err := cmd.Wait(); err != nil {
			tracker.Remove(cmd)
			log.Error().Err(err).Msgf("Error executing init container: %s", container.Name)
			return fmt.Errorf("init container failed: %v", err) // Abort on failure
		}
		tracker.Remove(cmd)

		if err := os.WriteFile(containerPath.ExitCodePath(), []byte(strconv.Itoa(cmd.ProcessState.ExitCode())), 0644); err != nil {
			return fmt.Errorf("failed to create exitCode file") // Log the error
		}
	}
	return nil
}

func handleContainers(pod *v1.Pod, wg *sync.WaitGroup, tracker *containerTracker) error {
	isDebug := os.Getenv("DEBUG_MODE") == "true"
	podKey := client.ObjectKeyFromObject(pod)
	hpk := endpoint.HPK(pod.Annotations["workingDirectory"])
	podPath := hpk.Pod(podKey)
	for _, container := range pod.Spec.Containers {
		effectiSecurityContext := podhandler.DetermineEffectiveSecurityContext(pod, &container)
		uid, gid := podhandler.DetermineEffectiveRunAsUser(effectiSecurityContext)
		instanceName := fmt.Sprintf("%s_%s_%s", pod.GetNamespace(), pod.GetName(), container.Name)

		containerPath := podPath.Container(container.Name)
		envFilePath := containerPath.EnvFilePath()

		// Environment File Handling
		if fileExists(envFilePath) {
			output, err := exec.Command("sh", "-c", envFilePath).CombinedOutput()
			if err != nil {
				return fmt.Errorf("error executing EnvFilePath: %v, output: %s", err, output)
			}
			envFileName := filepath.Join("/scratch", instanceName+".env")
			if err := os.WriteFile(envFileName, output, 0644); err != nil {
				return fmt.Errorf("error writing env file: %v", err)
			}
		}

		executionMode := "exec"
		if container.Command == nil {
			executionMode = "run"
		}

		binds := make([]string, len(container.VolumeMounts))

		// check the code from https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/kubelet_pods.go#L196
		for i, mount := range container.VolumeMounts {
			hostPath := filepath.Join(podPath.VolumeDir(), mount.Name)

			subPath := mount.SubPath
			if mount.SubPathExpr != "" {

				podEnv, err := podhandler.FromServicesForPod(context.Background(), pod)
				if err != nil {
					compute.SystemPanic(err, "failed to get service env vars for pod '%s'", podKey)
				}
				path, err := kubecontainer.ExpandContainerVolumeMounts(mount, podEnv)
				if err != nil {
					compute.SystemPanic(err, "cannot expand env variables for container '%s' of pod '%s'", container.Name, podKey)
				}
				subPath = path
			}

			if subPath != "" {
				if filepath.IsAbs(subPath) {
					return fmt.Errorf("error SubPath '%s' must not be an absolute path", subPath)
				}

				subPathFile := filepath.Join(hostPath, subPath)

				// mount the subpath
				hostPath = subPathFile
			}

			accessMode := "rw"
			if mount.ReadOnly {
				accessMode = "ro"
			}

			binds[i] = hostPath + ":" + mount.MountPath + ":" + accessMode
		}

		// Apptainer Command Construction
		apptainerVerbosity := "--quiet"
		if isDebug {
			apptainerVerbosity = "--debug"
		}
		apptainerArgs := []string{
			apptainerVerbosity, executionMode, "--nv", "--cleanenv", "--writable-tmpfs", "--no-mount", "home,bind-paths", "--unsquash",
		}
		var allBinds []string
		if fileExists("/scratch/etc/resolv.conf") {
			allBinds = append(allBinds, "/scratch/etc/resolv.conf:/etc/resolv.conf", "/scratch/etc/hosts:/etc/hosts")
		}
		allBinds = append(allBinds, binds...)

		if len(allBinds) > 0 {
			apptainerArgs = append(apptainerArgs, "--bind", strings.Join(allBinds, ","))
		}
		if uid != 0 || gid != 0 {
			apptainerArgs = append(apptainerArgs, "--security", fmt.Sprintf("uid:%d,gid:%d", uid, gid), "--userns")
		}

		if fileExists(envFilePath) {
			apptainerArgs = append(apptainerArgs, "--env-file", filepath.Join("/scratch", instanceName+".env"))
		}

		apptainerArgs = append(apptainerArgs, hpk.ImageDir()+image.ParseImageName(container.Image))
		apptainerArgs = append(apptainerArgs, kubecontainer.ExpandContainerCommandOnlyStatic(container.Command, container.Env)...)
		apptainerArgs = append(apptainerArgs, kubecontainer.ExpandContainerCommandOnlyStatic(container.Args, container.Env)...)

		wg.Add(1)
		go func(container v1.Container) { // Ensure container cleanup
			defer wg.Done()
			// Execute Apptainer in Background
			log.Debug().Msg(fmt.Sprintf("ApptainerArgs: %v", apptainerArgs))
			cmd := exec.Command("apptainer", apptainerArgs...)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Env = os.Environ()
			// If needed, get references to stdout and stderr
			// log.Debug().Msgf("LogPath: %s", containerPath.LogsPath())
			logFile, err := os.Create(containerPath.LogsPath())
			if err != nil {
				log.Error().Err(err).Msgf("Failed to create log file %s", containerPath.LogsPath())
				return
			}
			defer logFile.Close()

			cmd.Stdout = logFile
			cmd.Stderr = logFile
			log.Info().Msgf("Spawning main container: %s", container.Name)
			// Start the container
			if err := cmd.Start(); err != nil {
				log.Error().Err(err).Msg("Failed to start Apptainer container")
				return
			}
			tracker.Add(cmd)
			defer tracker.Remove(cmd)

			// Get the PID
			pid := cmd.Process.Pid
			if err := os.WriteFile(containerPath.IDPath(), []byte(fmt.Sprintf("pid://%d", pid)), 0644); err != nil {
				log.Error().Err(err).Msg("Failed to create pid file") // Log the error
				return
			}

			// Handle Exit (consider moving output writing or using cmd.Wait)
			if err := cmd.Wait(); err != nil {
				log.Error().Err(err).Msgf("error executing container: %s, because of %v", container.Name, err)
			}

			if err := os.WriteFile(containerPath.ExitCodePath(), []byte(strconv.Itoa(cmd.ProcessState.ExitCode())), 0644); err != nil {
				log.Error().Err(err).Msg("Failed to create exitCode file") // Log the error
				return
			}

		}(container)

	}
	return nil
}
