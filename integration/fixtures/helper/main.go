// Command dls-helper is the payload of the integration build fixture. With no
// arguments it prints the marker file copied into the image by the Dockerfile;
// with "serve <port>" it runs a tiny HTTP server so wait.ForHTTP can be
// exercised against an image that needs no registry access.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "serve" {
		if err := serve(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "dls-helper:", err)
			os.Exit(1)
		}
		return
	}

	content, err := os.ReadFile("/dls-marker.txt")
	if err != nil {
		fmt.Fprintln(os.Stderr, "dls-helper:", err)
		os.Exit(1)
	}
	fmt.Print(string(content))
}

func serve(port string) error {
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 1 || parsed > 65535 {
		return fmt.Errorf("invalid port %q", port)
	}
	address := net.JoinHostPort("0.0.0.0", port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	fmt.Println("dls-helper serving on " + address)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("dls-helper-ready"))
	})
	return http.Serve(listener, mux)
}
