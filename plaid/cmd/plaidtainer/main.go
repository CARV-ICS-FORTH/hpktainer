package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"plaid/pkg/api"
	"plaid/pkg/ipam"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	apptainerBin, err := findApptainerBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	globalOpts, remaining := parsePlaidOptions(os.Args[1:])
	if len(remaining) < 1 {
		printUsage()
		os.Exit(1)
	}

	cmd := remaining[0]
	args := remaining[1:]

	switch cmd {
	case "version", "--version", "-v":
		fmt.Printf("plaidtainer version %s (Plaid Apptainer execution wrapper)\n", version)
		// Also invoke underlying apptainer version
		_ = runCommand(apptainerBin, []string{"version"})
		os.Exit(0)

	case "help", "--help", "-h":
		printUsage()
		os.Exit(0)

	case "instance":
		handleInstance(apptainerBin, globalOpts, args)

	case "exec":
		handleExec(apptainerBin, globalOpts, args)

	case "run":
		handleRun(apptainerBin, globalOpts, args)

	case "shell":
		handleShell(apptainerBin, globalOpts, args)

	default:
		// Forward any other Apptainer command directly (build, inspect, pull, etc.)
		code := runCommand(apptainerBin, remaining)
		os.Exit(code)
	}
}

func handleInstance(apptainerBin string, globalOpts PlaidOptions, args []string) {
	if len(args) == 0 {
		code := runCommand(apptainerBin, []string{"instance"})
		os.Exit(code)
	}

	subCmd := args[0]
	subArgs := args[1:]

	switch subCmd {
	case "start", "run":
		handleInstanceStartOrRun(apptainerBin, globalOpts, subCmd, subArgs)
	case "stop":
		handleInstanceStop(apptainerBin, globalOpts, subArgs)
	default:
		// Forward commands like "instance list", "instance stats"
		passArgs := append([]string{"instance", subCmd}, subArgs...)
		code := runCommand(apptainerBin, passArgs)
		os.Exit(code)
	}
}

func handleInstanceStartOrRun(apptainerBin string, globalOpts PlaidOptions, action string, args []string) {
	cmdOpts, remaining := parsePlaidOptions(args)
	opts := mergePlaidOptions(globalOpts, cmdOpts)
	opts.EnsureDefaults()

	// Extract flags, image, and instance name
	flags, posArgs := splitFlagsAndPositional(remaining)
	if len(posArgs) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: plaidtainer instance %s [options] <image> <instance-name> [args...]\n", action)
		os.Exit(1)
	}

	imagePath := posArgs[0]
	instanceName := posArgs[1]
	instanceArgs := posArgs[2:]

	if opts.HostNetworking {
		apptainerArgs := []string{"instance", action}
		if opts.DNS != "" {
			apptainerArgs = append(apptainerArgs, "--dns", opts.DNS)
		}
		for _, b := range opts.Binds {
			apptainerArgs = append(apptainerArgs, "--bind", b)
		}
		if len(opts.Binds) == 0 && opts.Bind != "" {
			apptainerArgs = append(apptainerArgs, "--bind", opts.Bind)
		}
		apptainerArgs = append(apptainerArgs, flags...)
		apptainerArgs = append(apptainerArgs, imagePath, instanceName)
		apptainerArgs = append(apptainerArgs, instanceArgs...)

		fmt.Printf("[plaidtainer] Starting instance %q with host networking...\n", instanceName)
		code := runCommand(apptainerBin, apptainerArgs)
		os.Exit(code)
	}

	// Plaid User-Space Networking
	client := api.NewClient(opts.Socket)
	status, err := client.GetStatus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot connect to plaidd API at %s: %v\n", opts.Socket, err)
		os.Exit(1)
	}

	ipStr := opts.IP
	dynamicallyAllocated := false
	if ipStr == "" {
		allocatedIP, allocatedGW, err := ipam.AllocateIP(instanceName, opts.Socket)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: IPAM allocation failed: %v\n", err)
			os.Exit(1)
		}
		ipStr = allocatedIP
		if opts.Gateway == "" {
			opts.Gateway = allocatedGW
		}
		dynamicallyAllocated = true
	}

	_, gwStr, dnsStr := resolveNetworkParams(status, ipStr, opts.Gateway, opts.DNS)
	hostPlaidBin := findHostPlaidBinary()

	apptainerArgs := []string{
		"instance", action,
		"--net", "--network=none",
		"--dns", dnsStr,
		"--bind", filepath.Dir(opts.Socket),
	}
	if os.Geteuid() != 0 && !hasFakeroot(flags) {
		apptainerArgs = append(apptainerArgs, "--fakeroot")
	}
	if hostPlaidBin != "" {
		apptainerArgs = append(apptainerArgs, "--bind", hostPlaidBin+":"+opts.Binary)
	}
	for _, b := range opts.Binds {
		apptainerArgs = append(apptainerArgs, "--bind", b)
	}
	if len(opts.Binds) == 0 && opts.Bind != "" {
		apptainerArgs = append(apptainerArgs, "--bind", opts.Bind)
	}
	apptainerArgs = append(apptainerArgs, flags...)
	apptainerArgs = append(apptainerArgs, imagePath, instanceName)
	apptainerArgs = append(apptainerArgs, instanceArgs...)

	fmt.Printf("[plaidtainer] Launching Apptainer instance %q with Plaid networking...\n", instanceName)
	cmdStart := exec.Command(apptainerBin, apptainerArgs...)
	cmdStart.Stdout = os.Stdout
	cmdStart.Stderr = os.Stderr
	if err := cmdStart.Run(); err != nil {
		if dynamicallyAllocated {
			_ = ipam.ReleaseIP(instanceName, opts.Socket)
		}
		fmt.Fprintf(os.Stderr, "Error: apptainer instance %s failed: %v\n", action, err)
		os.Exit(1)
	}

	// Initialize networking in running instance
	fmt.Printf("[plaidtainer] Initializing Plaid network on %q (IP: %s, GW: %s)...\n", instanceName, ipStr, gwStr)
	initArgs := []string{
		"exec",
		"instance://" + instanceName,
		opts.Binary, "init",
		"--ip", ipStr,
		"--gateway", gwStr,
		"--socket", opts.Socket,
		"--container-id", instanceName,
	}

	cmdInit := exec.Command(apptainerBin, initArgs...)
	out, err := cmdInit.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing Plaid inside instance: %v (%s)\n", err, strings.TrimSpace(string(out)))
		_ = exec.Command(apptainerBin, "instance", "stop", instanceName).Run()
		if dynamicallyAllocated {
			_ = ipam.ReleaseIP(instanceName, opts.Socket)
		}
		os.Exit(1)
	}

	fmt.Printf("[plaidtainer] Instance %q is online and connected to Plaid network!\n", instanceName)
	fmt.Printf("             IP:      %s\n", ipStr)
	fmt.Printf("             Gateway: %s\n", gwStr)
	fmt.Printf("             DNS:     %s\n", dnsStr)
}

func handleInstanceStop(apptainerBin string, globalOpts PlaidOptions, args []string) {
	cmdOpts, remaining := parsePlaidOptions(args)
	opts := mergePlaidOptions(globalOpts, cmdOpts)
	opts.EnsureDefaults()
	_, posArgs := splitFlagsAndPositional(remaining)

	if len(posArgs) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: plaidtainer instance stop [options] <instance-name>")
		os.Exit(1)
	}

	instanceName := posArgs[0]

	// 1. Tell plaidd to remove endpoint
	client := api.NewClient(opts.Socket)
	_ = client.RemoveEndpoint(instanceName, instanceName)

	// 2. Release IPAM allocation
	_ = ipam.ReleaseIP(instanceName, opts.Socket)

	// 3. Stop Apptainer instance
	fmt.Printf("[plaidtainer] Stopping Apptainer instance %q...\n", instanceName)
	stopArgs := append([]string{"instance", "stop"}, remaining...)
	code := runCommand(apptainerBin, stopArgs)
	os.Exit(code)
}

func handleExec(apptainerBin string, globalOpts PlaidOptions, args []string) {
	cmdOpts, remaining := parsePlaidOptions(args)
	opts := mergePlaidOptions(globalOpts, cmdOpts)
	opts.EnsureDefaults()
	flags, image, cmdArgs := splitImageAndCommand(remaining)

	if image == "" || len(cmdArgs) == 0 {
		// Passthrough or show apptainer exec help
		code := runCommand(apptainerBin, append([]string{"exec"}, args...))
		os.Exit(code)
	}

	// Case A: target is an existing instance (e.g. instance://name)
	if strings.HasPrefix(image, "instance://") {
		passArgs := append([]string{"exec"}, remaining...)
		code := runCommand(apptainerBin, passArgs)
		os.Exit(code)
	}

	// Case B: host networking requested
	if opts.HostNetworking {
		apptainerArgs := []string{"exec"}
		if opts.DNS != "" {
			apptainerArgs = append(apptainerArgs, "--dns", opts.DNS)
		}
		for _, b := range opts.Binds {
			apptainerArgs = append(apptainerArgs, "--bind", b)
		}
		if len(opts.Binds) == 0 && opts.Bind != "" {
			apptainerArgs = append(apptainerArgs, "--bind", opts.Bind)
		}
		apptainerArgs = append(apptainerArgs, flags...)
		apptainerArgs = append(apptainerArgs, image)
		apptainerArgs = append(apptainerArgs, cmdArgs...)

		code := runCommand(apptainerBin, apptainerArgs)
		os.Exit(code)
	}

	// Case C: Plaid user-space networking for one-off execution
	client := api.NewClient(opts.Socket)
	status, err := client.GetStatus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot connect to plaidd API at %s: %v\n", opts.Socket, err)
		os.Exit(1)
	}

	containerID := fmt.Sprintf("plaidtainer-exec-%d", time.Now().UnixNano()%10000000)
	ipStr := opts.IP
	dynamicallyAllocated := false
	if ipStr == "" {
		allocatedIP, allocatedGW, err := ipam.AllocateIP(containerID, opts.Socket)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: IPAM allocation failed: %v\n", err)
			os.Exit(1)
		}
		ipStr = allocatedIP
		if opts.Gateway == "" {
			opts.Gateway = allocatedGW
		}
		dynamicallyAllocated = true
	}

	_, gwStr, dnsStr := resolveNetworkParams(status, ipStr, opts.Gateway, opts.DNS)
	hostPlaidBin := findHostPlaidBinary()

	cleanupDone := false
	cleanup := func() {
		if cleanupDone {
			return
		}
		cleanupDone = true
		_ = client.RemoveEndpoint(containerID, containerID)
		if dynamicallyAllocated {
			_ = ipam.ReleaseIP(containerID, opts.Socket)
		}
	}

	apptainerArgs := []string{
		"exec",
		"--net", "--network=none",
		"--dns", dnsStr,
		"--bind", filepath.Dir(opts.Socket),
	}
	if os.Geteuid() != 0 && !hasFakeroot(flags) {
		apptainerArgs = append(apptainerArgs, "--fakeroot")
	}
	if hostPlaidBin != "" {
		apptainerArgs = append(apptainerArgs, "--bind", hostPlaidBin+":"+opts.Binary)
	}
	for _, b := range opts.Binds {
		apptainerArgs = append(apptainerArgs, "--bind", b)
	}
	if len(opts.Binds) == 0 && opts.Bind != "" {
		apptainerArgs = append(apptainerArgs, "--bind", opts.Bind)
	}
	apptainerArgs = append(apptainerArgs, flags...)
	apptainerArgs = append(apptainerArgs, image)

	// Wrap execution with plaid exec to configure TAP before running target command
	apptainerArgs = append(apptainerArgs,
		opts.Binary, "exec",
		"--ip", ipStr,
		"--gateway", gwStr,
		"--socket", opts.Socket,
		"--container-id", containerID,
		"--",
	)
	apptainerArgs = append(apptainerArgs, cmdArgs...)

	code := runCommandWithCleanup(apptainerBin, apptainerArgs, cleanup)
	os.Exit(code)
}

func handleRun(apptainerBin string, globalOpts PlaidOptions, args []string) {
	cmdOpts, remaining := parsePlaidOptions(args)
	opts := mergePlaidOptions(globalOpts, cmdOpts)
	opts.EnsureDefaults()
	flags, image, cmdArgs := splitImageAndCommand(remaining)

	if image == "" {
		code := runCommand(apptainerBin, append([]string{"run"}, args...))
		os.Exit(code)
	}

	if opts.HostNetworking {
		apptainerArgs := []string{"run"}
		if opts.DNS != "" {
			apptainerArgs = append(apptainerArgs, "--dns", opts.DNS)
		}
		for _, b := range opts.Binds {
			apptainerArgs = append(apptainerArgs, "--bind", b)
		}
		if len(opts.Binds) == 0 && opts.Bind != "" {
			apptainerArgs = append(apptainerArgs, "--bind", opts.Bind)
		}
		apptainerArgs = append(apptainerArgs, flags...)
		apptainerArgs = append(apptainerArgs, image)
		apptainerArgs = append(apptainerArgs, cmdArgs...)

		code := runCommand(apptainerBin, apptainerArgs)
		os.Exit(code)
	}

	// Plaid User-Space Networking
	client := api.NewClient(opts.Socket)
	status, err := client.GetStatus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot connect to plaidd API at %s: %v\n", opts.Socket, err)
		os.Exit(1)
	}

	containerID := fmt.Sprintf("plaidtainer-run-%d", time.Now().UnixNano()%10000000)
	ipStr := opts.IP
	dynamicallyAllocated := false
	if ipStr == "" {
		allocatedIP, allocatedGW, err := ipam.AllocateIP(containerID, opts.Socket)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: IPAM allocation failed: %v\n", err)
			os.Exit(1)
		}
		ipStr = allocatedIP
		if opts.Gateway == "" {
			opts.Gateway = allocatedGW
		}
		dynamicallyAllocated = true
	}

	_, gwStr, dnsStr := resolveNetworkParams(status, ipStr, opts.Gateway, opts.DNS)
	hostPlaidBin := findHostPlaidBinary()

	cleanupDone := false
	cleanup := func() {
		if cleanupDone {
			return
		}
		cleanupDone = true
		_ = client.RemoveEndpoint(containerID, containerID)
		if dynamicallyAllocated {
			_ = ipam.ReleaseIP(containerID, opts.Socket)
		}
	}

	apptainerArgs := []string{
		"run",
		"--net", "--network=none",
		"--dns", dnsStr,
		"--bind", filepath.Dir(opts.Socket),
		"--env", "PLAID_IP=" + ipStr,
		"--env", "PLAID_GATEWAY_IP=" + gwStr,
		"--env", "PLAID_DNS_IP=" + dnsStr,
		"--env", "PLAID_SOCKET_PATH=" + opts.Socket,
		"--env", "PLAID_CONTAINER_ID=" + containerID,
	}
	if os.Geteuid() != 0 && !hasFakeroot(flags) {
		apptainerArgs = append(apptainerArgs, "--fakeroot")
	}
	if hostPlaidBin != "" {
		apptainerArgs = append(apptainerArgs, "--bind", hostPlaidBin+":"+opts.Binary)
	}
	for _, b := range opts.Binds {
		apptainerArgs = append(apptainerArgs, "--bind", b)
	}
	if len(opts.Binds) == 0 && opts.Bind != "" {
		apptainerArgs = append(apptainerArgs, "--bind", opts.Bind)
	}
	apptainerArgs = append(apptainerArgs, flags...)
	apptainerArgs = append(apptainerArgs, image)

	if len(cmdArgs) > 0 {
		// Use plaid exec to initialize TAP and run user command
		apptainerArgs = append(apptainerArgs,
			opts.Binary, "exec",
			"--ip", ipStr,
			"--gateway", gwStr,
			"--socket", opts.Socket,
			"--container-id", containerID,
			"--",
		)
		apptainerArgs = append(apptainerArgs, cmdArgs...)
	}

	code := runCommandWithCleanup(apptainerBin, apptainerArgs, cleanup)
	os.Exit(code)
}

func handleShell(apptainerBin string, globalOpts PlaidOptions, args []string) {
	cmdOpts, remaining := parsePlaidOptions(args)
	opts := mergePlaidOptions(globalOpts, cmdOpts)
	opts.EnsureDefaults()
	flags, posArgs := splitFlagsAndPositional(remaining)

	if len(posArgs) == 0 {
		code := runCommand(apptainerBin, append([]string{"shell"}, args...))
		os.Exit(code)
	}

	target := posArgs[0]
	if strings.HasPrefix(target, "instance://") || opts.HostNetworking {
		apptainerArgs := append([]string{"shell"}, remaining...)
		code := runCommand(apptainerBin, apptainerArgs)
		os.Exit(code)
	}

	// Plaid User-Space Networking
	client := api.NewClient(opts.Socket)
	status, err := client.GetStatus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot connect to plaidd API at %s: %v\n", opts.Socket, err)
		os.Exit(1)
	}

	containerID := fmt.Sprintf("plaidtainer-shell-%d", time.Now().UnixNano()%10000000)
	ipStr := opts.IP
	dynamicallyAllocated := false
	if ipStr == "" {
		allocatedIP, allocatedGW, err := ipam.AllocateIP(containerID, opts.Socket)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: IPAM allocation failed: %v\n", err)
			os.Exit(1)
		}
		ipStr = allocatedIP
		if opts.Gateway == "" {
			opts.Gateway = allocatedGW
		}
		dynamicallyAllocated = true
	}

	_, gwStr, dnsStr := resolveNetworkParams(status, ipStr, opts.Gateway, opts.DNS)
	hostPlaidBin := findHostPlaidBinary()

	cleanupDone := false
	cleanup := func() {
		if cleanupDone {
			return
		}
		cleanupDone = true
		_ = client.RemoveEndpoint(containerID, containerID)
		if dynamicallyAllocated {
			_ = ipam.ReleaseIP(containerID, opts.Socket)
		}
	}

	// Use apptainer exec with interactive shell
	apptainerArgs := []string{
		"exec",
		"--net", "--network=none",
		"--dns", dnsStr,
		"--bind", filepath.Dir(opts.Socket),
	}
	if os.Geteuid() != 0 && !hasFakeroot(flags) {
		apptainerArgs = append(apptainerArgs, "--fakeroot")
	}
	if hostPlaidBin != "" {
		apptainerArgs = append(apptainerArgs, "--bind", hostPlaidBin+":"+opts.Binary)
	}
	for _, b := range opts.Binds {
		apptainerArgs = append(apptainerArgs, "--bind", b)
	}
	if len(opts.Binds) == 0 && opts.Bind != "" {
		apptainerArgs = append(apptainerArgs, "--bind", opts.Bind)
	}
	apptainerArgs = append(apptainerArgs, flags...)
	apptainerArgs = append(apptainerArgs, target)
	apptainerArgs = append(apptainerArgs,
		opts.Binary, "exec",
		"--ip", ipStr,
		"--gateway", gwStr,
		"--socket", opts.Socket,
		"--container-id", containerID,
		"--",
		"/bin/sh",
	)

	code := runCommandWithCleanup(apptainerBin, apptainerArgs, cleanup)
	os.Exit(code)
}

func resolveNetworkParams(status *api.Response, ipStr, gwStr, dnsStr string) (string, string, string) {
	if gwStr == "" {
		if status != nil && status.GatewayIP != "" {
			gwStr = status.GatewayIP
		} else if ip, ipNet, err := net.ParseCIDR(ipStr); err == nil && ip.To4() != nil {
			base := make(net.IP, len(ip.To4()))
			copy(base, ip.To4().Mask(ipNet.Mask))
			base[3] = 1
			gwStr = base.String()
		}
	}

	if dnsStr == "" {
		if gwIP := net.ParseIP(gwStr); gwIP != nil && gwIP.To4() != nil {
			dns := make(net.IP, len(gwIP.To4()))
			copy(dns, gwIP.To4())
			dns[3] = 3
			dnsStr = dns.String()
		} else {
			dnsStr = "1.1.1.1"
		}
	}

	return ipStr, gwStr, dnsStr
}

// splitFlagsAndPositional splits arguments into leading flags and remaining positional arguments.
func splitFlagsAndPositional(args []string) (flags []string, posArgs []string) {
	inPositional := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if inPositional {
			posArgs = append(posArgs, arg)
			continue
		}
		if arg == "--" {
			inPositional = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			// Check if flag takes a separate argument value (e.g. -B, --bind, etc.)
			if !strings.Contains(arg, "=") && takesArg(arg) && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		// First non-flag argument begins positional args
		posArgs = append(posArgs, args[i:]...)
		break
	}
	return flags, posArgs
}

// splitImageAndCommand extracts apptainer flags, image, and command arguments from args.
func splitImageAndCommand(args []string) (flags []string, image string, cmdArgs []string) {
	flags, posArgs := splitFlagsAndPositional(args)
	if len(posArgs) > 0 {
		image = posArgs[0]
		if len(posArgs) > 1 {
			cmdArgs = posArgs[1:]
		}
	}
	return flags, image, cmdArgs
}

// takesArg returns true if the specified Apptainer flag expects a separate value argument.
func takesArg(flag string) bool {
	knownValFlags := map[string]bool{
		"-B": true, "--bind": true,
		"-b": true,
		"-c": true, "--cwd": true, "--pwd": true,
		"-H": true, "--home": true,
		"-W": true, "--workdir": true,
		"-S": true, "--scratch": true,
		"--dns":                true,
		"--network":            true,
		"--network-args":       true,
		"--security":           true,
		"--env-file":           true,
		"--env":                true,
		"--mount":              true,
		"--app":                true,
		"--no-mount":           true,
		"--netns-path":         true,
		"--hostname":           true,
		"--overlay":            true,
		"-o":                   true,
		"--fusemount":          true,
		"--memory":             true,
		"--memory-reservation": true,
		"--memory-swap":        true,
		"--pids-limit":         true,
		"--pem-path":           true,
	}
	return knownValFlags[flag]
}

func hasFakeroot(flags []string) bool {
	for _, f := range flags {
		if f == "--fakeroot" || f == "-f" {
			return true
		}
	}
	return false
}

func printUsage() {
	fmt.Print(`plaidtainer - Dedicated Apptainer execution wrapper with user-space networking

Usage:
  plaidtainer <command> [options...] [args...]

Supported Execution Commands:
  exec [options] <image> <command> [args...]       Execute a command inside a container
  instance start [options] <image> <name> [args..] Start an Apptainer instance (runs startscript)
  instance run [options] <image> <name> [args...]  Start an Apptainer instance (runs runscript)
  instance stop <name>                             Stop an instance and clean up Plaid network
  run [options] <image> [args...]                  Run a container image
  shell [options] <image>                          Open an interactive shell in a container

Plaid-Specific Options (can be passed to execution commands):
  --host-networking    Bypass Plaid user-space networking; use host network interfaces
  --ip <cidr>          Explicit container IP address (default: dynamic IPAM via host-local)
  --gateway <ip>       Gateway IP address (default: .1 of subnet)
  --dns <ip>           DNS nameserver IP address (default: .3 of subnet)
  --socket <path>      Path to plaidd UNIX socket (default: /run/plaid/plaidd.sock)
  --bind <paths>       Additional bind mounts for Apptainer (comma-separated)

All other Apptainer commands (build, pull, inspect, instance list, etc.) and flags are passed
transparently to the underlying apptainer binary.
`)
}
