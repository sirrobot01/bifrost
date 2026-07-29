//go:build darwin

package platformselect

import (
	platformapi "github.com/sirrobot01/bifrost/internal/platform"
	platformimpl "github.com/sirrobot01/bifrost/internal/platform/darwin"
)

func New() platformapi.Platform { return platformimpl.New() }
