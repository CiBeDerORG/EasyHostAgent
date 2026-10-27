package httpapi

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
)
func TestHTTPSIdentityAndHealth(t *testing.T){
	state,err:=identity.LoadOrCreate(t.TempDir())
	if err!=nil {t.Fatal(err)}
	server:=httptest.NewUnstartedServer(NewHandler(state,"fnos","dev"))
	server.TLS=&tls.Config{MinVersion:tls.VersionTLS13,Certificates:[]tls.Certificate{state.Certificate}}
	server.StartTLS()
	defer server.Close()
	for _, test := range []struct{path string;required []string}{
		{"/v1/health",[]string{"status","protocol_version"}},
		{"/v1/identity",[]string{"agent_id","tls_spki_sha256","version","observed_at","hostname","platform","arch"}},
	}{
		resp,err:=server.Client().Get(server.URL+test.path)
		if err!=nil {t.Fatal(err)}
		var body map[string]any
		if err:=json.NewDecoder(resp.Body).Decode(&body);err!=nil {resp.Body.Close();t.Fatal(err)}
		resp.Body.Close()
		if resp.StatusCode!=200 {t.Fatalf("%s status %d",test.path,resp.StatusCode)}
		if resp.Header.Get("Cache-Control")!="no-store"||resp.Header.Get("X-EHA-Agent-ID")!=state.AgentID {t.Fatal("missing identity/security headers")}
		for _,key:=range test.required {if _,ok:=body[key];!ok{t.Errorf("%s missing %s",test.path,key)}}
		if test.path=="/v1/identity" {
			if body["agent_id"]!=state.AgentID||body["tls_spki_sha256"]!=state.SPKISHA256{t.Fatal("identity or fingerprint differs")}
			if body["platform"]!="fnos" {t.Fatal("platform mismatch")}
		}
	}
}
func TestEndpointMethodsAndPlainHTTPRejected(t *testing.T){
	state,err:=identity.LoadOrCreate(t.TempDir());if err!=nil {t.Fatal(err)}
	handler:=NewHandler(state,"linux","dev")
	for _,tt:=range []struct{method,path string;tls bool;want int}{
		{"POST","/v1/health",true,405},
		{"POST","/v1/identity",true,405},
		{"GET","/v1/other",true,404},
		{"POST","/v1/pairings",true,404},
		{"GET","/v1/identity",false,400},
	}{
		req:=httptest.NewRequest(tt.method,"https://localhost"+tt.path,nil)
		if !tt.tls {req.TLS=nil}
		rr:=httptest.NewRecorder()
		handler.ServeHTTP(rr,req)
		if rr.Code!=tt.want{t.Errorf("%s %s: got %d want %d",tt.method,tt.path,rr.Code,tt.want)}
	}
}
