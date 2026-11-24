package main

import (
	"embed"
)

//go:embed fleet_static/*
var fleetStaticFS embed.FS

//go:embed funauth_static/*
var funauthStaticFS embed.FS

//go:embed sales_static/*
var salesStaticFS embed.FS

//go:embed autosell_static/*
var autosellStaticFS embed.FS
