package runtime

import (
 "context"
 "os"
 "path/filepath"
 "testing"
 "loop/pkg/config"
 "loop/pkg/packages"
)

func TestPackageReloadRemovesResourcesAndRollsBackBrokenReplacement(t *testing.T){
 directory:=t.TempDir();source:=t.TempDir()
 skill:=filepath.Join(source,"skills","fixture","SKILL.md")
 plugin:=filepath.Join(source,"plugins","fixture.sh")
 for _,path:=range []string{skill,plugin}{if err:=os.MkdirAll(filepath.Dir(path),0700);err!=nil{t.Fatal(err)}}
 if err:=os.WriteFile(skill,[]byte("---\nname: fixture\ndescription: A fixture\n---\noriginal instructions"),0600);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(plugin,[]byte("#!/bin/sh\nprintf success\n"),0700);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(plugin+".json",[]byte(`{"name":"fixture","description":"fixture","parameters":{"type":"object","properties":{}}}`),0600);err!=nil{t.Fatal(err)}
 hook:=filepath.Join(source,"input.sh")
 if err:=os.WriteFile(hook,[]byte("#!/bin/sh\nprintf '{\"prompt\":\"transformed\"}'\n"),0700);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(source,"loop.json"),[]byte(`{"api_version":1,"name":"fixture","skills":["skills"],"plugins":["plugins"],"hooks":{"input":"input.sh"},"providers":{"fixture-profile":{"endpoint":"http://localhost:1"}}}`),0600);err!=nil{t.Fatal(err)}
 manager:=packages.Manager{Directory:directory}
 if _,err:=manager.Install(context.Background(),source);err!=nil{t.Fatal(err)}
 cfg:=config.DefaultConfig();cfg.DisableProjectPlugins=true
 s,err:=New(Options{Config:cfg,ConfigPath:filepath.Join(directory,"config.json"),Workspace:directory,NoSession:true});if err!=nil{t.Fatal(err)}
 defer s.Close(context.Background())
 if _,exists:=s.Agent.Registry.Lookup("plugin__fixture");!exists{t.Fatal("package tool not installed")}
 if prompt,err:=s.transformInput(context.Background(),"original");err!=nil||prompt!="transformed"{t.Fatalf("input hook: %q %v",prompt,err)}
 if err:=os.WriteFile(skill,[]byte("---\nname: [broken\n---\nreplacement"),0600);err!=nil{t.Fatal(err)}
 if err:=s.Reload();err==nil{t.Fatal("broken replacement did not fail")}
 if skills:=s.Agent.GetActiveSkills();len(skills)!=1||skills[0].Content!="original instructions"{t.Fatal("failed reload lost previous resources")}
 if err:=manager.Remove("fixture");err!=nil{t.Fatal(err)}
 if err:=s.Reload();err!=nil{t.Fatal(err)}
 if _,exists:=s.Agent.Registry.Lookup("plugin__fixture");exists{t.Fatal("removed package tool remained registered")}
 if len(s.Agent.GetActiveSkills())!=0{t.Fatal("removed package skill remained loaded")}
 if _,exists:=s.Agent.Config.Providers["fixture-profile"];exists{t.Fatal("removed package provider remained registered")}
 if prompt,err:=s.transformInput(context.Background(),"original");err!=nil||prompt!="original"{t.Fatal("removed package hook remained registered")}
}
