// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tomato is the OpenTofu/Terraform provider plugin entrypoint for
// Tomato-firmware routers (FreshTomato / AdvancedTomato), managing NVRAM over
// SSH.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/JamesonRGrieve/tofu-tomato/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/jamesonrgrieve/tomato",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
