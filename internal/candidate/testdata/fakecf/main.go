// Command fakecf stands in for the clawflow binary in lifecycle tests. It
// listens only on the candidate's loopback port and refuses to start without a
// candidate identity.
package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	args := strings.Join(os.Args[1:], " ")
	switch {
	case args == "db migrate":
		fmt.Println("applied")
	case strings.HasPrefix(args, "worker token create"):
		fmt.Println("token-for-" + os.Args[len(os.Args)-1])
		fmt.Fprintln(os.Stderr, "SHA-256 fingerprint: "+strings.Repeat("AB", 32))
	case args == "controller":
		if os.Getenv("CLAWFLOW_CANDIDATE_ID") == "" {
			fmt.Fprintln(os.Stderr, "refusing to run outside a candidate")
			os.Exit(2)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("CLAWFLOW_CANDIDATE_PORT"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer ln.Close()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		wait()
	case strings.HasPrefix(args, "worker "):
		wait()
	default:
		os.Exit(3)
	}
}

func wait() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-ch:
	case <-time.After(2 * time.Minute):
	}
}
