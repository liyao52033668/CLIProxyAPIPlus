package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var emptyGeminiUserTurnJSON = []byte(`{"role":"user","parts":[{"text":""}]}`)

// EnsureGeminiLeadingUserContent ensures that the contents array starts with a user turn.
func EnsureGeminiLeadingUserContent(payload []byte, path string) []byte {
	if gjson.GetBytes(payload, path+".0.role").String() != "model" {
		return payload
	}
	contents := util.GetGJSONBytesNoCopy(payload, path)
	if !contents.IsArray() || len(contents.Array()) == 0 {
		return payload
	}

	items := make([][]byte, 0, len(contents.Array())+1)
	items = append(items, emptyGeminiUserTurnJSON)
	for _, content := range contents.Array() {
		items = append(items, []byte(content.Raw))
	}
	out, errSet := sjson.SetRawBytes(payload, path, util.JoinRawArrayBytes(items))
	if errSet != nil {
		return payload
	}
	return out
}
