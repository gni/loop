package message

type Usage struct {
 Input int `json:"input"`
 Output int `json:"output"`
 CacheRead int `json:"cache_read"`
 CacheWrite int `json:"cache_write"`
 Cost *float64 `json:"cost"`
 Responses int `json:"responses"`
}

func MergeUsage(first,second Usage)Usage{
 total:=Usage{Input:first.Input+second.Input,Output:first.Output+second.Output,CacheRead:first.CacheRead+second.CacheRead,CacheWrite:first.CacheWrite+second.CacheWrite,Responses:first.Responses+second.Responses}
 if first.Cost!=nil&&second.Cost!=nil{cost:=*first.Cost+*second.Cost;total.Cost=&cost}
 return total
}

func TotalUsage(messages []Message)Usage{
 cost:=0.0
 total:=Usage{Cost:&cost}
 for _,entry:=range messages{
  if entry.Usage!=nil{total=MergeUsage(total,*entry.Usage)}
  if entry.Role!=RoleAssistant{continue}
  if entry.PromptTokens==0&&entry.CompletionTokens==0&&entry.Content==""&&len(entry.ToolCalls)==0{continue}
  total=MergeUsage(total,Usage{Input:entry.PromptTokens,Output:entry.CompletionTokens,CacheRead:entry.CacheReadTokens,CacheWrite:entry.CacheWriteTokens,Cost:entry.Cost,Responses:1})
 }
 return total
}
