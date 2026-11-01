// lint:expect depguard github.com/golang/glog

package depguard

import "github.com/golang/glog"

func UseGlog() { glog.Use() }
