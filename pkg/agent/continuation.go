package agent

import (
	"crypto/sha256"
	"fmt"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"regexp"
)

var nativeToolID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func nativeHistory(input []db.Message) []db.Message {
	output := message.CloneAll(input)
	normalize := func(id string) string {
		if id == "" || nativeToolID.MatchString(id) {
			return id
		}
		hash := sha256.Sum256([]byte(id))
		return fmt.Sprintf("call_%x", hash[:20])
	}
	for i := range output {
		output[i].ToolCallID = normalize(output[i].ToolCallID)
		for j := range output[i].ToolCalls {
			output[i].ToolCalls[j].ID = normalize(output[i].ToolCalls[j].ID)
		}
		for j := range output[i].ContentBlocks {
			output[i].ContentBlocks[j].ToolCallID = normalize(output[i].ContentBlocks[j].ToolCallID)
		}
	}
	return output
}
