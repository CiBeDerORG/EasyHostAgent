package easytier

import (
	"errors"
	"reflect"
	"testing"
)

func TestSelectorArgs(t *testing.T) {
	if _, err := selectorArgs(InstanceSelector{}, true); !errors.Is(err, ErrInvalidPortForward) {
		t.Fatal("mutation without explicit instance must be denied")
	}
	args, err := selectorArgs(InstanceSelector{InstanceName: "home-network"}, true)
	if err != nil || !reflect.DeepEqual(args, []string{"--instance-name", "home-network"}) {
		t.Fatalf("unexpected selector: %v %v", args, err)
	}
	args, err = selectorArgs(InstanceSelector{InstanceID: "123e4567-e89b-12d3-a456-426614174000"}, true)
	if err != nil || len(args) != 2 || args[0] != "--instance-id" {
		t.Fatalf("ID selector: %v %v", args, err)
	}
	for _, s := range []InstanceSelector{
		{InstanceName: "-bad"},
		{InstanceName: "has\nnewline"},
		{InstanceID: "bad"},
		{InstanceID: "123e4567-e89b-12d3-a456-426614174000", InstanceName: "x"},
	} {
		if _, err := selectorArgs(s, false); err == nil {
			t.Fatalf("accepted invalid selector %#v", s)
		}
	}
}

func TestPortForwardArgs(t *testing.T) {
	req := PortForwardRequest{
		ResourceID: "compose:easytier", InstanceName: "home",
		Protocol: "tcp", Listen: "127.0.0.1:8080", Destination: "10.144.0.20:80",
	}
	args, err := portForwardArgs("add", req)
	want := []string{"--instance-name", "home", "port-forward", "add", "tcp", "127.0.0.1:8080", "10.144.0.20:80"}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("invalid CLI argv: %v %v", args, err)
	}
	req.Listen = "0.0.0.0:8080"
	if _, err := portForwardArgs("add", req); err == nil {
		t.Fatal("public listener without confirmation accepted")
	}
	req.AllowPublicBind = true
	if _, err := portForwardArgs("add", req); err != nil {
		t.Fatalf("explicit public bind rejected: %v", err)
	}
	for _, change := range []func(*PortForwardRequest){
		func(r *PortForwardRequest) { r.Protocol = "sctp" },
		func(r *PortForwardRequest) { r.Destination = "127.0.0.1:0" },
		func(r *PortForwardRequest) { r.Destination = "8.8.8.8:70000" },
		func(r *PortForwardRequest) { r.Destination = "example.org:80" },
		func(r *PortForwardRequest) { r.Destination = "0.0.0.0:80" },
		func(r *PortForwardRequest) { r.Listen = "0.0.0.0:8080;rm -rf /" },
		func(r *PortForwardRequest) { r.InstanceID = "not-uuid" },
		func(r *PortForwardRequest) { r.ResourceID = "compose:other" },
	} {
		invalid := PortForwardRequest{
			ResourceID: "compose:easytier", InstanceName: "home", Protocol: "tcp",
			Listen: "127.0.0.1:8080", Destination: "10.144.0.20:80",
		}
		change(&invalid)
		if _, err := portForwardArgs("add", invalid); err == nil {
			t.Fatalf("accepted %#v", invalid)
		}
	}
	req = PortForwardRequest{ResourceID: "compose:easytier", InstanceName: "home", Protocol: "udp", Listen: "127.0.0.1:7777"}
	args, err = portForwardArgs("remove", req)
	if err != nil || !reflect.DeepEqual(args, []string{"--instance-name", "home", "port-forward", "remove", "udp", "127.0.0.1:7777"}) {
		t.Fatalf("remove args: %v %v", args, err)
	}
}
