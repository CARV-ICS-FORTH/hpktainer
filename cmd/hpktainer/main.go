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
	SocketDir     = "/var/run/hpktainer"
	CNIDataDir    = "/var/lib/cni/networks/hpktainer"
	FlannelConfig = "/run/flannel/subnet.env"
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
	// 1. Check Root
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("hpktainer version: %s (built: %s)\n", version.Version, version.BuildTime)
		os.Exit(0)
	}

	currentUser, err := user.Current()
	if err != nil {
		log.Fatalf("Failed to get current user: %v", err)
	}
	if currentUser.Uid != "0" {
		log.Fatal("hpktainer must be run as root to configure networking.")
	}

	// 2. Parse Flannel Config
	flannelConf, err := network.ParseFlannelConfig(FlannelConfig)
	if err != nil {
		log.Fatalf("Failed to parse flannel config at %s: %v. Is flannel running?", FlannelConfig, err)
	}
	log.Printf("Using Subnet: %s", flannelConf.Subnet)

	// 3. Ensure Bridge and IPTables
	gwIP, err := network.EnsureBridge(flannelConf.Subnet)
	if err != nil {
		log.Fatalf("Failed to setup bridge: %v", err)
	}
	log.Printf("Bridge %s ready with IP %s", network.BridgeName, gwIP)

	defaultIface, err := network.GetDefaultInterface()
	if err != nil {
		log.Fatalf("Failed to get default interface: %v", err)
	}
	log.Printf("Default interface: %s", defaultIface)

	if err := network.EnsureIPTablesMasquerade(flannelConf.Subnet, defaultIface); err != nil {
		log.Fatalf("Failed to setup iptables: %v", err)
	}

	// 4. Allocate IP via CNI
	containerID := uuid.New().String()
	containerIP, err := network.AllocateIP(containerID, flannelConf.Subnet, CNIDataDir)
	if err != nil {
		log.Fatalf("Failed to allocate IP: %v", err)
	}
	defer func() {
		if err := network.ReleaseIP(containerID, flannelConf.Subnet, CNIDataDir); err != nil {
			log.Printf("Failed to release IP: %v", err)
		}
	}()
	log.Printf("Allocated IP %s for container %s", containerIP, containerID)

	// Parse IP for TAP and Socket
	ip, _, err := net.ParseCIDR(containerIP)
	if err != nil {
		log.Fatalf("Invalid container IP format: %v", err)
	}

	// 5. Host Tap Setup
	// Tap name: hpk-tap-<LastOctet>
	// ip was parsed above for socket path.
	// ip is net.IP
	ipv4 := ip.To4()
	if ipv4 == nil {
		log.Fatal("Non-IPv4 address not supported yet")
	}
	lastOctet := ipv4[3]
	hostTapName := fmt.Sprintf("hpk-tap-%d", lastOctet)

	// We delegate TAP creation to the daemon to ensure correct flags/ownership.
	// Logic moved to after daemon start.

	if err := os.MkdirAll(SocketDir, 0755); err != nil {
		log.Fatalf("Failed to create socket dir: %v", err)
	}

	socketPath := filepath.Join(SocketDir, ip.String()+".sock")

	// Clean up socket if exists (daemon typically handles it but we can ensure)
	os.Remove(socketPath) // ignore error

	// We assume hpk-net-daemon is in PATH or same dir.
	// Let's try to find it.
	daemonBin, err := exec.LookPath("hpk-net-daemon")
	if err != nil {
		// Try next to executable
		exe, _ := os.Executable()
		candidate := filepath.Join(filepath.Dir(exe), "hpk-net-daemon")
		if _, err := os.Stat(candidate); err == nil {
			daemonBin = candidate
		} else {
			log.Fatal("hpk-net-daemon binary not found")
		}
	}

	daemonCmd := exec.Command(daemonBin,
		"-mode", "server",
		"-socket", socketPath,
		"-tap", hostTapName,
		"-create-tap", "true",
	)

	// Forward daemon logs for debug? Or file?
	// Let's pipe to stdout for now or separate.
	daemonCmd.Stdout = os.Stdout
	daemonCmd.Stderr = os.Stderr

	if err := daemonCmd.Start(); err != nil {
		log.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		if daemonCmd.Process != nil {
			daemonCmd.Process.Kill()
		}
	}()

	// Wait a bit for socket to be created? Daemon "Listening on..."
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(socketPath); err != nil {
		log.Printf("Warning: Socket %s not found yet", socketPath)
	}

	// Post-Validation: Wait for TAP creation by daemon and attach to bridge
	log.Printf("Waiting for TAP %s...", hostTapName)
	var tapLink netlink.Link
	for i := 0; i < 50; i++ { // 5 seconds
		l, err := netlink.LinkByName(hostTapName)
		if err == nil {
			tapLink = l
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if tapLink == nil {
		log.Fatalf("Timeout waiting for TAP %s", hostTapName)
	}

	// Ensure we delete it on exit (though daemon exit might close it if it's not persistent?
	// water might create persistent if not handled right, but usually closes on fd close.
	// If it closes on daemon exit, we don't need manual delete.
	// But let's verify bridging requirements.)

	// Add to Bridge
	bridgeLink, err := netlink.LinkByName(network.BridgeName)
	if err != nil {
		daemonCmd.Process.Kill()
		log.Fatalf("Failed to find bridge %s: %v", network.BridgeName, err)
	}
	if err := netlink.LinkSetMaster(tapLink, bridgeLink.(*netlink.Bridge)); err != nil {
		daemonCmd.Process.Kill()
		log.Fatalf("Failed to add tap to bridge: %v", err)
	}
	if err := netlink.LinkSetUp(tapLink); err != nil {
		log.Printf("Warning: failed to set tap up from host (daemon should have done it): %v", err)
	}
	log.Printf("Added %s to bridge %s", hostTapName, network.BridgeName)

	// 7. Run Apptainer
	// Args: everything passed to this cli.
	// Except we need to inject flags.
	// The user might pass `hpktainer run instance://...` or `hpktainer exec ...` or `hpktainer shell ...`
	// Typically `apptainer run [options] <image> [args]`
	// We want to inject `--network none --bind /var/run/hpktainer` at the right place.
	// And ENV variables.

	// Construct args
	userArgs := os.Args[1:]
	// Wait, hpktainer handles "run", "shell", "exec", "instance start"?
	// The requirement: "It will use apptainer to run containers, passing through all arguments to apptainer except those that refer to network configuration."
	// So if user types `hpktainer shell img.sif`, we run `apptainer shell ...`.

	// We need to find where to insert flags. apptainer commands often accept global flags and command flags.
	// Simplest: `apptainer [userArgs] --network none --bind ...` might put flags after image if not careful.
	// Apptainer syntax: `apptainer [global options] command [command options] [args]`
	// e.g. `apptainer run --network none img.sif` works.
	// But `apptainer run img.sif --network none` implies args to the image?
	// Usually flags for apptainer must be before the image.

	// Strategy: Prepend our flags to the arguments, but we need to respect the command (run/shell/exec).
	// If first arg is run/shell/exec, we insert flags AFTER it.
	// If first arg is an image (implicit run?), we insert flags BEFORE it?
	// Apptainer usually requires explicit command or treats it as run?
	// Actually `apptainer myimage.sif` works? No, `apptainer` is the binary. `apptainer run myimage.sif`.
	// If user runs `hpktainer myimage.sif`? User probably runs `hpktainer run ...`.
	// Let's assume user provides the subcommand.

	// If user calls `hpktainer run -B /foo:/bar image.sif arg1`, we want `apptainer run --network none --bind ... -B /foo:/bar image.sif arg1`.

	if len(userArgs) == 0 {
		log.Fatal("No arguments provided")
	}

	cmdOp := userArgs[0]
	// If it's a known command that accepts network flags: run, shell, exec, instance start.
	// test?

	// We'll insert our flags immediately after the subcommand.
	// If the first arg is NOT a subcommand, we assume "run"?
	// Apptainer help says: "Usage: apptainer [global options] <command> [args]"
	// So we assume the first arg is the command.

	cmdsWithNet := map[string]bool{"run": true, "shell": true, "exec": true, "instance": true /* start? */}

	var finalArgs []string

	// Env variables
	envVars := []string{
		fmt.Sprintf("HPK_IP=%s", containerIP),
		fmt.Sprintf("HPK_GATEWAY_IP=%s", gwIP),
		fmt.Sprintf("HPK_SOCKET_PATH=%s", socketPath),
	}

	// We'll set these in the environment of the executed command.
	// Apptainer passes env vars prefixed with APPTAINERENV_ or defaults?
	// We can use SINGULARITYENV_ / APPTAINERENV_ prefix to pass them into container.
	hostEnv := os.Environ()
	tmpDir, cacheDir, err := ensureApptainerRuntimeDirs()
	if err != nil {
		log.Fatalf("Failed to prepare apptainer runtime dirs: %v", err)
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

	// Reconstruct args
	// We insert `--network none --bind /var/run/hpktainer`.
	// Check if "instance start" is used (2 words).

	if cmdOp == "instance" && len(userArgs) > 1 && userArgs[1] == "start" {
		// handle instance start
		finalArgs = append(finalArgs, "instance", "start")
		finalArgs = append(finalArgs, "--network", "none", "--bind", SocketDir)
		finalArgs = append(finalArgs, userArgs[2:]...)
	} else if cmdsWithNet[cmdOp] {
		finalArgs = append(finalArgs, cmdOp)
		finalArgs = append(finalArgs, "--network", "none", "--bind", SocketDir)
		finalArgs = append(finalArgs, userArgs[1:]...)
	} else {
		// Just pass through? Or assume implicit run?
		// If user typed `hpktainer image.sif`, maybe they expect `apptainer run image.sif`?
		// But if they typed `hpktainer --version`, we shouldn't add network flags.
		// If it's a flag, likely global option or unknown command.
		// We'll just pass through if not strictly a container execution command.
		// Warn user?
		log.Printf("Unknown or non-network command '%s', passing through without network config", cmdOp)
		finalArgs = append(finalArgs, userArgs...)
	}

	log.Printf("Executing apptainer: %v", finalArgs)

	runCmd := exec.Command("apptainer", finalArgs...)
	runCmd.Stdin = os.Stdin
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	runCmd.Env = hostEnv

	// Handle signals to propagate to child?
	// exec.Command starts a process. We wait for it.

	// Create a channel to catch signals
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	if err := runCmd.Start(); err != nil {
		log.Fatalf("Failed to start apptainer: %v", err)
	}

	// Wait for process in another goroutine or select
	done := make(chan error, 1)
	go func() {
		done <- runCmd.Wait()
	}()

	select {
	case <-sigs:
		// Forward signal?
		if runCmd.Process != nil {
			runCmd.Process.Signal(syscall.SIGTERM)
		}
		// Wait for exit
		<-done
	case err := <-done:
		if err != nil {
			// Extract exit code
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			log.Fatalf("Apptainer exited with error: %v", err)
		}
	}

	// Cleanup happens via defers (daemon kill, release IP, del tap)
}
