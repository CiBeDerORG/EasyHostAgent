package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)
func TestConstantTimeTokenHelper(t *testing.T) {
	if !EqualToken("same","same") || EqualToken("same","diff") || EqualToken("same","short") { t.Fatal("token comparison incorrect") }
}
func TestProtectedHandlerDeniesUntilPaired(t *testing.T) {
	const agentID="11111111-1111-4111-8111-111111111111"
	const controller="22222222-2222-4222-8222-222222222222"
	const token="example-secret-token-32-bytes-long----"
	next := http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){ w.WriteHeader(http.StatusNoContent) })
	for _, tc := range []struct{name, controller, agent, auth string; tls bool; verifier Verifier; want int}{
		{"not_tls",controller,agentID,"Bearer "+token,false,nil,400},
		{"no_verifier",controller,agentID,"Bearer "+token,true,nil,401},
		{"missing_agent",controller,"","Bearer "+token,true,VerifyFunc(func(_,_,_ string)bool{return true}),401},
		{"wrong_controller",controller,agentID,"Bearer wrong",true,VerifyFunc(func(_,_,_ string)bool{return true}),401},
		{"wrong_agent",controller,"other","Bearer "+token,true,VerifyFunc(func(_,_,_ string)bool{return true}),401},
		{"valid",controller,agentID,"Bearer "+token,true,VerifyFunc(func(c,a,s string)bool{return c==controller&&a==agentID&&EqualToken(s,token)}),204},
	} {
		t.Run(tc.name,func(t *testing.T){
			req:=httptest.NewRequest("GET","https://localhost/v1/host",nil)
			if !tc.tls {req.TLS=nil}else{req.TLS=&tls.ConnectionState{}}
			req.Header.Set("X-EHA-Controller-ID",tc.controller)
			req.Header.Set("X-EHA-Agent-ID",tc.agent)
			req.Header.Set("Authorization",tc.auth)
			rec:=httptest.NewRecorder()
			RequireController(agentID,tc.verifier,next).ServeHTTP(rec,req)
			if rec.Code!=tc.want {t.Fatalf("got %d want %d: %s",rec.Code,tc.want,rec.Body.String())}
			if rec.Code!=204&&strings.Contains(rec.Body.String(),token){t.Fatal("credential leaked")}
		})
	}
}
