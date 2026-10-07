package middleware

import (
	"net/http"

	"github.com/1Panel-dev/1Panel/core/app/api/v2/helper"
	"github.com/1Panel-dev/1Panel/core/app/repo"
	"github.com/gin-gonic/gin"
)

// upgradeProgressPath must stay reachable while SystemStatus is locked to
// "Upgrading": the frontend polls it every second to render the live upgrade
// progress and logs. Passing GlobalLoading here does not bypass auth — the
// route group still applies SessionAuth.
const upgradeProgressPath = "/api/v2/core/settings/upgrade/progress"

func GlobalLoading() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet && c.Request.URL.Path == upgradeProgressPath {
			c.Next()
			return
		}
		settingRepo := repo.NewISettingRepo()
		status, err := settingRepo.GetValueByKey("SystemStatus")
		if err != nil {
			helper.InternalServer(c, err)
			return
		}
		if status != "Free" {
			helper.ErrorWithDetail(c, 407, status, err)
			return
		}
		c.Next()
	}
}
