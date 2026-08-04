package main

import "embed"

//go:embed sales_static/*
var salesStaticFS embed.FS
