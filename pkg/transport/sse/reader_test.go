package sse

import (
 "context"
 "io"
 "strings"
 "testing"
)

func TestMultilineEventsAndActivity(t *testing.T){
 reader:=NewReader(strings.NewReader(": ping\r\nevent: message\r\nid: 7\r\ndata: {\r\ndata: \"ok\":true}\r\n\r\n"))
 activity:=0;reader.OnActivity=func(){activity++}
 event,err:=reader.Read(context.Background())
 if err!=nil||event.Name!="message"||event.ID!="7"||event.Data!="{\n\"ok\":true}"||activity!=6{t.Fatalf("event: %+v activity %d error %v",event,activity,err)}
 if _,err:=reader.Read(context.Background());err!=io.EOF{t.Fatal(err)}
}

func TestEventSizeIsBounded(t *testing.T){
 reader:=NewReader(strings.NewReader("data: "+strings.Repeat("x",MaxEventBytes)+"\n\n"))
 if _,err:=reader.Read(context.Background());err==nil{t.Fatal("oversized event accepted")}
}
