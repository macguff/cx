package main

import (
	"fmt"
	"os"

	"github.com/macguff/cx/internal/cx"
)

func main() {
	app, err := cx.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cx: %v\n", err)
		os.Exit(1)
	}

	if err := app.Run(os.Args[1:]); err != nil {
		if exitErr, ok := err.(cx.ExitError); ok {
			if exitErr.Err != nil {
				fmt.Fprintf(os.Stderr, "cx: %v\n", exitErr.Err)
			}
			os.Exit(exitErr.Code)
		}
		fmt.Fprintf(os.Stderr, "cx: %v\n", err)
		os.Exit(1)
	}
}
