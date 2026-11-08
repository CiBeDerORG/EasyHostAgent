package dockerengine

import "testing"

func TestRPCAddress(t *testing.T) {
	address, err := rpcAddress([]string{"TZ=Asia/Shanghai", "EASYTIER_RPC_PORT=7116"})
	if err != nil || address != "127.0.0.1:7116" {
		t.Fatalf("expected custom RPC port, got %q: %v", address, err)
	}
	for _, env := range [][]string{
		nil,
		{"EASYTIER_RPC_PORT=0"},
		{"EASYTIER_RPC_PORT=65536"},
		{"EASYTIER_RPC_PORT=abc"},
		{"EASYTIER_RPC_PORT=7006", "EASYTIER_RPC_PORT=7007"},
	} {
		if _, err := rpcAddress(env); err == nil {
			t.Fatalf("invalid RPC address accepted: %v", env)
		}
	}
}
