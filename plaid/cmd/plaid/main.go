package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"plaid/pkg/api"
	"plaid/pkg/tap"
)

// CNIConfig represents the configuration passed via Stdin.
type CNIConfig struct {
	CNIVersion string          `json:"cniVersion"`
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	SocketPath string          `json:"socketPath,omitempty"`
	MTU        int             `json:"mtu,omitempty"`
	IP         string          `json:"ip,omitempty"`
	Gateway    string          `json:"gateway,omitempty"`
	IPAM       json.RawMessage `json:"ipam,omitempty"`
}

// IPAMConfig extracts the type field from the ipam block.
type IPAMConfig struct {
	Type string `json:"type"`
}

// IPAMResult represents a partial CNI IPAM result.
type IPAMResult struct {
	CNIVersion string `json:"cniVersion"`
	IPs        []struct {
		Version string `json:"version"`
		Address string `json:"address"`
		Gateway string `json:"gateway"`
	} `json:"ips"`
	Routes []struct {
		Dst string `json:"dst"`
		GW  string `json:"gw"`
	} `json:"routes"`
	DNS struct {
		Nameservers []string `json:"nameservers"`
	} `json:"dns"`
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			cmdInit(os.Args[2:])
			return
		case "exec":
			cmdExec(os.Args[2:])
			return
		case "version", "--version", "-v":
			cmdVersion()
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}

	cmd := os.Getenv("CNI_COMMAND")
	switch cmd {
	case "VERSION":
		cmdVersion()
	case "ADD":
		cmdAdd()
	case "DEL":
		cmdDel()
	case "CHECK":
		cmdCheck()
	default:
		if cmd != "" {
			fmt.Fprintf(os.Stderr, "Unknown CNI_COMMAND: %s\n", cmd)
		} else {
			printUsage()
		}
		os.Exit(1)
	}
}

func cmdVersion() {
	result := map[string]interface{}{
		"cniVersion":        "0.4.0",
		"supportedVersions": []string{"0.3.0", "0.3.1", "0.4.0", "1.0.0"},
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

func cmdAdd() {
	stdinData, err := io.ReadAll(os.Stdin)
	if err != nil {
		cniError(1, "Failed to read stdin", err.Error())
	}

	var conf CNIConfig
	if err := json.Unmarshal(stdinData, &conf); err != nil {
		cniError(1, "Failed to parse CNI config JSON", err.Error())
	}

	if conf.SocketPath == "" {
		conf.SocketPath = "/run/plaid/plaidd.sock"
	}
	if conf.MTU <= 0 {
		conf.MTU = 1500
	}

	containerID := os.Getenv("CNI_CONTAINERID")
	netnsPath := os.Getenv("CNI_NETNS")
	ifName := os.Getenv("CNI_IFNAME")
	if ifName == "" {
		ifName = "eth0"
	}

	cniArgs := parseCNIArgs(os.Getenv("CNI_ARGS"))
	podName := cniArgs["K8S_POD_NAME"]
	if podName == "" {
		podName = containerID
	}
	podNamespace := cniArgs["K8S_POD_NAMESPACE"]

	var podIP net.IP
	var podMask net.IPMask
	var gwIP net.IP

	// 1. Run IPAM plugin if present (fallback to /run/plaid/ipam.json if omitted)
	if len(conf.IPAM) == 0 {
		socketDir := filepath.Dir(conf.SocketPath)
		ipamPath := filepath.Join(socketDir, "ipam.json")
		if data, err := os.ReadFile(ipamPath); err == nil {
			var ipamWrapper struct {
				IPAM json.RawMessage `json:"ipam"`
			}
			if err := json.Unmarshal(data, &ipamWrapper); err == nil && len(ipamWrapper.IPAM) > 0 {
				conf.IPAM = ipamWrapper.IPAM
				var rawMap map[string]interface{}
				if err := json.Unmarshal(stdinData, &rawMap); err == nil {
					var rawIPAM interface{}
					_ = json.Unmarshal(ipamWrapper.IPAM, &rawIPAM)
					rawMap["ipam"] = rawIPAM
					stdinData, _ = json.Marshal(rawMap)
				}
			}
		}
	}

	var ipamResult *IPAMResult
	if len(conf.IPAM) > 0 {
		var ipamConf IPAMConfig
		if err := json.Unmarshal(conf.IPAM, &ipamConf); err == nil && ipamConf.Type != "" {
			cniData := adjustHostLocalIPAM(stdinData)
			res, err := execIPAM("ADD", ipamConf.Type, cniData)
			if err != nil {
				cniError(2, "IPAM allocation failed", err.Error())
			}
			ipamResult = res
			if len(ipamResult.IPs) > 0 {
				if ip, ipNet, err := net.ParseCIDR(ipamResult.IPs[0].Address); err == nil {
					podIP = ip
					podMask = ipNet.Mask
				}
				if ipamResult.IPs[0].Gateway != "" {
					gwIP = net.ParseIP(ipamResult.IPs[0].Gateway)
				}
			}
		}
	}

	// Support inline IP or CNI_ARGS if IPAM was not used
	if podIP == nil {
		ipCandidate := conf.IP
		if ipCandidate == "" {
			ipCandidate = cniArgs["IP"]
		}
		if ipCandidate == "" {
			ipCandidate = cniArgs["ip"]
		}

		if ipCandidate != "" {
			if ip, ipNet, err := net.ParseCIDR(ipCandidate); err == nil {
				podIP = ip
				podMask = ipNet.Mask
			} else if ip := net.ParseIP(ipCandidate); ip != nil {
				podIP = ip
				podMask = net.CIDRMask(24, 32)
			}
		}

		gwCandidate := conf.Gateway
		if gwCandidate == "" {
			gwCandidate = cniArgs["GATEWAY"]
		}
		if gwCandidate == "" {
			gwCandidate = cniArgs["gateway"]
		}

		if gwCandidate != "" {
			gwIP = net.ParseIP(gwCandidate)
		} else if podIP != nil && podMask != nil {
			// Default gateway to .1 in the subnet
			gwIP = make(net.IP, len(podIP.To4()))
			copy(gwIP, podIP.To4().Mask(podMask))
			gwIP[3] = 1
		}
	}

	// Generate MAC address (locally administered unicast: 02:xx:xx:xx:xx:xx)
	podMAC := generateMAC()

	// 2. Open TAP interface inside the container netns
	tapFile, err := createTapInNetNS(netnsPath, tap.TapConfig{
		Name:    ifName,
		IP:      podIP,
		Mask:    podMask,
		Gateway: gwIP,
		MAC:     podMAC,
		MTU:     conf.MTU,
	})
	if err != nil {
		cniError(3, "Failed to create TAP device in netns", err.Error())
	}
	defer tapFile.Close()

	// 3. Connect to plaidd and register endpoint with TAP fd
	client := api.NewClient(conf.SocketPath)
	podIPStr := ""
	if podIP != nil {
		podIPStr = podIP.String()
	}
	req := &api.Request{
		PodID:        containerID,
		PodName:      podName,
		PodNamespace: podNamespace,
		ContainerID:  containerID,
		IP:           podIPStr,
		MAC:          podMAC.String(),
		NetnsPath:    netnsPath,
	}

	if err := client.AddEndpoint(req, int(tapFile.Fd())); err != nil {
		cniError(4, "Failed to register endpoint with plaidd", err.Error())
	}

	// 4. Output CNI success result
	cniVersion := conf.CNIVersion
	if cniVersion == "" {
		cniVersion = "0.4.0"
	}

	outResult := map[string]interface{}{
		"cniVersion": cniVersion,
		"interfaces": []map[string]interface{}{
			{
				"name":    ifName,
				"mac":     podMAC.String(),
				"sandbox": netnsPath,
			},
		},
	}

	if podIP != nil {
		var maskBits int
		if podMask != nil {
			ones, _ := podMask.Size()
			maskBits = ones
		}
		gwIPStr := ""
		if gwIP != nil {
			gwIPStr = gwIP.String()
		}
		outResult["ips"] = []map[string]interface{}{
			{
				"version":   "4",
				"interface": 0,
				"address":   fmt.Sprintf("%s/%d", podIP, maskBits),
				"gateway":   gwIPStr,
			},
		}
		if gwIP != nil {
			outResult["routes"] = []map[string]interface{}{
				{
					"dst": "0.0.0.0/0",
					"gw":  gwIPStr,
				},
			}
		}

		nameservers := []string{"1.1.1.1", "8.8.8.8"}
		if gwIP != nil {
			sDNS := make(net.IP, len(gwIP.To4()))
			copy(sDNS, gwIP.To4())
			sDNS[3] = 3
			nameservers = append([]string{sDNS.String()}, nameservers...)
		}
		outResult["dns"] = map[string]interface{}{
			"nameservers": nameservers,
		}
	}

	_ = json.NewEncoder(os.Stdout).Encode(outResult)
}

func cmdDel() {
	stdinData, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(0)
	}

	var conf CNIConfig
	_ = json.Unmarshal(stdinData, &conf)
	if conf.SocketPath == "" {
		conf.SocketPath = "/run/plaid/plaidd.sock"
	}

	containerID := os.Getenv("CNI_CONTAINERID")
	cniArgs := parseCNIArgs(os.Getenv("CNI_ARGS"))
	podName := cniArgs["K8S_POD_NAME"]
	if podName == "" {
		podName = containerID
	}

	// 1. Unregister endpoint from plaidd
	client := api.NewClient(conf.SocketPath)
	_ = client.RemoveEndpoint(podName, containerID)

	// 2. Release IPAM if configured (fallback to /run/plaid/ipam.json if omitted)
	if len(conf.IPAM) == 0 {
		socketDir := filepath.Dir(conf.SocketPath)
		ipamPath := filepath.Join(socketDir, "ipam.json")
		if data, err := os.ReadFile(ipamPath); err == nil {
			var ipamWrapper struct {
				IPAM json.RawMessage `json:"ipam"`
			}
			if err := json.Unmarshal(data, &ipamWrapper); err == nil && len(ipamWrapper.IPAM) > 0 {
				conf.IPAM = ipamWrapper.IPAM
				var rawMap map[string]interface{}
				if err := json.Unmarshal(stdinData, &rawMap); err == nil {
					var rawIPAM interface{}
					_ = json.Unmarshal(ipamWrapper.IPAM, &rawIPAM)
					rawMap["ipam"] = rawIPAM
					stdinData, _ = json.Marshal(rawMap)
				}
			}
		}
	}

	if len(conf.IPAM) > 0 {
		var ipamConf IPAMConfig
		if err := json.Unmarshal(conf.IPAM, &ipamConf); err == nil && ipamConf.Type != "" {
			cniData := adjustHostLocalIPAM(stdinData)
			_, _ = execIPAM("DEL", ipamConf.Type, cniData)
		}
	}

	os.Exit(0)
}

func cmdCheck() {
	os.Exit(0)
}

func execIPAM(action, ipamType string, stdin []byte) (*IPAMResult, error) {
	cniPath := os.Getenv("CNI_PATH")
	if cniPath == "" {
		cniPath = "/opt/cni/bin"
	}

	var binPath string
	for _, dir := range filepath.SplitList(cniPath) {
		p := filepath.Join(dir, ipamType)
		if _, err := os.Stat(p); err == nil {
			binPath = p
			break
		}
	}

	if binPath == "" {
		return nil, fmt.Errorf("IPAM plugin %q not found in CNI_PATH %q", ipamType, cniPath)
	}

	cmd := exec.Command(binPath)
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CNI_ARGS=") {
			rawArgs := strings.TrimPrefix(e, "CNI_ARGS=")
			var keptArgs []string
			for _, pair := range strings.Split(rawArgs, ";") {
				pair = strings.TrimSpace(pair)
				if strings.HasPrefix(strings.ToUpper(pair), "IP=") {
					keptArgs = append(keptArgs, pair)
				}
			}
			if len(keptArgs) > 0 {
				env = append(env, "CNI_ARGS="+strings.Join(keptArgs, ";"))
			}
		} else {
			env = append(env, e)
		}
	}
	cmd.Env = env
	cmd.Env = append(cmd.Env, "CNI_COMMAND="+action, "CNI_PATH="+cniPath)
	cmd.Stdin = bytes.NewReader(stdin)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("IPAM plugin %s failed (%v): %s", ipamType, err, string(out))
	}

	if action == "DEL" {
		return nil, nil
	}

	var res IPAMResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("failed to parse IPAM output: %w", err)
	}

	return &res, nil
}

func parseCNIArgs(argsStr string) map[string]string {
	res := make(map[string]string)
	if argsStr == "" {
		return res
	}
	parts := strings.Split(argsStr, ";")
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			res[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return res
}

func generateMAC() net.HardwareAddr {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	// Set local bit, clear multicast bit -> 02:xx:xx:xx:xx:xx
	buf[0] = (buf[0] | 0x02) & 0xfe
	return net.HardwareAddr(buf)
}

func cniError(code int, msg, details string) {
	errResp := map[string]interface{}{
		"cniVersion": "0.4.0",
		"code":       code,
		"msg":        msg,
		"details":    details,
	}
	_ = json.NewEncoder(os.Stdout).Encode(errResp)
	os.Exit(code)
}

func cmdInit(args []string) {
	runInit(args, true)
}

func cmdExec(args []string) {
	var flagsPart []string
	var cmdPart []string

	splitIdx := -1
	for i, arg := range args {
		if arg == "--" {
			splitIdx = i
			break
		}
	}

	if splitIdx >= 0 {
		flagsPart = args[:splitIdx]
		cmdPart = args[splitIdx+1:]
	} else {
		// Find first non-flag argument as command
		cmdIdx := -1
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if strings.HasPrefix(arg, "-") {
				if strings.Contains(arg, "=") {
					continue
				}
				// Skip next arg if this flag takes a value
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
				}
				continue
			}
			cmdIdx = i
			break
		}
		if cmdIdx >= 0 {
			flagsPart = args[:cmdIdx]
			cmdPart = args[cmdIdx:]
		} else {
			flagsPart = args
		}
	}

	if len(cmdPart) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: plaid exec [flags] -- <command> [args...]")
		os.Exit(1)
	}

	runInit(flagsPart, false)

	binPath, err := exec.LookPath(cmdPart[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: command not found: %s\n", cmdPart[0])
		os.Exit(127)
	}

	if err := syscall.Exec(binPath, cmdPart, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "Error executing %s: %v\n", binPath, err)
		os.Exit(1)
	}
}

func runInit(args []string, verbose bool) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	var ipStr, gwStr, socketPath, ifName, containerID string
	var mtu int

	fs.StringVar(&ipStr, "ip", os.Getenv("PLAID_IP"), "Container IP address (e.g. 10.244.1.10/24)")
	fs.StringVar(&gwStr, "gateway", os.Getenv("PLAID_GATEWAY_IP"), "Gateway IP (defaults to .1 of subnet)")
	fs.StringVar(&socketPath, "socket", os.Getenv("PLAID_SOCKET_PATH"), "Path to plaidd UNIX domain socket")
	fs.StringVar(&ifName, "ifname", os.Getenv("PLAID_IFNAME"), "Interface name to create (default: eth0)")
	fs.IntVar(&mtu, "mtu", 1500, "MTU for the interface")
	fs.StringVar(&containerID, "container-id", os.Getenv("PLAID_CONTAINER_ID"), "Container identifier")

	_ = fs.Parse(args)

	if socketPath == "" {
		socketPath = "/run/plaid/plaidd.sock"
	}
	if ifName == "" {
		ifName = "eth0"
	}
	if containerID == "" {
		hostname, _ := os.Hostname()
		if hostname != "" {
			containerID = hostname
		} else {
			containerID = fmt.Sprintf("plaid-%d", time.Now().UnixNano())
		}
	}
	if mtu <= 0 {
		mtu = 1500
	}

	if ipStr == "" {
		fmt.Fprintf(os.Stderr, "Error: missing required IP address (--ip or PLAID_IP)\n")
		os.Exit(1)
	}

	var podIP net.IP
	var podMask net.IPMask
	if ip, ipNet, err := net.ParseCIDR(ipStr); err == nil {
		podIP = ip
		podMask = ipNet.Mask
	} else if ip := net.ParseIP(ipStr); ip != nil {
		podIP = ip
		podMask = net.CIDRMask(24, 32)
	} else {
		fmt.Fprintf(os.Stderr, "Error: invalid IP address: %s\n", ipStr)
		os.Exit(1)
	}

	var gwIP net.IP
	if gwStr != "" {
		gwIP = net.ParseIP(gwStr)
		if gwIP == nil {
			fmt.Fprintf(os.Stderr, "Error: invalid gateway IP: %s\n", gwStr)
			os.Exit(1)
		}
	} else {
		// Try to query plaidd for status / gateway, or default to .1 of subnet
		client := api.NewClient(socketPath)
		if st, err := client.GetStatus(); err == nil && st.GatewayIP != "" {
			gwIP = net.ParseIP(st.GatewayIP)
		}
		if gwIP == nil {
			gwIP = make(net.IP, len(podIP.To4()))
			copy(gwIP, podIP.To4().Mask(podMask))
			gwIP[3] = 1
		}
	}

	podMAC := generateMAC()

	tapFile, err := createTapInNetNS("", tap.TapConfig{
		Name:    ifName,
		IP:      podIP,
		Mask:    podMask,
		Gateway: gwIP,
		MAC:     podMAC,
		MTU:     mtu,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating TAP device %s: %v\n", ifName, err)
		os.Exit(1)
	}
	defer tapFile.Close()

	client := api.NewClient(socketPath)
	req := &api.Request{
		PodID:       containerID,
		PodName:     containerID,
		ContainerID: containerID,
		IP:          podIP.String(),
		MAC:         podMAC.String(),
		NetnsPath:   "",
	}

	if err := client.AddEndpoint(req, int(tapFile.Fd())); err != nil {
		fmt.Fprintf(os.Stderr, "Error registering endpoint with plaidd: %v\n", err)
		os.Exit(1)
	}

	if verbose {
		fmt.Printf("Plaid network initialized: iface=%s ip=%s/%d gw=%s mac=%s\n",
			ifName, podIP, maskBits(podMask), gwIP, podMAC)
	}
}

func maskBits(mask net.IPMask) int {
	if mask == nil {
		return 24
	}
	ones, _ := mask.Size()
	return ones
}

func adjustHostLocalIPAM(data []byte) []byte {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return data
	}
	ipamRaw, ok := raw["ipam"].(map[string]interface{})
	if !ok {
		return data
	}
	if ipamRaw["type"] != "host-local" {
		return data
	}
	if _, ok := ipamRaw["rangeStart"]; ok {
		return data
	}
	if _, ok := ipamRaw["ranges"]; ok {
		return data
	}
	subnetStr, ok := ipamRaw["subnet"].(string)
	if !ok || subnetStr == "" {
		return data
	}
	_, ipNet, err := net.ParseCIDR(subnetStr)
	if err != nil || ipNet.IP.To4() == nil {
		return data
	}
	ip4 := ipNet.IP.To4()
	ipamRaw["rangeStart"] = net.IPv4(ip4[0], ip4[1], ip4[2], 4).String()
	ipamRaw["rangeEnd"] = net.IPv4(ip4[0], ip4[1], ip4[2], 254).String()

	out, err := json.Marshal(raw)
	if err != nil {
		return data
	}
	return out
}

func printUsage() {
	fmt.Print(`Plaid - User-Space CNI Plugin & Network Initializer

Usage:
  plaid init [flags]                   Initialize TAP interface inside container network namespace
  plaid exec [flags] -- <cmd> [args]   Initialize network and execute command inside container
  plaid version                        Print CNI plugin version information

Environment Variables (for init & exec):
  PLAID_IP                Container IP in CIDR format (required, e.g. 10.244.1.10/24)
  PLAID_GATEWAY_IP        Gateway IP (default: .1 of subnet)
  PLAID_SOCKET_PATH       Path to plaidd UNIX socket (default: /run/plaid/plaidd.sock)
  PLAID_IFNAME            Interface name (default: eth0)
  PLAID_MTU               Interface MTU (default: 1500)
  PLAID_CONTAINER_ID      Container identifier (default: hostname)

When called by container runtimes via CNI, the standard CNI environment variables
(CNI_COMMAND, CNI_NETNS, CNI_IFNAME, CNI_PATH, CNI_ARGS) are evaluated.
`)
}
