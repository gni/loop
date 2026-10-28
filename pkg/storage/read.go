package storage

import (
 "fmt"
 "io"
 "os"
)

func ReadFile(path string,limit int64)([]byte,error){
 file,err:=os.Open(path)
 if err!=nil{return nil,err}
 defer file.Close()
 info,err:=file.Stat()
 if err!=nil{return nil,err}
 if !info.Mode().IsRegular(){return nil,fmt.Errorf("%s is not a regular file",path)}
 if info.Size()>limit{return nil,fmt.Errorf("%s exceeds %d bytes",path,limit)}
 data,err:=io.ReadAll(io.LimitReader(file,limit+1))
 if err!=nil{return nil,err}
 if int64(len(data))>limit{return nil,fmt.Errorf("%s exceeds %d bytes",path,limit)}
 return data,nil
}
