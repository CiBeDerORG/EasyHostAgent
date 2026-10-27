package pairing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	agent = "11111111-1111-4111-8111-111111111111"
	controllerA = "22222222-2222-4222-8222-222222222222"
	controllerB = "44444444-4444-4444-8444-444444444444"
	deviceA = "33333333-3333-4333-8333-333333333333"
	deviceB = "55555555-5555-4555-8555-555555555555"
)
func newStore(t *testing.T) (*Store,string) {
	t.Helper()
	dir:=filepath.Join(t.TempDir(),"private")
	if err:=os.Mkdir(dir,0700);err!=nil {t.Fatal(err)}
	s,err:=New(dir,agent)
	if err!=nil {t.Fatal(err)}
	return s,dir
}
func TestOneTimeCodeDurableAuthorizationAndNoSecretsAtRest(t *testing.T) {
	s,dir:=newStore(t)
	code,expiry,err:=s.Generate()
	if err!=nil {t.Fatal(err)}
	if len(code)!=32||expiry.Sub(time.Now())>CodeLifetime+time.Second {t.Fatal("invalid code or expiry")}
	r,err:=s.Exchange(controllerA,deviceA,code)
	if err!=nil {t.Fatal(err)}
	if len(r.AccessToken)!=64||r.AgentID!=agent||!s.Authorized(controllerA,agent,r.AccessToken){t.Fatal("invalid grant")}
	if _,err=s.Exchange(controllerA,deviceA,code);!errors.Is(err,ErrNoCode){t.Fatalf("one-time reuse: %v",err)}
	body,err:=os.ReadFile(filepath.Join(dir,fileName))
	if err!=nil{t.Fatal(err)}
	if strings.Contains(string(body),code)||strings.Contains(string(body),r.AccessToken) {t.Fatal("plaintext secret persisted")}
	var data map[string]any
	if err:=json.Unmarshal(body,&data);err!=nil{t.Fatal(err)}
	other,err:=New(dir,agent)
	if err!=nil{t.Fatal(err)}
	if !other.Authorized(controllerA,agent,r.AccessToken) {t.Fatal("grant lost after restart")}
	if other.Authorized(controllerB,agent,r.AccessToken)||other.Authorized(controllerA,deviceA,r.AccessToken) {t.Fatal("cross-identity token accepted")}
	info,err:=os.Stat(filepath.Join(dir,fileName))
	if err!=nil||info.Mode().Perm()!=0600 {t.Fatalf("permissions %#v %v",info,err)}
}
func TestFiveWrongAttemptsRequireNewLocalCode(t *testing.T){
	s,dir:=newStore(t)
	code,_,err:=s.Generate();if err!=nil{t.Fatal(err)}
	for i:=1;i<=5;i++{
		_,err=s.Exchange(controllerA,deviceA,strings.Repeat("0",32))
		if i<5&&!errors.Is(err,ErrInvalidCode){t.Fatalf("attempt %d: %v",i,err)}
		if i==5&&!errors.Is(err,ErrRateLimited){t.Fatalf("5th attempt: %v",err)}
	}
	another,err:=New(dir,agent);if err!=nil{t.Fatal(err)}
	if _,err=another.Exchange(controllerA,deviceA,code);!errors.Is(err,ErrRateLimited){t.Fatalf("rate limit bypass after restart: %v",err)}
	fresh,_,err:=another.Generate();if err!=nil{t.Fatal(err)}
	if fresh==code{t.Fatal("code reused")}
	if _,err=another.Exchange(controllerA,deviceA,fresh);err!=nil{t.Fatal(err)}
}
func TestExpiredCodeDoesNotAuthorize(t *testing.T){
	s,_:=newStore(t)
	now:=time.Date(2026,10,27,0,0,0,0,time.UTC)
	s.now=func()time.Time{return now}
	code,_,err:=s.Generate();if err!=nil{t.Fatal(err)}
	now=now.Add(CodeLifetime)
	if _,err=s.Exchange(controllerA,deviceA,code);!errors.Is(err,ErrExpired){t.Fatalf("expired code accepted: %v",err)}
}
func TestNewPairingRevokesOldTokenOnlyOnSuccess(t *testing.T){
	s,_:=newStore(t)
	first,_,err:=s.Generate();if err!=nil{t.Fatal(err)}
	old,err:=s.Exchange(controllerA,deviceA,first);if err!=nil{t.Fatal(err)}
	next,_,err:=s.Generate();if err!=nil{t.Fatal(err)}
	if !s.Authorized(controllerA,agent,old.AccessToken){t.Fatal("old token revoked before new pairing")}
	if _,err=s.Exchange(controllerB,deviceB,"bad");!errors.Is(err,ErrInvalidCode){t.Fatalf("wrong code: %v",err)}
	if !s.Authorized(controllerA,agent,old.AccessToken){t.Fatal("old token revoked on bad code")}
	newGrant,err:=s.Exchange(controllerB,deviceB,next);if err!=nil{t.Fatal(err)}
	if s.Authorized(controllerA,agent,old.AccessToken) {t.Fatal("old token still works")}
	if !s.Authorized(controllerB,agent,newGrant.AccessToken){t.Fatal("new token denied")}
	if err=s.Revoke(controllerA,agent,old.AccessToken);!errors.Is(err,ErrUnauthorized){t.Fatal("old controller revoked new grant")}
	if err=s.Revoke(controllerB,agent,newGrant.AccessToken);err!=nil{t.Fatal(err)}
	if s.Authorized(controllerB,agent,newGrant.AccessToken){t.Fatal("revoked token still works")}
}
func TestInvalidStateFailsClosed(t *testing.T){
	s,dir:=newStore(t)
	code,_,err:=s.Generate();if err!=nil{t.Fatal(err)}
	path:=filepath.Join(dir,fileName)
	if err=os.WriteFile(path,[]byte("{broken"),0600);err!=nil{t.Fatal(err)}
	if _,err=New(dir,agent);err==nil{t.Fatal("corruption accepted")}
	if s.Authorized(controllerA,agent,"x")==true{t.Fatal("authorized on corrupt data")}
	buf,_:=os.ReadFile(path)
	if string(buf)!="{broken" {t.Fatal("corruption overwritten")}
	_ = code
}
func TestConcurrentExchangeOneWinner(t *testing.T){
	s,_:=newStore(t)
	code,_,err:=s.Generate();if err!=nil{t.Fatal(err)}
	var wg sync.WaitGroup
	results:=make(chan Result,10)
	for i:=0;i<10;i++{
		wg.Add(1)
		go func(){defer wg.Done();if grant,e:=s.Exchange(controllerA,deviceA,code);e==nil{results<-grant}}()
	}
	wg.Wait();close(results)
	count:=0
	for range results{count++}
	if count!=1{t.Fatalf("one-time code yielded %d successes",count)}
}
func TestSymlinkStateRefused(t *testing.T){
	s,dir:=newStore(t)
	other:=filepath.Join(t.TempDir(),"outside")
	if err:=os.WriteFile(other,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
	if err:=os.Symlink(other,filepath.Join(dir,fileName));err!=nil{t.Fatal(err)}
	if _,err:=s.Generate();err==nil{t.Fatal("symlink accepted")}
}
