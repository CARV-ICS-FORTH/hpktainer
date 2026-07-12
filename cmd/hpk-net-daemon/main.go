package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
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
	mtuFlag := flag.Int("mtu", 1500, "MTU of the TAP interface")

	flag.Parse()
	_ = createTap

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

	// Opens or creates the TAP interface using songgao/water.
	// (New opens existing if it already exists)
	tap, err := water.New(config)
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

	// Context for graceful shutdown on signals
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup

	if *mode == "server" {
		// Cleanup stale socket
		os.Remove(*socketPath)

		listener, err := net.Listen("unix", *socketPath)
		if err != nil {
			log.Fatalf("Failed to listen on socket %s: %v", *socketPath, err)
		}
		defer listener.Close()

		// Restrict socket permission to owner (root) only
		if err := os.Chmod(*socketPath, 0600); err != nil {
			log.Printf("Warning: failed to chmod socket: %v", err)
		}

		log.Printf("Listening on %s", *socketPath)

		// Close listener and tap on context cancellation to wake up Accept and Copy
		go func() {
			<-ctx.Done()
			log.Println("Received signal, stopping listener and tap...")
			listener.Close()
			tap.Close()
		}()

		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					// Clean exit
					log.Println("Listener closed, exiting daemon")
					return
				default:
				}
				log.Printf("Accept error: %v", err)
				time.Sleep(1 * time.Second)
				continue
			}

			log.Printf("Accepted connection")

			// Handle the connection session
			handleSession(ctx, &wg, tap, conn)
			log.Printf("Connection closed, waiting for next connection...")
		}
	} else if *mode == "client" {
		var conn net.Conn
		// Retries for client connection (wait for daemon on host to start/socket to appear)
		for i := 0; i < 10; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
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

		// Close conn and tap on context cancellation to wake up Copy
		go func() {
			<-ctx.Done()
			log.Println("Received signal, stopping client connection and tap...")
			conn.Close()
			tap.Close()
		}()

		handleSession(ctx, &wg, tap, conn)
	} else {
		log.Fatalf("Invalid mode: %s", *mode)
	}
}

func handleSession(ctx context.Context, wg *sync.WaitGroup, tap *water.Interface, conn net.Conn) {
	defer conn.Close()

	// Ensure we close the connection if context is cancelled during the session
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	go func() {
		<-sessionCtx.Done()
		conn.Close()
	}()

	errChan := make(chan error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		err := netutil.CopyFromTapToSocket(tap, conn)
		select {
		case errChan <- err:
		default:
		}
	}()

	go func() {
		defer wg.Done()
		err := netutil.CopyFromSocketToTap(conn, tap)
		select {
		case errChan <- err:
		default:
		}
	}()

	// Wait for copy error (e.g. client disconnect/socket EOF)
	select {
	case <-ctx.Done():
		log.Println("Session ending due to daemon shutdown signal")
	case err := <-errChan:
		log.Printf("Connection session ended: %v", err)
	}

	// Wait for goroutines to drain
	wg.Wait()
}
