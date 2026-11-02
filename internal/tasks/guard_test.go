package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

type mockInventory struct {
	snapshot dockerengine.GroupedInventory
	err error
	calls int
}
func (m *mockInventory) InspectGrouped(context.Context) (dockerengine.GroupedInventory, error) {
	m.calls++
	return m.snapshot, m.err
}
func guardedFixture(t *testing.T) (testEnvironment, *Store, *mockInventory) {
	t.Helper()
	env := setup(t)
	if _, err := env.approvals.Approve(env.digest, sourceID, "sub2api", gitRevision); err != nil {
		t.Fatal(err)
	}
	cache, err := bundles.New(env.dir)
	if err != nil {
		t.Fatal(err)
	}
	inventory := &mockInventory{snapshot:dockerengine.GroupedInventory{Complete:true,Resources:[]dockerengine.Resource{}}}
	queue, err := NewGuarded(env.dir, agentID, env.grants, env.approvals, cache, inventory,
		filepath.Join(t.TempDir(), "software"))
	if err != nil {
		t.Fatal(err)
	}
	return env,queue,inventory
}

func TestGuardedAdmissionUsesImmutableManifestAndLiveDocker(t *testing.T) {
	env,queue,inventory:=guardedFixture(t)
	task,replay,err:=queue.Submit(controllerID,env.token,input(env.digest))
	if err!=nil || replay || task.ExecutionEnabled || task.Footprint.ComposeProject!="sub2api" ||
		len(task.Footprint.Ports)!=1 || task.Footprint.Ports[0].Published!=7010 ||
		len(task.Footprint.NamedVolumes)!=1 || inventory.calls!=1 {
		t.Fatalf("实际资源准入异常: %+v repeated=%v inventory=%d err=%v",task,replay,inventory.calls,err)
	}
	if _,err:=queue.Preflight(controllerID,env.token,task.ID);err!=nil{
		t.Fatalf("执行前只读检查错误: %v",err)
	}
	if err:=env.approvals.Revoke(env.digest);err!=nil{t.Fatal(err)}
	if _,err:=queue.Preflight(controllerID,env.token,task.ID);err==nil {
		t.Fatal("撤销本机批准后仍然预检成功")
	}
}

func TestGuardRejectsIncompleteDockerAndConflictingResources(t *testing.T) {
	cases:=[]struct{
		name string
		inventory dockerengine.GroupedInventory
		err error
		want error
	}{
		{"扫描失败",dockerengine.GroupedInventory{},errors.New("docker socket denied"),ErrInventoryUnavailable},
		{"不完整扫描",dockerengine.GroupedInventory{Complete:false},nil,ErrInventoryUnavailable},
		{"TCP 主机端口冲突",dockerengine.GroupedInventory{Complete:true,Resources:[]dockerengine.Resource{{
			Kind:dockerengine.KindContainer,Containers:[]dockerengine.Container{{
				ID:"external-port",Ports:[]dockerengine.Port{{IP:"0.0.0.0",PublicPort:7010,Type:"tcp"}},
			}},
		}}},nil,ErrConflict},
		{"命名卷冲突",dockerengine.GroupedInventory{Complete:true,Resources:[]dockerengine.Resource{{
			Kind:dockerengine.KindContainer,Containers:[]dockerengine.Container{{
				ID:"external-volume",Mounts:[]dockerengine.Mount{{Type:"volume",Name:"sub2api_pgdata"}},
			}},
		}}},nil,ErrConflict},
		{"同名 Compose 项目",dockerengine.GroupedInventory{Complete:true,Resources:[]dockerengine.Resource{{
			Kind:dockerengine.KindCompose,ComposeProject:"sub2api",
			Containers:[]dockerengine.Container{{ID:"external-compose"}},
		}}},nil,ErrConflict},
		{"不同 UDP 端口不冲突",dockerengine.GroupedInventory{Complete:true,Resources:[]dockerengine.Resource{{
			Kind:dockerengine.KindContainer,Containers:[]dockerengine.Container{{
				ID:"other-protocol",Ports:[]dockerengine.Port{{IP:"0.0.0.0",PublicPort:7010,Type:"udp"}},
			}},
		}}},nil,nil},
	}
	for _,tc:=range cases{
		t.Run(tc.name,func(t *testing.T){
			env,queue,docker:=guardedFixture(t)
			docker.snapshot=tc.inventory
			docker.err=tc.err
			task,_,err:=queue.Submit(controllerID,env.token,input(env.digest))
			if tc.want==nil && (err!=nil || task.Status!=Prepared) {
				t.Fatalf("不冲突的 Docker 资源被拒绝: %v",err)
			}
			if tc.want!=nil && !errors.Is(err,tc.want) {
				t.Fatalf("未按预期拒绝 Docker 资源: got=%v want=%v",err,tc.want)
			}
		})
	}
}

func TestStalePreparedTaskRecoveryRequiresLocalExpiredGrant(t *testing.T){
	env,queue,_:=guardedFixture(t)
	first,_,err:=queue.Submit(controllerID,env.token,input(env.digest))
	if err!=nil {t.Fatal(err)}
	if _,err:=queue.RecoverStaleLocal(first.ID,controllerID,"sub2api");!errors.Is(err,ErrConflict) {
		t.Fatalf("仍然有效的控制中心任务被提前回收: %v",err)
	}
	code,_,err:=env.grants.Generate()
	if err!=nil{t.Fatal(err)}
	newController:="55555555-5555-4555-8555-555555555555"
	newPair,err:=env.grants.Exchange(newController,deviceID,code)
	if err!=nil{t.Fatal(err)}
	if _,err:=queue.Preflight(controllerID,env.token,first.ID);!errors.Is(err,ErrUnauthorized){
		t.Fatalf("旧控制中心在重新配对后继续通过预检: %v",err)
	}
	if _,err:=queue.RecoverStaleLocal(first.ID,controllerID,"wrong");!errors.Is(err,ErrConflict){
		t.Fatalf("本机未核对应用仍然恢复旧任务: %v",err)
	}
	recovered,err:=queue.RecoverStaleLocal(first.ID,controllerID,"sub2api")
	if err!=nil || recovered.Status!=Superseded {
		t.Fatalf("未能回收旧配对准备任务: %+v %v",recovered,err)
	}
	repeated,err:=queue.RecoverStaleLocal(first.ID,controllerID,"sub2api")
	if err!=nil || repeated.UpdatedAt!=recovered.UpdatedAt {
		t.Fatalf("本机恢复不具幂等性: %+v %v",repeated,err)
	}
	second,_,err:=queue.Submit(newController,newPair.AccessToken,
		Input{RequestID:"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",Action:"install",BundleSHA256:env.digest})
	if err!=nil || second.Status!=Prepared || second.ID==first.ID{
		t.Fatalf("旧预留未释放或新控制中心无法安全创建: %+v %v",second,err)
	}
}

func TestManifestResourceClaimsRejectAmbiguousInputs(t *testing.T){
	good:=`schema_version: 1
id: sub2api
runtime:
  kind: docker_compose
  compose_project: sub2api
config:
  - key: HTTP_PORT
    type: port
    default: 7010
  - key: BIND_IP
    type: ipv4
    default: 0.0.0.0
ports:
  - protocol: tcp
    host_port_key: HTTP_PORT
    bind_address_key: BIND_IP
data:
  paths: [sub2api/data/app]
  named_volumes: [sub2api_pgdata]
  default_retain: true
`
	if v,err:=ParseFootprint([]byte(good),"sub2api");err!=nil || v.ComposeProject!="sub2api"{
		t.Fatalf("合法资源声明未通过: %+v %v",v,err)
	}
	mutations:=[]string{
		strings.Replace(good,"default: 7010","default: 70000",1),
		strings.Replace(good,"default: 7010","default: ${UNRESOLVED}",1),
		strings.Replace(good,"default: 0.0.0.0","default: invalid",1),
		strings.Replace(good,"compose_project: sub2api","compose_project: other\n  compose_project: sub2api",1),
		strings.Replace(good,"sub2api/data/app","../data",1),
		strings.Replace(good,"default_retain: true","default_retain: false",1),
		strings.Replace(good,"host_port_key: HTTP_PORT","host_port_key: UNKNOWN",1),
		strings.Replace(good,"protocol: tcp","protocol: sctp",1),
		good+"---\nid: other\n",
	}
	for i,raw:=range mutations{
		if _,err:=ParseFootprint([]byte(raw),"sub2api");err==nil{
			t.Fatalf("模糊或危险资源声明 #%d 被误判为合法",i)
		}
	}
}
