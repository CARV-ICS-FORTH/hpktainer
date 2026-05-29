package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hpk/internal/netutil"
	"hpk/pkg/version"

	"github.com/songgao/water"
	"github.com/vishvananda/netlink"
)

func main() {
	mode := flag.String("mode", "", "Mode: 'server' or 'client'")
	socketPath := flag.String("socket", "", "Path to UNIX socket")
	tapName := flag.String("tap", "", "Name of TAP interface")
	createTap := flag.Bool("create-tap", false, "Whether to create the TAP interface (if false, opens existing)")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	mtuFlag := flag.Int("mtu", 1450, "MTU of the TAP interface (defaults to 1400 to match Flannel/Bridge overlay)")

	flag.Parse()

	if *versionFlag {
		fmt.Printf("hpk-net-daemon version: %s (built: %s)\n", version.Version, version.BuildTime)
		os.Exit(0)
	}

	if *mode == "" || *socketPath == "" || *tapName == "" {
		flag.Usage()
		os.Exit(1)
	}

	// Setup TAP interface
	config := water.Config{
		DeviceType: water.TAP,
	}
	config.Name = *tapName

	var tap *water.Interface
	var err error

	if *createTap {
		tap, err = water.New(config)
	} else {
		// New also opens existing if it exists, roughly.
		// However, for water/tun, New() usually creates.
		// If we want to open existing persistent device created by 'ip tuntap add',
		// water.New SHOULD work if the name matches and it persists.
		// But in some environments, we might need specific flags.
		// For now, we use water.New which on Linux effectively opens the /dev/net/tun and registers the name.
		// If it's already registered by another process (persistent), it might fail if we try to create?
		// Actually, if we want to attach to an EXISTING persistent TAP, we just use the same name.
		tap, err = water.New(config)
	}
	if err != nil {
		log.Fatalf("Failed to open/create TAP interface %s: %v", *tapName, err)
	}
	defer tap.Close()

	log.Printf("Opened TAP interface: %s", tap.Name())

	// Ensure interface is UP to avoid I/O errors on write
	if link, err := netlink.LinkByName(tap.Name()); err == nil {    
        if err := netlink.LinkSetMTU(link, *mtuFlag); err != nil {
            log.Printf("Warning: failed to set MTU %d on %s: %v", *mtuFlag, tap.Name(), err)
        } else {
            log.Printf("Set MTU on %s to %d", tap.Name(), *mtuFlag)
        }

        if err := netlink.LinkSetUp(link); err != nil {
            log.Printf("Warning: failed to set link up: %v", err)
        }
    } else {
        log.Printf("Warning: failed to find link %s: %v", tap.Name(), err)
    }

	var conn net.Conn

	if *mode == "server" {
		// Cleanup stale socket
		os.Remove(*socketPath)

		listener, err := net.Listen("unix", *socketPath)
		if err != nil {
			log.Fatalf("Failed to listen on socket %s: %v", *socketPath, err)
		}
		defer listener.Close()

		// Ensure everyone can write to it (so container user can connect? actually container is root usually?)
		// But if we bind mount, permissions matter.
		if err := os.Chmod(*socketPath, 0777); err != nil {
			log.Printf("Warning: failed to chmod socket: %v", err)
		}

		log.Printf("Listening on %s", *socketPath)

		// Accept loop handling one connection at a time or just one.
		// Given the 1:1 mapping, we just accept one.
		conn, err = listener.Accept()
		if err != nil {
			log.Fatalf("Failed to accept connection: %v", err)
		}
		log.Printf("Accepted connection")

	} else if *mode == "client" {
		// Retries for client connection (wait for daemon on host to start/socket to appear)
		for i := 0; i < 10; i++ {
			conn, err = net.Dial("unix", *socketPath)
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil {
			log.Fatalf("Failed to connect to socket %s: %v", *socketPath, err)
		}
		log.Printf("Connected to %s", *socketPath)
	} else {
		log.Fatalf("Invalid mode: %s", *mode)
	}
	defer conn.Close()

	// Start forwarding
	errChan := make(chan error, 2)

	go func() {
		errChan <- netutil.CopyFromTapToSocket(tap, conn)
	}()

	go func() {
		errChan <- netutil.CopyFromSocketToTap(conn, tap)
	}()

	// Wait for error or interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errChan:
		log.Fatalf("Copy error: %v", err)
	case <-sigChan:
		log.Println("Received signal, exiting")
	}
}
