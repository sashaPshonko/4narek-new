package main

import (
	"strings"
)

func isV9Down(action string) bool {
	return strings.Contains(action, "corridor_price_down")
}

func isV9Up(action string) bool {
	return strings.Contains(action, "corridor_price_up")
}
