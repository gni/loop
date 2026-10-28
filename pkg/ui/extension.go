package ui

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/resources"
	"loop/pkg/ui/render"
)

type ExtensionInfo = render.ExtensionInfo

func init() {
	render.ExtensionCounter = func(workspaceRoot string) int {
		return len(ListExtensions(workspaceRoot))
	}
}

func ListExtensions(workspaceRoot string,additional ...string)[]ExtensionInfo{
 directories:=append(tool.ExtensionSearchDirs(workspaceRoot),additional...)
 var extensions []ExtensionInfo
 for _,command:=range resources.Commands(directories){
  location:="global"
  if relative,err:=filepath.Rel(workspaceRoot,command.Path);err==nil&&relative!=".."&&!strings.HasPrefix(relative,".."+string(filepath.Separator)){location="project"}
  extensions=append(extensions,ExtensionInfo{Name:command.Name,Path:command.Path,Location:location})
 }
 return extensions
}

func RunExtension(
	a *agent.Agent,
	cmdName string,
	args []string,
	messages *[]db.Message,
	w io.Writer,
) (bool, error) {
	if a.Config.DisableLocalPlugins {
		return true, fmt.Errorf("local extensions are disabled for this workspace (run with trust to enable)")
	}

	name := strings.TrimPrefix(cmdName, "/")
	if !tool.IsSafeIdentifier(name) {
		return false, nil
	}

	targetCmd := "/" + strings.ToLower(name)
	var extPath string
	for _, ext := range ListExtensions(a.GetWorkspaceRoot(), a.Config.ExtensionDirectories...) {
		if ext.Name == targetCmd && a.ProjectResourceAllowed(ext.Path) {
			extPath = ext.Path
			break
		}
	}

	if extPath == "" {
		return false, nil
	}

	parentCtx := context.Background()
	if a != nil {
		parentCtx = a.Context()
	}
 var history []db.Message
 if messages!=nil{history=*messages}
 timeout:=15*time.Second
 if a.Config.ExtensionTimeout>0{timeout=time.Duration(a.Config.ExtensionTimeout)*time.Second}
 output,err:=resources.RunCommand(parentCtx,resources.Command{Name:targetCmd,Path:extPath},args,a.WorkspaceRoot,history,timeout)
 if output!=""{if _,writeErr:=io.WriteString(w,output);writeErr!=nil{return true,writeErr}}
 if err!=nil{return true,err}

	return true, nil
}
