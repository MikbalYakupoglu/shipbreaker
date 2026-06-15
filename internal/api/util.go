package api

import "strings"

func splitComma(s string) []string {
	return strings.Split(s, ",")
}
