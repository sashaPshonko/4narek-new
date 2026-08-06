package main

import "embed"

//go:embed fleet_static/*
var fleetStaticFS embed.FS
