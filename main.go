package main

import (
	"context"
	"fmt"
	"os"

	"github.com/iwahbe/pulumi-resend/provider"
)

func main() {
	prov, err := provider.New()
	if err == nil {
		err = prov.Run(context.Background(), provider.Name, provider.Version)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
