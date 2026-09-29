package server

import "testing"

func TestSerialExitCodeRequiresMarker(t *testing.T) {
	code, found, output := serialExitCode("hello\r\n__EDGEKIT_CMD_123__0\r\n", "__EDGEKIT_CMD_123__")
	if !found || code != 0 || output != "hello" {
		t.Fatalf("exit 0 parse = %d, %v, %q", code, found, output)
	}
	code, found, output = serialExitCode("hello\r\n__EDGEKIT_CMD_123__7\r\n", "__EDGEKIT_CMD_123__")
	if !found || code != 7 || output != "hello" {
		t.Fatalf("exit 7 parse = %d, %v, %q", code, found, output)
	}
	if _, found, _ := serialExitCode("hello\r\n", "__EDGEKIT_CMD_123__"); found {
		t.Fatal("missing marker must not be interpreted as exit code 0")
	}
}
