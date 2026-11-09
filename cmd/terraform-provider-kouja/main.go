package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/voidstarit/kouja/internal/terraformprovider"
)

var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "run the provider in debug mode")
	flag.Parse()
	err := providerserver.Serve(context.Background(), terraformprovider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/voidstarit/kouja",
		Debug:   *debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
