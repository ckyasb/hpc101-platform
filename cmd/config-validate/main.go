// config-validate: verify the CSOJ config overlay parses correctly.
// Imports the vendored CSOJ config package without modification.
package main

import (
	"fmt"
	"os"

	"github.com/ZJUSCT/CSOJ/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: config-validate <config.yaml>")
		os.Exit(2)
	}
	cfg, err := config.Load(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: config.Load returned error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("PASS: config.Load succeeded\n")
	fmt.Printf("  Listen: %s\n", cfg.Listen)
	fmt.Printf("  Admin.Enabled: %v\n", cfg.Admin.Enabled)
	fmt.Printf("  Storage.Database: %s\n", cfg.Storage.Database)
	fmt.Printf("  Clusters: %d cluster(s)\n", len(cfg.Cluster))
	for _, cl := range cfg.Cluster {
		fmt.Printf("    Cluster %q: %d node(s)\n", cl.Name, len(cl.Nodes))
		for _, n := range cl.Nodes {
			fmt.Printf("      Node %q: cpu=%d mem=%d docker_host=%s\n",
				n.Name, n.CPU, n.Memory, n.Docker.Host)
		}
	}
	fmt.Printf("  ContestsRoot: %s\n", cfg.ContestsRoot)
}
