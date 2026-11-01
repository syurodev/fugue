// lint:expect depguard github.com/sirupsen/logrus

package depguard

import "github.com/sirupsen/logrus"

func UseLogrus() { logrus.Use() }
