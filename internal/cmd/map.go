package cmd

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/spf13/cobra"
)

var mapCmd = &cobra.Command{
	Use:   "map <service-port> <listen-port>",
	Short: "Make a service available on a different port",
	Long:  "Proxies traffic from listen-port to service-port.\nExample: sonar map 6873 3002 — access the service on 6873 via http://localhost:3002",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		target, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid service port: %s", args[0])
		}
		listen, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid listen port: %s", args[1])
		}

		listenAddr := fmt.Sprintf(":%d", listen)
		targetAddr := fmt.Sprintf("localhost:%d", target)

		listener, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", listen, err)
		}
		defer listener.Close()

		fmt.Printf("%s %s %s\n",
			display.BoldCyan(fmt.Sprintf("http://localhost:%d", listen)),
			display.Dim("->"),
			display.BoldCyan(fmt.Sprintf("http://localhost:%d", target)))
		fmt.Println(display.Dim("Press Ctrl+C to stop"))

		// Handle Ctrl+C
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		go func() {
			<-sigCh
			fmt.Printf("\n%s Stopped port mapping\n", display.Dim("[sonar]"))
			listener.Close()
			os.Exit(0)
		}()

		for {
			conn, err := listener.Accept()
			if err != nil {
				return nil // listener closed
			}
			go proxy(conn, targetAddr, listen, target)
		}
	},
}

func proxy(src net.Conn, targetAddr string, listenPort, targetPort int) {
	defer src.Close()

	dst, err := net.Dial("tcp", targetAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", display.Red("error:"), err)
		return
	}
	defer dst.Close()

	fmt.Printf("%s %s %s %s\n",
		display.Dim("[conn]"),
		display.Green(fmt.Sprintf(":%d", listenPort)),
		display.Dim("->"),
		display.Green(fmt.Sprintf(":%d", targetPort)))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(dst, src)
	}()
	go func() {
		defer wg.Done()
		io.Copy(src, dst)
	}()
	wg.Wait()
}

func init() {
	rootCmd.AddCommand(mapCmd)
}
