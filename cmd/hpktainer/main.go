package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"hpk/internal/network"
	"hpk/pkg/version"

	"github.com/google/uuid"
	"github.com/vishvananda/netlink"
)

const (
	SocketDir    = "/var/run/hpktainer"
	CNIDataDir   = "/var/lib/cni/networks/hpktainer"
	CalicoConfig = "/run/calico/subnet.env"
)

func ensureApptainerRuntimeDirs() (tmpDir string, cacheDir string, err error) {
	preferredBase := "/root/.hpk/.apptainer"
	tmpDir = filepath.Join(preferredBase, "tmp")
	cacheDir = filepath.Join(preferredBase, "cache")

	if err = os.MkdirAll(tmpDir, 0o755); err == nil {
		if err = os.MkdirAll(cacheDir, 0o755); err == nil {
			return tmpDir, cacheDir, nil
		}
	}

	// Fallback for contexts where /root/.hpk is not available.
	fallbackBase := "/tmp/.hpk-apptainer"
	tmpDir = filepath.Join(fallbackBase, "tmp")
	cacheDir = filepath.Join(fallbackBase, "cache")

	if err = os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", "", err
	}

	if err = os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", "", err
	}

	return tmpDir, cacheDir, nil
}

func main() {
	os.Exit(run())
}

func isRoot() bool {
	currentUser, err := user.Current()
	if err != nil {
		log.Printf("Failed to get current user: %v", err)
		return false
	}
	return currentUser.Uid == "0"
}

func findDaemonBinary() (string, error) {
	daemonBin, err := exec.LookPath("hpk-net-daemon")
	if err == nil {
		return daemonBin, nil
	}
	// Try next to executable
	exe, _ := os.Executable()
	candidate := filepath.Join(filepath.Dir(exe), "hpk-net-daemon")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("hpk-net-daemon binary not found")
}

func pollForSocket(path string) error {
	for i := 0; i < 40; i++ { // 2 seconds max
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("socket %s not found", path)
}

func pollForTAP(name string) (netlink.Link, error) {
	log.Printf("Waiting for TAP %s...", name)
	for i := 0; i < 100; i++ { // 5 seconds max
		l, err := netlink.LinkByName(name)
		if err == nil {
			return l, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for TAP %s", name)
}

func configureTAP(name string, link netlink.Link, podIP net.IP) error {
	// Enable Proxy ARP and PVLAN Proxy ARP on host tap interface
	proxyArpPath := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/proxy_arp", name)
	if err := os.WriteFile(proxyArpPath, []byte("1"), 0644); err != nil {
		log.Printf("Warning: failed to enable proxy_arp on %s: %v", name, err)
	}
	proxyArpPvlanPath := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/proxy_arp_pvlan", name)
	if err := os.WriteFile(proxyArpPvlanPath, []byte("1"), 0644); err != nil {
		log.Printf("Warning: failed to enable proxy_arp_pvlan on %s: %v", name, err)
	}

	// Add point-to-point route to pod container IP (with /32 prefix)
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &net.IPNet{IP: podIP, Mask: net.CIDRMask(32, 32)},
		Scope:     netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("failed to add route to pod %s via %s: %w", podIP, name, err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		log.Printf("Warning: failed to set tap up from host: %v", err)
	}

	log.Printf("Point-to-point route to pod %s configured via %s", podIP, name)
	return nil
}

func buildApptainerCommand(containerIP, gwIP, socketPath, mtuStr string) (*exec.Cmd, error) {
	userArgs := os.Args[1:]
	if len(userArgs) == 0 {
		return nil, fmt.Errorf("no arguments provided")
	}

	cmdOp := userArgs[0]
	cmdsWithNet := map[string]bool{"run": true, "shell": true, "exec": true, "instance": true}

	var finalArgs []string
	if cmdOp == "instance" && len(userArgs) > 1 && userArgs[1] == "start" {
		finalArgs = append(finalArgs, "instance", "start")
		finalArgs = append(finalArgs, "--network", "none", "--bind", SocketDir)
		finalArgs = append(finalArgs, userArgs[2:]...)
	} else if cmdsWithNet[cmdOp] {
		finalArgs = append(finalArgs, cmdOp)
		finalArgs = append(finalArgs, "--network", "none", "--bind", SocketDir)
		finalArgs = append(finalArgs, userArgs[1:]...)
	} else {
		log.Printf("Unknown or non-network command '%s', passing through without network config", cmdOp)
		finalArgs = append(finalArgs, userArgs...)
	}

	log.Printf("Executing apptainer: %v", finalArgs)

	runCmd := exec.Command("apptainer", finalArgs...)
	runCmd.Stdin = os.Stdin
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr

	envVars := []string{
		fmt.Sprintf("HPK_IP=%s", containerIP),
		fmt.Sprintf("HPK_GATEWAY_IP=%s", gwIP),
		fmt.Sprintf("HPK_SOCKET_PATH=%s", socketPath),
		fmt.Sprintf("HPK_MTU=%s", mtuStr),
	}

	hostEnv := os.Environ()
	tmpDir, cacheDir, err := ensureApptainerRuntimeDirs()
	if err != nil {
		return nil, fmt.Errorf("failed to prepare apptainer runtime dirs: %w", err)
	}

	hostEnv = append(hostEnv,
		"APPTAINER_TMPDIR="+tmpDir,
		"SINGULARITY_TMPDIR="+tmpDir,
		"TMPDIR="+tmpDir,
		"APPTAINER_CACHEDIR="+cacheDir,
		"SINGULARITY_CACHEDIR="+cacheDir,
	)

	for _, kv := range envVars {
		k, v, _ := strings.Cut(kv, "=")
		hostEnv = append(hostEnv, "APPTAINERENV_"+k+"="+v)
	}

	runCmd.Env = hostEnv
	return runCmd, nil
}

func run() int {
	// 1. Check Root
	if !isRoot() {
		log.Println("hpktainer must be run as root to configure networking.")
		return 1
	}

	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("hpktainer version: %s (built: %s)\n", version.Version, version.BuildTime)
		return 0
	}

	// 2. Parse Calico Config
	calicoConf, err := network.ParseCalicoConfig(CalicoConfig)
	if err != nil {
		log.Printf("Failed to parse calico config at %s: %v. Is Calico running?", CalicoConfig, err)
		return 1
	}
	log.Printf("Using Subnet: %s", calicoConf.Subnet)

	// Gateway IP is hardcoded as link-local 169.254.1.1
	gwIP := "169.254.1.1"

	// 3. Allocate IP via CNI
	var tapLink netlink.Link
	var hostTapName string

	containerID := uuid.New().String()
	containerIP, err := network.AllocateIP(containerID, calicoConf.Subnet, CNIDataDir)
	if err != nil {
		log.Printf("Failed to allocate IP: %v", err)
		return 1
	}
	defer func() {
		if tapLink != nil {
			if _, err := netlink.LinkByName(hostTapName); err == nil {
				if err := netlink.LinkDel(tapLink); err != nil {
					log.Printf("Warning: failed to delete TAP %s on exit: %v", hostTapName, err)
				} else {
					log.Printf("Deleted TAP interface %s on exit", hostTapName)
				}
			}
		}
		if err := network.ReleaseIP(containerID, calicoConf.Subnet, CNIDataDir); err != nil {
			log.Printf("Failed to release IP: %v", err)
		}
	}()
	log.Printf("Allocated IP %s for container %s", containerIP, containerID)

	// Parse IP for TAP and Socket
	ip, _, err := net.ParseCIDR(containerIP)
	if err != nil {
		log.Printf("Invalid container IP format: %v", err)
		return 1
	}

	ipv4 := ip.To4()
	if ipv4 == nil {
		log.Println("Non-IPv4 address not supported yet")
		return 1
	}
	// Derive TAP name using hex representation of full IP (e.g. hpk-0af40105)
	hostTapName = fmt.Sprintf("hpk-%02x%02x%02x%02x", ipv4[0], ipv4[1], ipv4[2], ipv4[3])

	if err := os.MkdirAll(SocketDir, 0755); err != nil {
		log.Printf("Failed to create socket dir: %v", err)
		return 1
	}

	socketPath := filepath.Join(SocketDir, ip.String()+".sock")
	os.Remove(socketPath) // ignore error

	// Find daemon binary
	daemonBin, err := findDaemonBinary()
	if err != nil {
		log.Printf("Error finding daemon: %v", err)
		return 1
	}

	mtuStr := calicoConf.MTU
	if mtuStr == "" {
		mtuStr = "1500"
	}

	daemonCmd := exec.Command(daemonBin,
		"-mode", "server",
		"-socket", socketPath,
		"-tap", hostTapName,
		"-create-tap",
		"-mtu", mtuStr,
	)

	daemonCmd.Stdout = os.Stdout
	daemonCmd.Stderr = os.Stderr

	if err := daemonCmd.Start(); err != nil {
		log.Printf("Failed to start daemon: %v", err)
		return 1
	}
	defer func() {
		if daemonCmd.Process != nil {
			daemonCmd.Process.Kill()
			daemonCmd.Wait()
		}
	}()

	// Poll for socket path existence
	if err := pollForSocket(socketPath); err != nil {
		log.Printf("Warning: Socket %s not found yet: %v", socketPath, err)
	}

	// Poll for TAP creation by daemon
	tapLink, err = pollForTAP(hostTapName)
	if err != nil {
		log.Printf("Failed to find TAP %s: %v", hostTapName, err)
		return 1
	}

	// Configure Proxy ARP and routing on the TAP
	if err := configureTAP(hostTapName, tapLink, ip); err != nil {
		log.Printf("Failed to configure TAP link: %v", err)
		return 1
	}

	// 4. Run Apptainer
	runCmd, err := buildApptainerCommand(containerIP, gwIP, socketPath, mtuStr)
	if err != nil {
		log.Printf("Failed to prepare apptainer command: %v", err)
		return 1
	}

	// Handle signals to propagate to child
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)

	if err := runCmd.Start(); err != nil {
		log.Printf("Failed to start apptainer: %v", err)
		return 1
	}

	// Goroutine for signal propagation
	doneSig := make(chan struct{})
	defer close(doneSig)
	go func() {
		select {
		case sig := <-sigs:
			if runCmd.Process != nil {
				runCmd.Process.Signal(sig)
			}
		case <-doneSig:
		}
	}()

	err = runCmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			log.Printf("Apptainer exited with error: %v", err)
			return exitErr.ExitCode()
		}
		log.Printf("Apptainer exited with error: %v", err)
		return 1
	}

	return 0
}
