//go:build tools
// +build tools

package tools

import (
	_ "github.com/golangci/golangci-lint/cmd/golangci-lint"
	_ "k8s.io/code-generator"
	_ "mvdan.cc/gofumpt"
)
