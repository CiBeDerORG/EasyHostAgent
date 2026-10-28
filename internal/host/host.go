// Package host provides on-demand, authenticated Linux host inspection and
// durable software root settings. It intentionally has no polling goroutines.
package host

import (
 "bufio"
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "net"
 "net/http"
 "os"
 "os/exec"
 "path/filepath"
 "runtime"
 "strconv"
 "strings"
 "sync"
 "syscall"
 "time"
)

var (
 ErrInvalidRoot=errors.New("invalid software root")
 ErrManagedApps=errors.New("managed applications exist; migrate and verify before changing software root")
)
type Service struct {
 mu sync.Mutex
 dataDir, defaultRoot, platform, agentID string
}
type Settings struct {
 SoftwareRoot string `json:"software_root"`
}
type Disk struct {
 Mount string `json:"mount"`
 Total uint64 `json:"total_bytes"`
 Available uint64 `json:"available_bytes"`
}
type Docker struct {
 Available bool `json:"available"`
 EngineVersion string `json:"engine_version,omitempty"`
 ComposeAvailable bool `json:"compose_available"`
 ComposeVersion string `json:"compose_version,omitempty"`
 Error string `json:"error,omitempty"`
}
type Snapshot struct {
 AgentID string `json:"agent_id"`
 ObservedAt string `json:"observed_at"`
 Hostname string `json:"hostname"`
 Platform string `json:"platform"`
 Arch string `json:"arch"`
 CPU struct { LogicalCores int `json:"logical_cores"` } `json:"cpu"`
 Memory struct { Total uint64 `json:"total_bytes"`; Used uint64 `json:"used_bytes"` } `json:"memory"`
 Disks []Disk `json:"disks"`
 Docker Docker `json:"docker"`
 SoftwareRoot string `json:"software_root"`
}
func New(dataDir, defaultRoot, platform, agentID string) (*Service,error) {
 if err:=validateRoot(dataDir, defaultRoot);err!=nil{return nil,err}
 s:=&Service{dataDir:dataDir,defaultRoot:defaultRoot,platform:platform,agentID:agentID}
 if _,err:=s.Root();err!=nil{return nil,err}
 return s,nil
}
func validateRoot(dataDir, root string) error {
 if root=="" || !filepath.IsAbs(root) || filepath.Clean(root)!=root || root=="/" ||
   root==dataDir || strings.HasPrefix(root, dataDir+"/") || strings.HasPrefix(dataDir,root+"/") ||
   strings.ContainsAny(root,"\x00\n\r") {return ErrInvalidRoot}
 // Refuse symlinks in any existing component, not only the final segment.
 for p:=root;p!="/";p=filepath.Dir(p) {
  st,err:=os.Lstat(p)
  if errors.Is(err,os.ErrNotExist){continue}
  if err!=nil{return fmt.Errorf("%w: path inspection failed",ErrInvalidRoot)}
  if st.Mode()&os.ModeSymlink!=0 || !st.IsDir(){return ErrInvalidRoot}
 }
 return nil
}
func (s *Service) settingsPath() string{return filepath.Join(s.dataDir,"host-settings.json")}
func (s *Service) rootLocked()(string,error){
 data,err:=os.ReadFile(s.settingsPath())
 if errors.Is(err,os.ErrNotExist){return s.defaultRoot,nil}
 if err!=nil{return "",err}
 fi,err:=os.Lstat(s.settingsPath())
 if err!=nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0077!=0{return "",errors.New("insecure host settings file")}
 var st Settings
 if err:=json.Unmarshal(data,&st);err!=nil{return "",fmt.Errorf("invalid stored host settings: %w",err)}
 if err:=validateRoot(s.dataDir,st.SoftwareRoot);err!=nil{return "",err}
 return st.SoftwareRoot,nil
}
func(s *Service) Root()(string,error){s.mu.Lock();defer s.mu.Unlock();return s.rootLocked()}
func (s *Service) SetRoot(root string)(string,error){
 s.mu.Lock();defer s.mu.Unlock()
 if err:=validateRoot(s.dataDir,root);err!=nil{return "",err}
 current,err:=s.rootLocked();if err!=nil{return "",err}
 if current==root{return current,nil}
 // A future managed installation MUST create an entry under this directory
 // before execution. Fail closed if the registry cannot be read.
 apps:=filepath.Join(s.dataDir,"managed-apps")
 entries,err:=os.ReadDir(apps)
 if err==nil && len(entries)>0{return "",ErrManagedApps}
 if err!=nil && !errors.Is(err,os.ErrNotExist){return "",fmt.Errorf("inspect managed apps: %w",err)}
 // Don't overwrite an existing root setting through a symlink.
 if st,e:=os.Lstat(s.settingsPath());e==nil {
  if !st.Mode().IsRegular() || st.Mode().Perm()&0077!=0{return "",ErrInvalidRoot}
 }else if !errors.Is(e,os.ErrNotExist){return "",e}
 raw,_:=json.Marshal(Settings{SoftwareRoot:root})
 if err:=os.MkdirAll(s.dataDir,0700);err!=nil{return "",err}
 f,err:=os.CreateTemp(s.dataDir,".host-settings-*.tmp")
 if err!=nil{return "",err}
 defer os.Remove(f.Name())
 defer f.Close()
 if err:=f.Chmod(0600);err!=nil{return "",err}
 if _,err:=f.Write(raw);err!=nil{return "",err}
 if err:=f.Sync();err!=nil{return "",err}
 if err:=f.Close();err!=nil{return "",err}
 if err:=os.Rename(f.Name(),s.settingsPath());err!=nil{return "",err}
 if d,e:=os.Open(s.dataDir);e==nil{_ =d.Sync();_ =d.Close()}
 return root,nil
}
func(s *Service) Snapshot(ctx context.Context)(Snapshot,error){
 root,err:=s.Root();if err!=nil{return Snapshot{},err}
 hostName,_:=os.Hostname()
 x:=Snapshot{AgentID:s.agentID,ObservedAt:time.Now().UTC().Format(time.RFC3339),Hostname:hostName,Platform:s.platform,Arch:runtime.GOARCH,SoftwareRoot:root,Disks:[]Disk{}}
 x.CPU.LogicalCores=runtime.NumCPU()
 if x.CPU.LogicalCores<1{x.CPU.LogicalCores=1}
 x.Memory.Total,x.Memory.Used=memory()
 for _,p:=range []string{"/",root}{
  path:=p
  for{
   if _,e:=os.Stat(path);e==nil{break}
   parent:=filepath.Dir(path);if path==parent{path="/";break};path=parent
  }
  var stat syscall.Statfs_t
  if err:=syscall.Statfs(path,&stat);err!=nil{continue}
  found:=false;for _,v:=range x.Disks{if v.Mount==path{found=true}}
  if !found{x.Disks=append(x.Disks,Disk{Mount:path,Total:stat.Blocks*uint64(stat.Bsize),Available:stat.Bavail*uint64(stat.Bsize)})}
 }
 x.Docker=docker(ctx)
 return x,nil
}
func memory()(total,used uint64){
 f,err:=os.Open("/proc/meminfo");if err!=nil{return 0,0};defer f.Close()
 var available uint64
 sc:=bufio.NewScanner(f)
 for sc.Scan(){
  fields:=strings.Fields(sc.Text());if len(fields)<2{continue}
  n,e:=strconv.ParseUint(fields[1],10,64);if e!=nil{continue}
  switch fields[0]{case "MemTotal:":total=n*1024;case "MemAvailable:":available=n*1024}
 }
 if total>=available{used=total-available}
 return total,used
}
func docker(ctx context.Context)Docker{
 result:=Docker{Available:false,ComposeAvailable:false}
 // Docker's Unix socket is accessed locally, never via an untrusted DOCKER_HOST.
 transport:=&http.Transport{DialContext:func(ctx context.Context,_,_ string)(net.Conn,error){
  return (&net.Dialer{Timeout:2*time.Second}).DialContext(ctx,"unix","/var/run/docker.sock")
 }}
 defer transport.CloseIdleConnections()
 client:=&http.Client{Transport:transport,Timeout:3*time.Second}
 req,e:=http.NewRequestWithContext(ctx,"GET","http://docker.local/version",nil)
 if e!=nil{return result}
 resp,e:=client.Do(req)
 if e!=nil{result.Error="Docker Engine unavailable";return result}
 defer resp.Body.Close()
 if resp.StatusCode!=200{result.Error="Docker Engine unavailable";return result}
 var info struct{Version string `json:"Version"`}
 if json.NewDecoder(http.MaxBytesReader(nil,resp.Body,4096)).Decode(&info)!=nil{
  result.Error="Invalid Docker Engine response";return result
 }
 result.Available=true;result.EngineVersion=info.Version
 if _,e:=exec.LookPath("docker");e!=nil{return result}
 cctx,cancel:=context.WithTimeout(ctx,3*time.Second);defer cancel()
 out,e:=exec.CommandContext(cctx,"docker","compose","version","--short").Output()
 if e==nil{result.ComposeAvailable=true;result.ComposeVersion=strings.TrimSpace(string(out))}
 return result
}
