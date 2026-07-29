//go:build linux

package platformselect

import (
	platformapi "github.com/sirrobot01/bifrost/internal/platform"
	platformimpl "github.com/sirrobot01/bifrost/internal/platform/linux"
)

func New() platformapi.Platform { return platformimpl.New() }
