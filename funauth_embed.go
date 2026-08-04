package main

import "embed"

//go:embed funauth_static/*
var funauthStaticFS embed.FS
