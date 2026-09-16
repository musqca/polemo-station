package station

import (
	"time"

	"github.com/wonli/aqi/ws"
)

func (s *Service) Ping(a *ws.Context) {
	a.SendActionData("v1.pong", map[string]any{"at": time.Now().UnixMilli()})
}
