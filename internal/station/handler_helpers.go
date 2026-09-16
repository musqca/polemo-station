package station

import (
	"encoding/json"

	"github.com/wonli/aqi/ws"
)

func decodeParams(a *ws.Context, target any) bool {
	return a.Params != "" && json.Unmarshal([]byte(a.Params), target) == nil
}

func clientDeviceID(a *ws.Context) string {
	value := a.Client.GetKey("stationDeviceId")
	if value == nil {
		return ""
	}
	return value.String()
}
