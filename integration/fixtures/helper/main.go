// Command dls-helper is the payload of the integration build fixture: it
// prints the marker file copied into the image by the Dockerfile.
package main

import (
	"fmt"
	"os"
)

func main() {
	content, err := os.ReadFile("/dls-marker.txt")
	if err != nil {
		fmt.Fprintln(os.Stderr, "dls-helper:", err)
		os.Exit(1)
	}
	fmt.Print(string(content))
}
