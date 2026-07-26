package main

import (
	"encoding/hex"
	"fmt"
	"os"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
)

func main() {
	// Encode MCP result with empty execId
	resultBytes, err := cursorproto.EncodeExecMcpResult(1, "", `{"test": "data"}`, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "EncodeExecMcpResult: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Result protobuf hex: %s\n", hex.EncodeToString(resultBytes))
	fmt.Printf("Result length: %d bytes\n", len(resultBytes))

	// Write to file for analysis
	if err := os.WriteFile("mcp_result.bin", resultBytes, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "WriteFile: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Wrote mcp_result.bin")
}
