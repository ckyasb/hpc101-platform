package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "codojo: hpc101-platform CLI")
		fmt.Fprintln(os.Stderr, "usage: codojo <command> [args]")
		fmt.Fprintln(os.Stderr, "  register-key  Register an SSH public key")
		fmt.Fprintln(os.Stderr, "  up            Start a development environment")
		fmt.Fprintln(os.Stderr, "  ssh-info      Show SSH connection info")
		fmt.Fprintln(os.Stderr, "  release       Release your active environment")
		fmt.Fprintln(os.Stderr, "  problem       List problems")
		fmt.Fprintln(os.Stderr, "  score         Show your scores")
		fmt.Fprintln(os.Stderr, "  submit        Submit a solution for judging")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "register-key":
		fmt.Println("register-key: not yet implemented")
	case "up":
		fmt.Println("up: not yet implemented")
	case "ssh-info":
		fmt.Println("ssh-info: not yet implemented")
	case "release":
		fmt.Println("release: not yet implemented")
	case "problem":
		fmt.Println("problem: not yet implemented")
	case "score":
		fmt.Println("score: not yet implemented")
	case "submit":
		fmt.Println("submit: not yet implemented")
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
