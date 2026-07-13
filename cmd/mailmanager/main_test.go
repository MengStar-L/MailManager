package main

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"mailmanager/internal/version"
)

func TestVersionJSON(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	err = run([]string{"version", "--json"})
	_ = write.Close()
	os.Stdout = original
	if err != nil {
		t.Fatal(err)
	}
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	var result map[string]string
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode version output %q: %v", output, err)
	}
	if result["version"] != version.Version || result["commit"] != version.Commit || result["build_time"] != version.BuildTime {
		t.Fatalf("unexpected version output: %+v", result)
	}
}
